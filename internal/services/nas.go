package services

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type NASService struct {
	cfg    *config.Config
	mu     sync.RWMutex
	cancel context.CancelFunc
}

func NewNASService(cfg *config.Config) *NASService {
	return &NASService{cfg: cfg}
}

type M3USyncStatus struct {
	Running    bool
	LastSync   time.Time
	TotalItems int
	Errors     int
}

var m3uStatus M3USyncStatus

func (s *NASService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *NASService) AddShare(share config.ShareConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.NAS.Shares = append(s.cfg.NAS.Shares, share)
	return s.persist()
}

func (s *NASService) RemoveShare(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, sh := range s.cfg.NAS.Shares {
		if sh.Name == name {
			s.cfg.NAS.Shares = append(s.cfg.NAS.Shares[:i], s.cfg.NAS.Shares[i+1:]...)
			return s.persist()
		}
	}
	return fmt.Errorf("share %q not found", name)
}

func (s *NASService) GetShares() []config.ShareConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]config.ShareConfig, len(s.cfg.NAS.Shares))
	copy(result, s.cfg.NAS.Shares)
	return result
}

func (s *NASService) RenderConfig() (string, error) {
	// ParseFiles names the template after the file basename. Use that
	// same name on New() so the FuncMap binds to the parsed template
	// instead of an empty placeholder. Previously `New("smb")` produced
	// an empty root template and Execute hit "incomplete or empty
	// template".
	tmpl, err := template.New("smb.conf.tmpl").Funcs(template.FuncMap{
		"join": strings.Join,
	}).ParseFiles("configs/sysconf/smb.conf.tmpl")
	if err != nil {
		return "", fmt.Errorf("parse smb template: %w", err)
	}

	s.mu.RLock()
	shares := s.cfg.NAS.Shares
	s.mu.RUnlock()

	var buf strings.Builder
	if err := tmpl.Execute(&buf, map[string]any{"Shares": shares}); err != nil {
		return "", fmt.Errorf("execute smb template: %w", err)
	}
	return buf.String(), nil
}

// RenderToDisk renders /etc/samba/smb.conf without reloading. Suitable for
// install-time invocation.
func (s *NASService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	if err := netutil.WriteFile("/etc/samba/smb.conf", []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write smb.conf: %w", err)
	}
	return nil
}

func (s *NASService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	if _, err := netutil.Run(ctx, "smbcontrol", "all", "reload-config"); err != nil {
		return fmt.Errorf("reload samba: %w", err)
	}
	log.Println("samba config reloaded")
	return nil
}

func (s *NASService) GetM3UStatus() M3USyncStatus {
	return m3uStatus
}

func (s *NASService) SyncM3U(ctx context.Context) error {
	m3uStatus.Running = true
	defer func() {
		m3uStatus.Running = false
		m3uStatus.LastSync = time.Now()
	}()

	var totalItems, totalErrors int

	for _, source := range s.cfg.NAS.M3USources {
		if !strings.HasPrefix(source.DownloadPath, "/srv/") && !strings.HasPrefix(source.DownloadPath, "/mnt/") {
			log.Printf("m3u download path rejected (must be under /srv/ or /mnt/): %s", source.DownloadPath)
			totalErrors++
			continue
		}

		items, err := downloadAndParseM3U(ctx, source.URL)
		if err != nil {
			log.Printf("m3u download %s: %v", source.URL, err)
			totalErrors++
			continue
		}

		filtered := filterM3UItems(items, source.IncludeGroups, source.ExcludeGroups)
		os.MkdirAll(source.DownloadPath, 0o755)

		for _, item := range filtered {
			groupDir := filepath.Join(source.DownloadPath, sanitizePath(item.Group))
			os.MkdirAll(groupDir, 0o755)

			strmPath := filepath.Join(groupDir, sanitizePath(item.Title)+".strm")
			if err := os.WriteFile(strmPath, []byte(item.URL+"\n"), 0o644); err != nil {
				totalErrors++
				continue
			}
			totalItems++
		}
	}

	m3uStatus.TotalItems = totalItems
	m3uStatus.Errors = totalErrors

	log.Printf("m3u sync complete: %d items, %d errors", totalItems, totalErrors)
	return nil
}

