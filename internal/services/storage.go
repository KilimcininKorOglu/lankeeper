package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type StorageService struct {
	cfg *config.Config
}

func NewStorageService(cfg *config.Config) *StorageService {
	return &StorageService{cfg: cfg}
}

type RAIDStatus struct {
	Device      string
	Level       string
	State       string
	ActiveDisks int
	TotalDisks  int
	Members     []DiskMember
}

type DiskMember struct {
	Device string
	State  string
}

type SMARTInfo struct {
	Device       string
	Model        string
	Temperature  int
	PowerOnHours int
	HealthOK     bool
	Errors       int
}

type DiskUsage struct {
	Filesystem string
	Size       string
	Used       string
	Available  string
	UsePercent string
	MountPoint string
}

func (s *StorageService) GetRAIDStatus(ctx context.Context) (*RAIDStatus, error) {
	device := s.cfg.Storage.RAID.Device
	if device == "" {
		device = "/dev/md0"
	}

	out, err := netutil.RunSimple(ctx, "mdadm", "--detail", device)
	if err != nil {
		return nil, fmt.Errorf("mdadm detail: %w", err)
	}

	status := &RAIDStatus{Device: device}
	for line := range strings.SplitSeq(out, "\n") {
		status.applyDetailLine(strings.TrimSpace(line))
	}
	return status, nil
}

// applyDetailLine records whatever one line of `mdadm --detail` output
// carries.
func (status *RAIDStatus) applyDetailLine(line string) {
	if after, ok := strings.CutPrefix(line, "Raid Level :"); ok {
		status.Level = strings.TrimSpace(after)
	}
	if after, ok := strings.CutPrefix(line, "State :"); ok {
		status.State = strings.TrimSpace(after)
	}
	if after, ok := strings.CutPrefix(line, "Active Devices :"); ok {
		_, _ = fmt.Sscanf(after, "%d", &status.ActiveDisks)
	}
	if after, ok := strings.CutPrefix(line, "Total Devices :"); ok {
		_, _ = fmt.Sscanf(after, "%d", &status.TotalDisks)
	}
	if strings.Contains(line, "/dev/sd") || strings.Contains(line, "/dev/nvme") {
		if fields := strings.Fields(line); len(fields) >= 7 {
			status.Members = append(status.Members, DiskMember{
				Device: fields[len(fields)-1],
				State:  fields[4],
			})
		}
	}
}

func (s *StorageService) GetSMARTInfo(ctx context.Context, device string) (*SMARTInfo, error) {
	out, err := netutil.RunSimple(ctx, "smartctl", "-a", device)
	if err != nil && !strings.Contains(err.Error(), "exit status") {
		return nil, fmt.Errorf("smartctl: %w", err)
	}

	info := &SMARTInfo{Device: device, HealthOK: true}
	for line := range strings.SplitSeq(out, "\n") {
		info.applySMARTLine(strings.TrimSpace(line))
	}
	return info, nil
}

// applySMARTLine records whatever one line of `smartctl -a` output
// carries.
func (info *SMARTInfo) applySMARTLine(line string) {
	if strings.HasPrefix(line, "Device Model:") || strings.HasPrefix(line, "Model Number:") {
		info.Model = strings.TrimSpace(line[strings.Index(line, ":")+1:])
	}
	if strings.Contains(line, "Temperature_Celsius") || strings.Contains(line, "Temperature Sensor") {
		scanTemperature(strings.Fields(line), &info.Temperature)
	}
	if strings.Contains(line, "Power_On_Hours") {
		scanRawValue(line, &info.PowerOnHours)
	}
	if strings.Contains(line, "SMART overall-health") && strings.Contains(line, "FAILED") {
		info.HealthOK = false
	}
	if strings.Contains(line, "Reallocated_Sector") {
		scanRawValue(line, &info.Errors)
	}
}

// scanTemperature reads the number before "Celsius", falling back to the
// attribute's raw value when that yields nothing.
func scanTemperature(fields []string, temp *int) {
	for i, f := range fields {
		if f == "Celsius" && i > 0 {
			_, _ = fmt.Sscanf(fields[i-1], "%d", temp)
		}
	}
	if *temp == 0 && len(fields) >= 10 {
		_, _ = fmt.Sscanf(fields[9], "%d", temp)
	}
}

