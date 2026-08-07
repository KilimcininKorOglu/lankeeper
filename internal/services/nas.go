package services

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// Share fields are rendered into smb.conf, which has no escaping, so
// they are constrained to sets that cannot terminate a directive. These
// mirror the intake checks in the NAS handler.
var (
	nasShareNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	nasShareUserPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
)

type NASService struct {
	cfg *config.Config
	// tmplContent, when set, is used instead of reading the template
	// from disk. Only the FromFS constructor sets it.
	tmplContent string
	mu          sync.RWMutex
	cancel      context.CancelFunc
}

func NewNASService(cfg *config.Config) *NASService {
	return &NASService{cfg: cfg}
}

// NewNASServiceFromFS injects the smb.conf template as a string instead
// of reading it relative to the working directory. Tests run with the
// package directory as CWD, where ParseFiles against a project-root path
// cannot resolve.
func NewNASServiceFromFS(cfg *config.Config, tmplContent string) *NASService {
	return &NASService{cfg: cfg, tmplContent: tmplContent}
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
	root := template.New("smb.conf.tmpl").Funcs(template.FuncMap{
		"join": strings.Join,
	})

	var tmpl *template.Template
	var err error
	if s.tmplContent != "" {
		tmpl, err = root.Parse(s.tmplContent)
	} else {
		tmpl, err = root.ParseFiles("configs/sysconf/smb.conf.tmpl")
	}
	if err != nil {
		return "", fmt.Errorf("parse smb template: %w", err)
	}

	s.mu.RLock()
	shares := s.cfg.NAS.Shares
	s.mu.RUnlock()

	var buf strings.Builder
	if err := tmpl.Execute(&buf, map[string]any{"Shares": safeShares(shares)}); err != nil {
		return "", fmt.Errorf("execute smb template: %w", err)
	}
	return buf.String(), nil
}

// safeShares drops any share whose fields cannot be written into
// smb.conf verbatim.
//
// The template is text/template with no escaping and the directives are
// unquoted, so a control character in a value ends the directive and
// begins another inside the same stanza. smbd runs as root and Samba
// implements directives that execute commands, so this has to hold on
// the render path itself, not only at the HTTP handler: a share can
// arrive from hand-edited YAML, a restored backup, or a config written
// by a release that predates handler validation.
func safeShares(shares []config.ShareConfig) []config.ShareConfig {
	out := make([]config.ShareConfig, 0, len(shares))
	for _, sh := range shares {
		if err := validateShare(sh); err != nil {
			log.Printf("nas: skipping share %q: %v", sh.Name, err)
			continue
		}
		out = append(out, sh)
	}
	return out
}

func validateShare(sh config.ShareConfig) error {
	if !nasShareNamePattern.MatchString(sh.Name) {
		return fmt.Errorf("invalid share name")
	}
	if err := netutil.ValidateFilesystemPath(sh.Path); err != nil {
		return err
	}
	for _, u := range sh.ValidUsers {
		if !nasShareUserPattern.MatchString(u) {
			return fmt.Errorf("invalid user %q", u)
		}
	}
	return nil
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
		if err := os.MkdirAll(source.DownloadPath, 0o755); err != nil {
			log.Printf("m3u sync: mkdir %s: %v", source.DownloadPath, err)
			continue
		}

		for _, item := range filtered {
			groupDir, err := containedJoin(source.DownloadPath, sanitizePath(item.Group))
			if err != nil {
				log.Printf("m3u sync: rejected group from %s: %v", source.URL, err)
				totalErrors++
				continue
			}
			if err := os.MkdirAll(groupDir, 0o755); err != nil {
				log.Printf("m3u sync: mkdir %s: %v", groupDir, err)
				totalErrors++
				continue
			}

			strmPath, err := containedJoin(groupDir, sanitizePath(item.Title)+".strm")
			if err != nil {
				log.Printf("m3u sync: rejected title from %s: %v", source.URL, err)
				totalErrors++
				continue
			}
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
					if err := s.SyncM3U(ctx); err != nil {
						log.Printf("m3u scheduled sync: %v", err)
					}
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
	if err := validateOutboundURL(url); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := outboundFetchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var items []M3UItem
	var currentGroup, currentTitle string

	body := newLimitedBody(resp.Body)
	scanner := bufio.NewScanner(body)
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

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if body.overflowed() {
		return nil, errFetchTooLarge
	}
	return items, nil
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

// sanitizePath turns an arbitrary string into a single safe path
// component.
//
// The separator replacement alone is not enough: it leaves `.` and `..`
// untouched, and `filepath.Join(base, "..")` resolves to the parent of
// base. Playlist bodies are fetched live from a remote server, so a
// hostile provider controls these strings. A component that is nothing
// but dots is therefore replaced outright rather than passed along.
func sanitizePath(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	out := strings.TrimSpace(replacer.Replace(s))
	if out == "" || strings.Trim(out, ".") == "" {
		return "_"
	}
	return out
}

// containedJoin appends one untrusted component to base and confirms the
// result stayed underneath it.
//
// This is the load-bearing check rather than sanitizePath: a
// character-replacement helper cannot be relied on to have anticipated
// every escape, while comparing the cleaned result against the cleaned
// base is decisive whatever the input was.
func containedJoin(base, component string) (string, error) {
	root := filepath.Clean(base)
	joined := filepath.Join(root, component)
	if joined != root && !strings.HasPrefix(joined, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %s", component, root)
	}
	return joined, nil
}