func (s *NASService) StartScheduledSync(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	for _, source := range s.cfg.NAS.M3USources {
		if source.Schedule == "" {
			continue
		}

		go func(src config.M3USourceConfig) {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.SyncM3U(ctx)
				}
			}
		}(source)
	}
}

func (s *NASService) StopScheduledSync() {
	if s.cancel != nil {
		s.cancel()
	}
}

type M3UItem struct {
	Group string
	Title string
	URL   string
}

func downloadAndParseM3U(ctx context.Context, url string) ([]M3UItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var items []M3UItem
	var currentGroup, currentTitle string

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "#EXTINF:") {
			info := line[len("#EXTINF:"):]

			if idx := strings.Index(info, "group-title=\""); idx != -1 {
				rest := info[idx+len("group-title=\""):]
				if end := strings.Index(rest, "\""); end != -1 {
					currentGroup = rest[:end]
				}
			}

			if idx := strings.LastIndex(info, ","); idx != -1 {
				currentTitle = strings.TrimSpace(info[idx+1:])
			}
			continue
		}

		if line != "" && !strings.HasPrefix(line, "#") {
			if currentTitle == "" {
				currentTitle = "Unknown"
			}
			if currentGroup == "" {
				currentGroup = "Ungrouped"
			}

			items = append(items, M3UItem{
				Group: currentGroup,
				Title: currentTitle,
				URL:   line,
			})

			currentGroup = ""
			currentTitle = ""
		}
	}

	return items, scanner.Err()
}

func ParseM3UData(data string) []M3UItem {
	var items []M3UItem
	var currentGroup, currentTitle string

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "#EXTINF:") {
			info := line[len("#EXTINF:"):]

			if idx := strings.Index(info, "group-title=\""); idx != -1 {
				rest := info[idx+len("group-title=\""):]
				if end := strings.Index(rest, "\""); end != -1 {
					currentGroup = rest[:end]
				}
			}

			if idx := strings.LastIndex(info, ","); idx != -1 {
				currentTitle = strings.TrimSpace(info[idx+1:])
			}
			continue
		}

		if line != "" && !strings.HasPrefix(line, "#") {
			if currentTitle == "" {
				currentTitle = "Unknown"
			}
			if currentGroup == "" {
				currentGroup = "Ungrouped"
			}

			items = append(items, M3UItem{
				Group: currentGroup,
				Title: currentTitle,
				URL:   line,
			})

			currentGroup = ""
			currentTitle = ""
		}
	}

	return items
}

func filterM3UItems(items []M3UItem, includeGroups, excludeGroups []string) []M3UItem {
	if len(includeGroups) == 0 && len(excludeGroups) == 0 {
		return items
	}

	includeSet := make(map[string]bool, len(includeGroups))
	for _, g := range includeGroups {
		includeSet[strings.ToLower(g)] = true
	}

	excludeSet := make(map[string]bool, len(excludeGroups))
	for _, g := range excludeGroups {
		excludeSet[strings.ToLower(g)] = true
	}

	var filtered []M3UItem
	for _, item := range items {
		groupLower := strings.ToLower(item.Group)

		if len(excludeSet) > 0 && excludeSet[groupLower] {
			continue
		}

		if len(includeSet) > 0 && !includeSet[groupLower] {
			continue
		}

		filtered = append(filtered, item)
	}

	return filtered
}

func (s *NASService) DiscoverM3UGroups(ctx context.Context, sourceURL string) ([]string, error) {
	items, err := downloadAndParseM3U(ctx, sourceURL)
	if err != nil {
		return nil, err
	}

	groupSet := make(map[string]bool)
	for _, item := range items {
		groupSet[item.Group] = true
	}

	var groups []string
	for g := range groupSet {
		groups = append(groups, g)
	}

	sort.Strings(groups)
	return groups, nil
}

func sanitizePath(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return replacer.Replace(s)
}
