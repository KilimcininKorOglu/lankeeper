package services_test

import (
	"context"
	"encoding/json"
	"fmt"
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