// scanRawValue reads the RAW_VALUE column of a SMART attribute line.
func scanRawValue(line string, dst *int) {
	if fields := strings.Fields(line); len(fields) >= 10 {
		_, _ = fmt.Sscanf(fields[9], "%d", dst)
	}
}

func (s *StorageService) GetDiskUsage(ctx context.Context) ([]DiskUsage, error) {
	out, err := netutil.RunSimple(ctx, "df", "-h", "--output=source,size,used,avail,pcent,target")
	if err != nil {
		return nil, fmt.Errorf("df: %w", err)
	}

	var usages []DiskUsage
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if !strings.HasPrefix(fields[0], "/dev/") {
			continue
		}
		usages = append(usages, DiskUsage{
			Filesystem: fields[0],
			Size:       fields[1],
			Used:       fields[2],
			Available:  fields[3],
			UsePercent: fields[4],
			MountPoint: fields[5],
		})
	}

	return usages, nil
}

func (s *StorageService) SetHDDStandby(ctx context.Context, device string, timeout int) error {
	_, err := netutil.Run(ctx, "hdparm", "-S", fmt.Sprintf("%d", timeout), device)
	return err
}

type AvailableDisk struct {
	Device string
	Model  string
	Size   string
	Type   string
	InUse  bool
}

// blockDevice is one node of `lsblk --json` output. Model and mountpoint
// are null for a device that has none.
type blockDevice struct {
	Name       string        `json:"name"`
	Size       string        `json:"size"`
	Model      *string       `json:"model"`
	Type       string        `json:"type"`
	Mountpoint *string       `json:"mountpoint"`
	Children   []blockDevice `json:"children"`
}

// mounted reports whether the device or anything stacked on it (a
// partition, an md array, an LVM volume) is mounted.
func (d blockDevice) mounted() bool {
	if d.Mountpoint != nil && *d.Mountpoint != "" {
		return true
	}
	return slices.ContainsFunc(d.Children, blockDevice.mounted)
}

