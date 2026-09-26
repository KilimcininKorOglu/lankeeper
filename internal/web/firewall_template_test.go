package web

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

// TestServerRefusesToStartWithoutTheFirewallTemplate is the regression
// test. The server read the nftables template from a path outside the web
// embed, discarded the error and fell back to "flush ruleset", so every
// firewall Apply would have wiped the ruleset. A missing template must
// stop the start instead.
func TestServerRefusesToStartWithoutTheFirewallTemplate(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.System.SessionSecret = "test-secret"
	t.Setenv("LANKEEPER_FIREWALL_STATE", filepath.Join(t.TempDir(), "firewall-pending.json"))

	loc, err := i18n.New("en")
	if err != nil {
		t.Fatalf("init i18n: %v", err)
	}
	if err := loc.LoadFromFS(webfs.EmbeddedFS, "locales"); err != nil {
		t.Fatalf("load locales: %v", err)
	}

	srv, err := NewServer(cfg, loc, webfs.EmbeddedFS, services.NewUpdateService("v0.0.0-test", "", "", nil))
	if err == nil {
		srv.stopBackgroundSweepers()
		t.Fatal("NewServer started without configs/sysconf/nftables.conf.tmpl")
	}
	if !strings.Contains(err.Error(), "nftables") {
		t.Errorf("error does not name the nftables template: %v", err)
	}
}
