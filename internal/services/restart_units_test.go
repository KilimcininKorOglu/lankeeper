package services_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestChronyAndRsyslogAreRestartedNotReloaded is the regression test. On
// Debian 12 neither unit has an ExecReload, so systemd refused every
// reload job and NTP and syslog changes waited for the next reboot.
func TestChronyAndRsyslogAreRestartedNotReloaded(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	if err := services.NewNTPService(cfg).Reload(context.Background()); err != nil {
		t.Fatalf("ntp reload: %v", err)
	}
	if err := services.NewSyslogService(cfg).Reload(context.Background()); err != nil {
		t.Fatalf("syslog reload: %v", err)
	}
	if agent.countExec("systemctl", "restart", "chrony") != 1 {
		t.Error("chrony was not restarted")
	}
	if agent.countExec("systemctl", "restart", "rsyslog") != 1 {
		t.Error("rsyslog was not restarted")
	}
}
