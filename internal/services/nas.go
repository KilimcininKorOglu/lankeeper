package services

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// m3uMu guards m3uStatus, which the sync goroutine writes and page
	// requests read.
	m3uMu     sync.Mutex
	m3uStatus M3USyncStatus
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
	data := map[string]any{
		"Shares":         safeShares(shares),
		"Addresses":      routerLANAddresses(s.cfg),
		"AllowedSubnets": servedClientSubnets(s.cfg),
	}
	if err := tmpl.Execute(&buf, data); err != nil {
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

// routerLANAddresses returns the router's own address on each LAN
// interface and VLAN, in address/prefix form. smbd binds to these alone,
// so it never listens on the WAN; VPN clients reach it through the LAN
// address, which routes over their tunnel.
func routerLANAddresses(cfg *config.Config) []string {
	var cidrs []string
	for _, iface := range cfg.Interfaces {
		if iface.Role == "lan" {
			cidrs = append(cidrs, iface.Address)
		}
	}
	for _, vlan := range cfg.VLANs {
		cidrs = append(cidrs, vlan.Address)
	}
	var out []string
	for _, cidr := range cidrs {
		ip, subnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			continue
		}
		ones, _ := subnet.Mask.Size()
		out = append(out, fmt.Sprintf("%s/%d", ip, ones))
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

// sambaUnits are the Samba daemons the NAS runs.
var sambaUnits = []string{"smbd", "nmbd"}

// ApplyConfig renders smb.conf and runs Samba only while a share exists.
// With no share, smbd and nmbd are stopped and disabled: a file server
// with nothing to serve is only something listening.
func (s *NASService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	s.mu.RLock()
	shares := safeShares(s.cfg.NAS.Shares)
	s.mu.RUnlock()

	verb := "enable"
	if len(shares) == 0 {
		verb = "disable"
	}
	for _, unit := range sambaUnits {
		if _, err := netutil.Run(ctx, "systemctl", verb, "--now", unit); err != nil {
			return fmt.Errorf("%s %s: %w", verb, unit, err)
		}
	}
	if len(shares) == 0 {
		log.Println("samba stopped: no shares configured")
		return nil
	}
	if _, err := netutil.Run(ctx, "smbcontrol", "all", "reload-config"); err != nil {
		return fmt.Errorf("reload samba: %w", err)
	}
	log.Println("samba config reloaded")
	return nil
}

func (s *NASService) GetM3UStatus() M3USyncStatus {
	s.m3uMu.Lock()
	defer s.m3uMu.Unlock()
	return s.m3uStatus
}

// ErrM3USyncRunning reports that a sync is already in progress. Two
// syncs would write the same .strm files and interleave their counts.
var ErrM3USyncRunning = errors.New("an M3U sync is already running")

// M3USyncRunning reports whether a sync is in progress.
func (s *NASService) M3USyncRunning() bool {
	s.m3uMu.Lock()
	defer s.m3uMu.Unlock()
	return s.m3uStatus.Running
}

func (s *NASService) SyncM3U(ctx context.Context) error {
	s.mu.RLock()
	sources := slices.Clone(s.cfg.NAS.M3USources)
	s.mu.RUnlock()
	return s.syncSources(ctx, sources)
}

// syncSources syncs the given sources as one run, refusing to start
// while another run is in progress.
func (s *NASService) syncSources(ctx context.Context, sources []config.M3USourceConfig) error {
	s.m3uMu.Lock()
	if s.m3uStatus.Running {
		s.m3uMu.Unlock()
		return ErrM3USyncRunning
	}
	s.m3uStatus.Running = true
	s.m3uMu.Unlock()

	var totalItems, totalErrors int
	for _, source := range sources {
		items, errs := syncM3USource(ctx, source)
		totalItems += items
		totalErrors += errs
	}

	s.m3uMu.Lock()
	s.m3uStatus = M3USyncStatus{LastSync: time.Now(), TotalItems: totalItems, Errors: totalErrors}
	s.m3uMu.Unlock()

	log.Printf("m3u sync complete: %d items, %d errors", totalItems, totalErrors)
	return nil
}

// syncM3USource writes the .strm files for one source and returns the
// items written and the errors met.
func syncM3USource(ctx context.Context, source config.M3USourceConfig) (items, errs int) {
	// downloadPath, not source.DownloadPath, is used from here on:
	// the cleaned form is the one that was checked.
	downloadPath, err := ValidateMediaPath(source.DownloadPath)
	if err != nil {
		log.Printf("m3u download path rejected: %v", err)
		return 0, 1
	}

	list, err := downloadAndParseM3U(ctx, source.URL)
	if err != nil {
		log.Printf("m3u download %s: %v", source.URL, err)
		return 0, 1
	}

	filtered := filterM3UItems(list, source.IncludeGroups, source.ExcludeGroups)
	// Media directories are served by smbd to LAN clients, so
	// they have to be world-readable. Confined to /srv or /mnt
	// by ValidateMediaPath.
	// #nosec G301
	if err := os.MkdirAll(downloadPath, 0o755); err != nil {
		log.Printf("m3u sync: mkdir %s: %v", downloadPath, err)
		return 0, 0
	}

	for _, item := range filtered {
		if err := writeStrm(downloadPath, source.URL, item); err != nil {
			log.Printf("m3u sync: %v", err)
			errs++
			continue
		}
		items++
	}
	return items, errs
}

// writeStrm writes one playlist entry under its group directory.
func writeStrm(downloadPath, sourceURL string, item M3UItem) error {
	groupDir, err := containedJoin(downloadPath, sanitizePath(item.Group))
	if err != nil {
		return fmt.Errorf("rejected group from %s: %w", sourceURL, err)
	}
	// Same: served by smbd, confined by ValidateMediaPath.
	// #nosec G301
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", groupDir, err)
	}

	strmPath, err := containedJoin(groupDir, sanitizePath(item.Title)+".strm")
	if err != nil {
		return fmt.Errorf("rejected title from %s: %w", sourceURL, err)
	}
	// A .strm file is a playlist entry Kodi and smbd clients read.
	// It holds a stream URL, not a credential.
	// #nosec G306
	return os.WriteFile(strmPath, []byte(item.URL+"\n"), 0o644)
}

// StartScheduledSync syncs each M3U source on its own cron expression,
// in the router's local time, until ctx ends. The sources are re-read on
// every tick, so an edit takes effect without a restart.
func (s *NASService) StartScheduledSync(ctx context.Context, wg *sync.WaitGroup) {
	wg.Go(func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		next := map[string]time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.m3uScheduleTick(ctx, now, next)
			}
		}
	})
}