// DiscoverDisks lists the whole disks lsblk reports. A disk is in use
// when it or anything stacked on it is mounted, which covers the disk
// holding the root filesystem. The JSON form is parsed because the
// column form splits a model name with spaces, or no model at all, into
// the wrong columns.
func (s *StorageService) DiscoverDisks(ctx context.Context) ([]AvailableDisk, error) {
	out, err := netutil.RunSimple(ctx, "lsblk", "--json", "-o", "NAME,SIZE,MODEL,TYPE,MOUNTPOINT")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var tree struct {
		BlockDevices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		return nil, fmt.Errorf("decode lsblk: %w", err)
	}

	var disks []AvailableDisk
	for _, d := range tree.BlockDevices {
		if d.Type != "disk" {
			continue
		}
		disk := AvailableDisk{Device: "/dev/" + d.Name, Size: d.Size, Type: d.Type, InUse: d.mounted()}
		if d.Model != nil {
			disk.Model = strings.TrimSpace(*d.Model)
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func (s *StorageService) CreateRAID(ctx context.Context, level int, devices []string, mountPoint string) error {
	if len(devices) < 2 && level != 0 {
		return fmt.Errorf("RAID-%d requires at least 2 devices", level)
	}

	mdDevice := s.cfg.Storage.RAID.Device
	if mdDevice == "" {
		mdDevice = "/dev/md0"
	}

	args := []string{
		"--create", mdDevice,
		"--level", fmt.Sprintf("%d", level),
		"--raid-devices", fmt.Sprintf("%d", len(devices)),
	}
	args = append(args, devices...)

	_, err := netutil.Run(ctx, "mdadm", args...)
	if err != nil {
		return fmt.Errorf("mdadm create: %w", err)
	}

	_, err = netutil.Run(ctx, "mkfs.ext4", "-F", mdDevice)
	if err != nil {
		return fmt.Errorf("mkfs: %w", err)
	}

	if _, err := netutil.Run(ctx, "mkdir", "-p", mountPoint); err != nil {
		return fmt.Errorf("mkdir mount point: %w", err)
	}
	_, err = netutil.Run(ctx, "mount", mdDevice, mountPoint)
	if err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	if err := appendFstabEntry(mdDevice, mountPoint, "ext4"); err != nil {
		return fmt.Errorf("persist mount: %w", err)
	}

	// Persist the new array's state to mdadm.conf (best-effort: the
	// scan runs even if previous state is incomplete).
	if _, err := netutil.Run(ctx, "mdadm", "--detail", "--scan", "--verbose"); err != nil {
		log.Printf("storage: mdadm scan: %v", err)
	}

	s.cfg.Storage.RAID.Device = mdDevice
	s.cfg.Storage.RAID.Level = level
	s.cfg.Storage.RAID.Members = devices

	return nil
}

func (s *StorageService) FormatAndMount(ctx context.Context, device, mountPoint string) error {
	_, err := netutil.Run(ctx, "mkfs.ext4", "-F", device)
	if err != nil {
		return fmt.Errorf("mkfs %s: %w", device, err)
	}

	if _, err := netutil.Run(ctx, "mkdir", "-p", mountPoint); err != nil {
		return fmt.Errorf("mkdir mount point: %w", err)
	}
	_, err = netutil.Run(ctx, "mount", device, mountPoint)
	if err != nil {
		return fmt.Errorf("mount %s: %w", device, err)
	}

	if err := appendFstabEntry(device, mountPoint, "ext4"); err != nil {
		return fmt.Errorf("persist mount: %w", err)
	}

	return nil
}

// fstab fields are whitespace-separated and one entry occupies one line,
// so a value carrying either ends the field or the record early. A
// newline in a device or mount point appends a second entry that the
// system honours at the next boot, which is a mount of the attacker's
// choosing performed by init.
//
// Both patterns are allowlists rather than escapes: fstab does have an
// escape form for spaces, but a value needing one is a value this
// product never produces, so refusing is both simpler and stricter.
var (
	fstabDevicePattern = regexp.MustCompile(`^/dev/[a-zA-Z0-9][a-zA-Z0-9/_.-]*$`)
	fstabMountPattern  = regexp.MustCompile(`^/[a-zA-Z0-9][a-zA-Z0-9/_.-]*$`)
	fstabTypePattern   = regexp.MustCompile(`^[a-z0-9]+$`)
)

// validateFstabEntry rejects anything that would not compose into
// exactly one well-formed record.
func validateFstabEntry(device, mountPoint, fsType string) error {
	if !fstabDevicePattern.MatchString(device) {
		return fmt.Errorf("refusing fstab entry: %q is not a device path", device)
	}
	if !fstabMountPattern.MatchString(mountPoint) {
		return fmt.Errorf("refusing fstab entry: %q is not a mount point", mountPoint)
	}
	if !fstabTypePattern.MatchString(fsType) {
		return fmt.Errorf("refusing fstab entry: %q is not a filesystem type", fsType)
	}
	// The character classes above already exclude "..", but a traversal
	// is worth naming separately: it would mount somewhere other than
	// where the caller believes.
	if slices.Contains(strings.Split(mountPoint, "/"), "..") {
		return fmt.Errorf("refusing fstab entry: mount point %q escapes upward", mountPoint)
	}
	return nil
}

// appendFstabEntry adds one record, or reports why it did not.
//
// The result used to be discarded: the write error was logged and the
// callers were told the mount succeeded, so a filesystem the operator
// had just created came back unmounted at the next boot with nothing to
// explain it.
//
// The read error was discarded too, which was worse. A failed read left
// `existing` empty, and the composition below would then have replaced
// the whole of /etc/fstab with this single line, dropping the root
// filesystem entry and everything else.
func appendFstabEntry(device, mountPoint, fsType string) error {
	if err := validateFstabEntry(device, mountPoint, fsType); err != nil {
		return err
	}

	existing, err := netutil.ReadFile("/etc/fstab")
	if err != nil {
		return fmt.Errorf("read fstab: %w", err)
	}

	entry := fmt.Sprintf("%s %s %s defaults 0 2", device, mountPoint, fsType)
	if strings.Contains(string(existing), entry) {
		return nil
	}

	newContent := strings.TrimRight(string(existing), "\n") + "\n" + entry + "\n"
	if err := netutil.WriteFile("/etc/fstab", []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("write fstab: %w", err)
	}
	return nil
}
