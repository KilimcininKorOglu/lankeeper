package services_test

import (
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestNewDHCPService(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewDHCPService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestParseLeaseData(t *testing.T) {
	data := `1735689600 aa:bb:cc:dd:ee:ff 10.10.10.50 desktop
1735689600 11:22:33:44:55:66 10.10.10.51 laptop
0 aa:bb:cc:dd:ee:01 10.10.10.10 *
`
	leases := services.ParseLeaseData(data)
	if len(leases) != 3 {
		t.Fatalf("expected 3 leases, got %d", len(leases))
	}

	if leases[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("lease 0 MAC = %q", leases[0].MAC)
	}
	if leases[0].IP != "10.10.10.50" {
		t.Errorf("lease 0 IP = %q", leases[0].IP)
	}
	if leases[0].Hostname != "desktop" {
		t.Errorf("lease 0 hostname = %q", leases[0].Hostname)
	}

	if leases[2].Hostname != "" {
		t.Errorf("lease with * hostname should be empty, got %q", leases[2].Hostname)
	}
}

func TestStaticLeaseCRUD(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewDHCPService(cfg)

	if err := svc.AddStaticLease("aa:bb:cc:dd:ee:ff", "10.10.10.10", "desktop"); err != nil {
		t.Fatalf("add static lease: %v", err)
	}
	if len(cfg.DHCP.StaticLeases) != 1 {
		t.Fatalf("expected 1 static lease, got %d", len(cfg.DHCP.StaticLeases))
	}

	if err := svc.RemoveStaticLease(0); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if len(cfg.DHCP.StaticLeases) != 0 {
		t.Error("should be empty after remove")
	}
}

func TestRemoveStaticLeaseInvalid(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewDHCPService(cfg)

	if err := svc.RemoveStaticLease(0); err == nil {
		t.Error("should error on empty list")
	}
}