// m3uScheduleTick syncs the scheduled sources that are due. next holds
// each source's fire time, keyed so an edited source starts over.
func (s *NASService) m3uScheduleTick(ctx context.Context, now time.Time, next map[string]time.Time) {
	s.mu.RLock()
	sources := slices.Clone(s.cfg.NAS.M3USources)
	s.mu.RUnlock()

	seen := make(map[string]bool, len(sources))
	for _, src := range sources {
		if src.Schedule == "" {
			continue
		}
		key := src.URL + "|" + src.DownloadPath + "|" + src.Schedule
		seen[key] = true
		s.m3uSourceTick(ctx, now, src, key, next)
	}
	for key := range next {
		if !seen[key] {
			delete(next, key)
		}
	}
}

// m3uSourceTick syncs one source when it is due and schedules its next run.
func (s *NASService) m3uSourceTick(ctx context.Context, now time.Time, src config.M3USourceConfig, key string, next map[string]time.Time) {
	at, known := next[key]
	sched, err := ParseSchedule(src.Schedule, time.Local)
	if err != nil {
		if !known {
			log.Printf("m3u: schedule %q for %s: %v", src.Schedule, src.URL, err)
			next[key] = time.Time{}
		}
		return
	}
	if !known || at.IsZero() {
		next[key] = sched.Next(now)
		return
	}
	if now.Before(at) {
		return
	}
	if err := s.syncSources(ctx, []config.M3USourceConfig{src}); err != nil {
		log.Printf("m3u scheduled sync %s: %v", src.URL, err)
	}
	next[key] = sched.Next(now)
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

	var p m3uParser
	body := newLimitedBody(resp.Body)
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		p.feed(scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if body.overflowed() {
		return nil, errFetchTooLarge
	}
	return p.items, nil
}

func ParseM3UData(data string) []M3UItem {
	var p m3uParser
	for line := range strings.SplitSeq(data, "\n") {
		p.feed(line)
	}
	return p.items
}

// m3uParser collects playlist entries one line at a time. An #EXTINF
// line sets the group and title for the next URL line.
type m3uParser struct {
	items        []M3UItem
	group, title string
}

// feed consumes one playlist line.
func (p *m3uParser) feed(line string) {
	line = strings.TrimSpace(line)
	if info, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
		p.readInfo(info)
		return
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	p.items = append(p.items, M3UItem{
		Group: cmp.Or(p.group, "Ungrouped"),
		Title: cmp.Or(p.title, "Unknown"),
		URL:   line,
	})
	p.group, p.title = "", ""
}

// readInfo takes the group-title attribute and the display title from
// an #EXTINF line.
func (p *m3uParser) readInfo(info string) {
	if _, after, ok := strings.Cut(info, "group-title=\""); ok {
		if before, _, ok := strings.Cut(after, "\""); ok {
			p.group = before
		}
	}
	if idx := strings.LastIndex(info, ","); idx != -1 {
		p.title = strings.TrimSpace(info[idx+1:])
	}
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

// Sentinels so a caller can tell the two rejection reasons apart and
// report each one distinctly.
var (
	ErrMediaPathCharacters = errors.New("path contains characters that are not allowed")
	ErrMediaPathPrefix     = errors.New("path must live under /srv/ or /mnt/")
)

// ValidateMediaPath checks a filesystem path that is meant to live
// inside the media roots and returns the cleaned form to use from then
// on.
//
// The order matters and is the reason this is one function rather than
// a rule each caller reimplements. The character set is checked on the
// RAW value, because filepath.Clean collapses dot segments but preserves
// control characters. The prefix test then runs on the CLEANED value,
// because a raw string like "/srv/../../etc/cron.d" passes a literal
// prefix test while resolving elsewhere, and the kernel resolves those
// segments at syscall time. Testing the raw string was exactly the step
// the M3U sync path was missing.
//
// Callers must use the returned path, not the one they passed in.
func ValidateMediaPath(raw string) (string, error) {
	if err := netutil.ValidateFilesystemPath(raw); err != nil {
		return "", fmt.Errorf("%w: %v", ErrMediaPathCharacters, err)
	}
	clean := filepath.Clean(raw)
	if !strings.HasPrefix(clean, "/srv/") && !strings.HasPrefix(clean, "/mnt/") {
		return "", fmt.Errorf("%w: %s", ErrMediaPathPrefix, clean)
	}
	return clean, nil
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
