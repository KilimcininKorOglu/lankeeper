package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestNewNASService(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewNASService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestShareCRUD(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewNASService(cfg)

	if err := svc.AddShare(config.ShareConfig{
		Name:     "media",
		Path:     "/mnt/raid/media",
		GuestOK:  true,
		ReadOnly: true,
	}); err != nil {
		t.Fatalf("add share: %v", err)
	}

	shares := svc.GetShares()
	if len(shares) != 1 {
		t.Fatalf("expected 1 share, got %d", len(shares))
	}
	if shares[0].Name != "media" {
		t.Errorf("name = %q, want media", shares[0].Name)
	}

	if err := svc.RemoveShare("media"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if len(svc.GetShares()) != 0 {
		t.Error("should be empty after removal")
	}
}

func TestRemoveShareNotFound(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewNASService(cfg)

	if err := svc.RemoveShare("nonexistent"); err == nil {
		t.Error("should error for nonexistent share")
	}
}

func TestParseM3UData(t *testing.T) {
	data := `#EXTM3U
#EXTINF:-1 group-title="Movies",The Matrix
http://example.com/matrix.mp4
#EXTINF:-1 group-title="Movies",Inception
http://example.com/inception.mp4
#EXTINF:-1 group-title="Series",Breaking Bad S01E01
http://example.com/bb-s01e01.mp4
`
	items := services.ParseM3UData(data)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	if items[0].Group != "Movies" {
		t.Errorf("item 0 group = %q, want Movies", items[0].Group)
	}
	if items[0].Title != "The Matrix" {
		t.Errorf("item 0 title = %q, want The Matrix", items[0].Title)
	}
	if items[0].URL != "http://example.com/matrix.mp4" {
		t.Errorf("item 0 url = %q", items[0].URL)
	}
	if items[2].Group != "Series" {
		t.Errorf("item 2 group = %q, want Series", items[2].Group)
	}
}

func TestParseM3UDataEmpty(t *testing.T) {
	items := services.ParseM3UData("")
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestM3UFilterIncludeGroups(t *testing.T) {
	data := `#EXTM3U
#EXTINF:-1 group-title="Sports",Football Match
http://example.com/football.mp4
#EXTINF:-1 group-title="Movies",The Matrix
http://example.com/matrix.mp4
#EXTINF:-1 group-title="Sports",Basketball Game
http://example.com/basketball.mp4
#EXTINF:-1 group-title="News",World News
http://example.com/news.mp4
`
	items := services.ParseM3UData(data)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.NAS.M3USources = []config.M3USourceConfig{
		{
			URL:           "http://example.com/test.m3u",
			DownloadPath:  t.TempDir(),
			IncludeGroups: []string{"Sports"},
		},
	}

	_ = cfg
	_ = items
}

func TestParseM3UGroupCount(t *testing.T) {
	data := `#EXTM3U
#EXTINF:-1 group-title="A",Item1
http://a/1
#EXTINF:-1 group-title="B",Item2
http://b/2
#EXTINF:-1 group-title="A",Item3
http://a/3
`
	items := services.ParseM3UData(data)

	groups := make(map[string]int)
	for _, item := range items {
		groups[item.Group]++
	}

	if groups["A"] != 2 {
		t.Errorf("group A should have 2 items, got %d", groups["A"])
	}
	if groups["B"] != 1 {
		t.Errorf("group B should have 1 item, got %d", groups["B"])
	}
}

func TestM3UStatus(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewNASService(cfg)
	status := svc.GetM3UStatus()
	if status.Running {
		t.Error("should not be running by default")
	}
}

// testSMBTemplate mirrors the share stanza of the real smb.conf
// template: text/template with unquoted directive values.
const testSMBTemplate = `[global]
    workgroup = WORKGROUP
{{ range .Shares }}
[{{ .Name }}]
    path = {{ .Path }}
    browseable = yes
{{- if .ValidUsers }}
    valid users = {{ join .ValidUsers ", " }}
{{- end }}
{{ end }}
`

// TestNASRenderRejectsDirectiveInjection is the regression test for a
// share path carrying a newline. smbd runs as root and Samba executes
// root preexec on client connect, so a path that terminates its own
// directive turns an unauthenticated SMB connection into root command
// execution.
func TestNASRenderRejectsDirectiveInjection(t *testing.T) {
	cases := []struct {
		name  string
		share config.ShareConfig
	}{
		{
			name: "newline in path",
			share: config.ShareConfig{
				Name: "media",
				Path: "/srv/media\n    root preexec = /bin/sh -c id",
			},
		},
		{
			name: "carriage return in path",
			share: config.ShareConfig{
				Name: "media",
				Path: "/srv/media\r    root preexec = /bin/sh -c id",
			},
		},
		{
			name: "newline in share name",
			share: config.ShareConfig{
				Name: "media]\n    root preexec = /bin/sh -c id\n[x",
				Path: "/srv/media",
			},
		},
		{
			name: "newline in valid users",
			share: config.ShareConfig{
				Name:       "media",
				Path:       "/srv/media",
				ValidUsers: []string{"bob\n    root preexec = /bin/sh -c id"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.NAS.Shares = []config.ShareConfig{tc.share}

			svc := services.NewNASServiceFromFS(cfg, testSMBTemplate)
			out, err := svc.RenderConfig()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if strings.Contains(out, "root preexec") {
				t.Errorf("injected directive reached smb.conf\n---\n%s", out)
			}
		})
	}
}

// TestNASRenderKeepsLegitimateShare guards against the validator being
// so strict it drops ordinary shares, including paths with a space.
func TestNASRenderKeepsLegitimateShare(t *testing.T) {
	cfg := &config.Config{}
	cfg.NAS.Shares = []config.ShareConfig{
		{Name: "media", Path: "/srv/media", ValidUsers: []string{"bob", "alice.j"}},
		{Name: "my-backups_2", Path: "/mnt/disk1/My Backups"},
	}

	svc := services.NewNASServiceFromFS(cfg, testSMBTemplate)
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, want := range []string{
		"[media]",
		"path = /srv/media",
		"valid users = bob, alice.j",
		"[my-backups_2]",
		"path = /mnt/disk1/My Backups",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("legitimate share content missing %q\n---\n%s", want, out)
		}
	}
}

// TestNASRenderDropsOnlyTheBadShare confirms one poisoned entry does not
// take the rest of the configuration with it.
func TestNASRenderDropsOnlyTheBadShare(t *testing.T) {
	cfg := &config.Config{}
	cfg.NAS.Shares = []config.ShareConfig{
		{Name: "good", Path: "/srv/good"},
		{Name: "bad", Path: "/srv/bad\n    root preexec = /bin/sh -c id"},
	}

	svc := services.NewNASServiceFromFS(cfg, testSMBTemplate)
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "[good]") {
		t.Error("valid share was dropped alongside the invalid one")
	}
	if strings.Contains(out, "[bad]") || strings.Contains(out, "root preexec") {
		t.Errorf("invalid share rendered\n---\n%s", out)
	}
}
