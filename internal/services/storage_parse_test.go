package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// stdoutAgent answers every exec.run with fixed stdout per command.
type stdoutAgent struct {
	stdout map[string]string
}

func (a *stdoutAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return nil, fmt.Errorf("unhandled %s", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	out, ok := a.stdout[p.Cmd]
	if !ok {
		return nil, fmt.Errorf("unexpected command %q", p.Cmd)
	}
	return json.Marshal(map[string]any{"stdout": out, "stderr": "", "exitCode": 0})
}

// useStdoutAgent installs the stub for the rest of the test. The agent
// client is process-global, so these tests never run in parallel.
func useStdoutAgent(t *testing.T, stdout map[string]string) {
	t.Helper()
	netutil.SetAgentClient(&stdoutAgent{stdout: stdout})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
}

const smartctlSATA = `smartctl 7.3 2022-02-28 r5338 [x86_64-linux-6.1.0-18-amd64] (local build)
=== START OF INFORMATION SECTION ===
Device Model:     Samsung SSD 870 EVO 1TB
Serial Number:    S6PUNX0T123456A
=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED
ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  5 Reallocated_Sector_Ct   0x0033   100   100   010    Pre-fail  Always       -       3
  9 Power_On_Hours          0x0032   097   097   000    Old_age   Always       -       12345
194 Temperature_Celsius     0x0022   066   052   000    Old_age   Always       -       34
`

const smartctlNVMeFailed = `=== START OF INFORMATION SECTION ===
Model Number:                       WD Blue SN570 1TB
SMART overall-health self-assessment test result: FAILED!
Temperature Sensor 1:               41 Celsius
`

// TestSMARTInfoParsesAttributes pins what GetSMARTInfo reads from
// smartctl for a SATA disk and for a failing NVMe disk.
func TestSMARTInfoParsesAttributes(t *testing.T) {
	svc := services.NewStorageService(&config.Config{})

	useStdoutAgent(t, map[string]string{"smartctl": smartctlSATA})
	sata, err := svc.GetSMARTInfo(context.Background(), "/dev/sda")
	if err != nil {
		t.Fatalf("sata: %v", err)
	}
	wantSATA := services.SMARTInfo{Device: "/dev/sda", Model: "Samsung SSD 870 EVO 1TB",
		Temperature: 34, PowerOnHours: 12345, HealthOK: true, Errors: 3}
	if *sata != wantSATA {
		t.Errorf("sata = %+v, want %+v", *sata, wantSATA)
	}

	useStdoutAgent(t, map[string]string{"smartctl": smartctlNVMeFailed})
	nvme, err := svc.GetSMARTInfo(context.Background(), "/dev/nvme0n1")
	if err != nil {
		t.Fatalf("nvme: %v", err)
	}
	wantNVMe := services.SMARTInfo{Device: "/dev/nvme0n1", Model: "WD Blue SN570 1TB",
		Temperature: 41, HealthOK: false}
	if *nvme != wantNVMe {
		t.Errorf("nvme = %+v, want %+v", *nvme, wantNVMe)
	}
}

// failingAgent answers every exec.run with the error the agent returns
// for a command that exited non-zero.
type failingAgent struct{ msg string }

func (a failingAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New(a.msg)
}

// TestSMARTInfoIsNotHealthyWhenSmartctlFails is the regression test.
// Any error mentioning an exit status was accepted with the output
// discarded, so the result was an empty record marked healthy. smartctl
// sets bit 3 of its exit status for a disk whose self-assessment failed,
// which made exactly that disk read as healthy.
func TestSMARTInfoIsNotHealthyWhenSmartctlFails(t *testing.T) {
	netutil.SetAgentClient(failingAgent{msg: "rpc error -32000: exec smartctl: exit status 8 (stderr: )"})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
	svc := services.NewStorageService(&config.Config{})

	info, err := svc.GetSMARTInfo(context.Background(), "/dev/sda")
	if err == nil {
		t.Fatalf("a failed smartctl run was accepted: %+v", *info)
	}
}

// lsblkTree is `lsblk --json` for a root disk with an LVM root, a disk
// whose model has spaces and whose partition is mounted, and an unused
// disk that reports no model.
const lsblkTree = `{"blockdevices": [
  {"name": "sda", "size": "238.5G", "model": "Samsung SSD 860 EVO", "type": "disk", "mountpoint": null,
   "children": [
     {"name": "sda1", "size": "512M", "model": null, "type": "part", "mountpoint": "/boot/efi"},
     {"name": "sda2", "size": "238G", "model": null, "type": "part", "mountpoint": null,
      "children": [{"name": "vg-root", "size": "238G", "model": null, "type": "lvm", "mountpoint": "/"}]}
   ]},
  {"name": "sdb", "size": "1.8T", "model": "WDC WD20EFRX-68E", "type": "disk", "mountpoint": null,
   "children": [{"name": "sdb1", "size": "1.8T", "model": null, "type": "part", "mountpoint": "/srv"}]},
  {"name": "sdc", "size": "14.9G", "model": null, "type": "disk", "mountpoint": null},
  {"name": "sr0", "size": "1024M", "model": "DVD-ROM", "type": "rom", "mountpoint": null}
]}`

// TestDiscoverDisksReadsEveryDisk is the regression test. The disks were
// read from lsblk's column output split on spaces, so a model name with a
// space moved the type column and an absent model removed it, and both
// disks were dropped. A disk whose partition held a mount was also listed
// as free.
func TestDiscoverDisksReadsEveryDisk(t *testing.T) {
	useStdoutAgent(t, map[string]string{"lsblk": lsblkTree})
	svc := services.NewStorageService(&config.Config{})

	disks, err := svc.DiscoverDisks(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := []services.AvailableDisk{
		{Device: "/dev/sda", Model: "Samsung SSD 860 EVO", Size: "238.5G", Type: "disk", InUse: true},
		{Device: "/dev/sdb", Model: "WDC WD20EFRX-68E", Size: "1.8T", Type: "disk", InUse: true},
		{Device: "/dev/sdc", Size: "14.9G", Type: "disk"},
	}
	if !slices.Equal(disks, want) {
		t.Errorf("disks = %+v\nwant    %+v", disks, want)
	}
}

const mdadmDetail = `/dev/md0:
           Version : 1.2
        Raid Level : raid1
        Array Size : 976630464 (931.39 GiB 1000.07 GB)
     Total Devices : 2
             State : clean
    Active Devices : 2

    Number   Major   Minor   RaidDevice State
       0       8        1        0      active sync   /dev/sda1
       1     259        2        1      active sync   /dev/nvme0n1p2
`

// TestRAIDStatusParsesDetail pins what GetRAIDStatus reads from
// `mdadm --detail`.
func TestRAIDStatusParsesDetail(t *testing.T) {
	useStdoutAgent(t, map[string]string{"mdadm": mdadmDetail})
	svc := services.NewStorageService(&config.Config{})

	status, err := svc.GetRAIDStatus(context.Background())
	if err != nil {
		t.Fatalf("raid: %v", err)
	}
	if status.Device != "/dev/md0" || status.Level != "raid1" || status.State != "clean" ||
		status.ActiveDisks != 2 || status.TotalDisks != 2 {
		t.Errorf("status = %+v", *status)
	}
	want := []services.DiskMember{{Device: "/dev/sda1", State: "active"}, {Device: "/dev/nvme0n1p2", State: "active"}}
	if len(status.Members) != len(want) {
		t.Fatalf("members = %+v, want %+v", status.Members, want)
	}
	for i := range want {
		if status.Members[i] != want[i] {
			t.Errorf("member %d = %+v, want %+v", i, status.Members[i], want[i])
		}
	}
}
