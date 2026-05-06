package services

import (
	"context"
	"fmt"
	"log"
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
	Device     string
	Level      string
	State      string
	ActiveDisks int
	TotalDisks  int
	Members    []DiskMember
}

type DiskMember struct {
	Device string
	State  string
}

type SMARTInfo struct {
	Device      string
	Model       string
	Temperature int
	PowerOnHours int
	HealthOK    bool
	Errors      int
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

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Raid Level :") {
			status.Level = strings.TrimSpace(strings.TrimPrefix(line, "Raid Level :"))
		}
		if strings.HasPrefix(line, "State :") {
			status.State = strings.TrimSpace(strings.TrimPrefix(line, "State :"))
		}
		if strings.HasPrefix(line, "Active Devices :") {
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "Active Devices :"), "%d", &status.ActiveDisks)
		}
		if strings.HasPrefix(line, "Total Devices :") {
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "Total Devices :"), "%d", &status.TotalDisks)
		}
		if strings.Contains(line, "/dev/sd") || strings.Contains(line, "/dev/nvme") {
			fields := strings.Fields(line)
			if len(fields) >= 7 {
				status.Members = append(status.Members, DiskMember{
					Device: fields[len(fields)-1],
					State:  fields[4],
				})
			}
		}
	}

	return status, nil
}

func (s *StorageService) GetSMARTInfo(ctx context.Context, device string) (*SMARTInfo, error) {
	out, err := netutil.RunSimple(ctx, "smartctl", "-a", device)
	if err != nil && !strings.Contains(err.Error(), "exit status") {
		return nil, fmt.Errorf("smartctl: %w", err)
	}

	info := &SMARTInfo{Device: device, HealthOK: true}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Device Model:") || strings.HasPrefix(line, "Model Number:") {
			info.Model = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
		if strings.Contains(line, "Temperature_Celsius") || strings.Contains(line, "Temperature Sensor") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "Celsius" && i > 0 {
					_, _ = fmt.Sscanf(fields[i-1], "%d", &info.Temperature)
				}
			}
			if info.Temperature == 0 && len(fields) >= 10 {
				_, _ = fmt.Sscanf(fields[9], "%d", &info.Temperature)
			}
		}
		if strings.Contains(line, "Power_On_Hours") {
			fields := strings.Fields(line)
			if len(fields) >= 10 {
				_, _ = fmt.Sscanf(fields[9], "%d", &info.PowerOnHours)
			}
		}
		if strings.Contains(line, "SMART overall-health") && strings.Contains(line, "FAILED") {
			info.HealthOK = false
		}
		if strings.Contains(line, "Reallocated_Sector") {
			fields := strings.Fields(line)
			if len(fields) >= 10 {
				_, _ = fmt.Sscanf(fields[9], "%d", &info.Errors)
			}
		}
	}

	return info, nil
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

func (s *StorageService) DiscoverDisks(ctx context.Context) ([]AvailableDisk, error) {
	out, err := netutil.RunSimple(ctx, "lsblk", "-d", "-n", "-o", "NAME,SIZE,MODEL,TYPE,MOUNTPOINT", "--json")
	if err != nil {
		return s.discoverDisksFallback(ctx)
	}

	_ = out
	return s.discoverDisksFallback(ctx)
}

func (s *StorageService) discoverDisksFallback(ctx context.Context) ([]AvailableDisk, error) {
	out, err := netutil.RunSimple(ctx, "lsblk", "-d", "-n", "-o", "NAME,SIZE,MODEL,TYPE,MOUNTPOINT")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}

	var disks []AvailableDisk
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "disk" {
			continue
		}

		disk := AvailableDisk{
			Device: "/dev/" + fields[0],
			Size:   fields[1],
			Type:   fields[3],
		}
		if len(fields) >= 3 {
			disk.Model = fields[2]
		}
		if len(fields) >= 5 && fields[4] != "" {
			disk.InUse = true
		}

		rootDisk, _ := netutil.RunSimple(ctx, "findmnt", "-n", "-o", "SOURCE", "/")
		if strings.Contains(strings.TrimSpace(rootDisk), fields[0]) {
			disk.InUse = true
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

	appendFstabEntry(mdDevice, mountPoint, "ext4")

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

	appendFstabEntry(device, mountPoint, "ext4")

	return nil
}

func appendFstabEntry(device, mountPoint, fsType string) {
	entry := fmt.Sprintf("%s %s %s defaults 0 2", device, mountPoint, fsType)
	existing, _ := netutil.ReadFile("/etc/fstab")
	if strings.Contains(string(existing), entry) {
		return
	}
	newContent := strings.TrimRight(string(existing), "\n") + "\n" + entry + "\n"
	if err := netutil.WriteFile("/etc/fstab", []byte(newContent), 0o644); err != nil {
		log.Printf("storage: write fstab: %v", err)
	}
}
