package services_test

import (
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
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

	svc.AddShare(config.ShareConfig{
		Name:     "media",
		Path:     "/mnt/raid/media",
		GuestOK:  true,
		ReadOnly: true,
	})

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
