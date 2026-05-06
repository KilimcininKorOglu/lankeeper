package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type UpdateService struct {
	currentVersion  string
	currentCommit   string
	currentDate     string
	architecture    string
	binaryPath      string
	statePath       string
	repoOwner       string
	repoName        string
	backup          *BackupService
	mu              sync.Mutex
	watchdogCancel  context.CancelFunc
	pendingVersion  string
	previousVersion string
	backupBinary    string
}

const (
	defaultUpdateStatePath = "/var/lib/lankeeper/update-state.json"
	updateConfirmWindow    = 60 * time.Second
)

func NewUpdateService(version, commit, date string, backup *BackupService) *UpdateService {
	statePath := os.Getenv("LANKEEPER_UPDATE_STATE")
	if statePath == "" {
		statePath = defaultUpdateStatePath
	}

	svc := &UpdateService{
		currentVersion: version,
		currentCommit:  commit,
		currentDate:    date,
		architecture:   runtime.GOARCH,
		binaryPath:     "/usr/local/bin/lankeeper",
		statePath:      statePath,
		repoOwner:      "KilimcininKorOglu",
		repoName:       "lankeeper",
		backup:         backup,
	}
	svc.restorePendingUpdate()
	return svc
}

type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	Architecture   string `json:"architecture"`
	ReleaseNotes   string `json:"releaseNotes"`
	DownloadURL    string `json:"downloadURL"`
	ChecksumURL    string `json:"checksumURL,omitempty"`
	AssetName      string `json:"assetName"`
	PublishedAt    string `json:"publishedAt"`
	AssetSize      int64  `json:"assetSize"`
}

type VersionInfo struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	Date         string `json:"date"`
	Architecture string `json:"architecture"`
}

func (s *UpdateService) GetVersionInfo() *VersionInfo {
	return &VersionInfo{
		Version:      s.currentVersion,
		Commit:       s.currentCommit,
		Date:         s.currentDate,
		Architecture: s.architecture,
	}
}

type updateState struct {
	PendingVersion  string    `json:"pendingVersion"`
	PreviousVersion string    `json:"previousVersion"`
	BackupBinary    string    `json:"backupBinary"`
	AppliedAt       time.Time `json:"appliedAt"`
}

func (s *UpdateService) HasPendingUpdate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingVersion != ""
}

func (s *UpdateService) PendingVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingVersion
}

type ghRelease struct {
	TagName   string    `json:"tag_name"`
	Body      string    `json:"body"`
	Published time.Time `json:"published_at"`
	Assets    []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (s *UpdateService) CheckForUpdate(ctx context.Context) (*UpdateInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", s.repoOwner, s.repoName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lankeeper/"+s.currentVersion)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	info := &UpdateInfo{
		CurrentVersion: s.currentVersion,
		LatestVersion:  release.TagName,
		Architecture:   s.architecture,
		ReleaseNotes:   release.Body,
		PublishedAt:    release.Published.Format("2006-01-02"),
	}

	assetNeedle := "linux-" + s.architecture
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, assetNeedle) && strings.HasSuffix(asset.Name, ".tar.gz") {
			info.AssetName = asset.Name
			info.DownloadURL = asset.BrowserDownloadURL
			info.AssetSize = asset.Size
		}
		if asset.Name == "SHA256SUMS" || asset.Name == "checksums.txt" {
			info.ChecksumURL = asset.BrowserDownloadURL
		}
	}

	info.Available = CompareSemver(release.TagName, s.currentVersion) > 0

	return info, nil
}

func (s *UpdateService) ApplyUpdate(ctx context.Context, info *UpdateInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if info.DownloadURL == "" {
		return fmt.Errorf("no download URL for linux-%s asset", s.architecture)
	}

	log.Printf("starting update from %s to %s", s.currentVersion, info.LatestVersion)

	if s.backup != nil {
		backupPath := fmt.Sprintf("/var/lib/lankeeper/backups/pre-update-%s.tar.gz", info.LatestVersion)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
			log.Printf("pre-update backup: mkdir: %v", err)
		}
		if err := s.backup.Export(ctx, backupPath, ""); err != nil {
			log.Printf("pre-update backup failed (continuing): %v", err)
		}
	}

	tmpArchive := filepath.Join("/tmp", safeUpdateFileName(info.AssetName))
	if err := s.downloadFile(ctx, info.DownloadURL, tmpArchive); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() { _ = os.Remove(tmpArchive) }()

	if err := s.verifyChecksum(ctx, info, tmpArchive); err != nil {
		return fmt.Errorf("checksum verification: %w", err)
	}

	tmpBinary := "/tmp/lankeeper-new"
	if err := s.extractBinary(tmpArchive, tmpBinary); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	defer func() { _ = os.Remove(tmpBinary) }()

	backupBinary := s.binaryPath + ".bak"
	if _, err := netutil.Run(ctx, "cp", "-f", s.binaryPath, backupBinary); err != nil {
		return fmt.Errorf("backup binary: %w", err)
	}

	if _, err := netutil.Run(ctx, "cp", "-f", tmpBinary, s.binaryPath); err != nil {
		// Best-effort rollback; if the restore also fails the operator
		// has the .bak file to recover by hand.
		if _, rbErr := netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath); rbErr != nil {
			log.Printf("update: rollback failed: %v", rbErr)
		}
		return fmt.Errorf("install binary: %w", err)
	}

	if _, err := netutil.Run(ctx, "chmod", "+x", s.binaryPath); err != nil {
		if _, rbErr := netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath); rbErr != nil {
			log.Printf("update: rollback failed: %v", rbErr)
		}
		return fmt.Errorf("chmod: %w", err)
	}

	out, err := s.runBinaryVersion(ctx)
	if err != nil || !strings.Contains(out, strings.TrimPrefix(info.LatestVersion, "v")) {
		log.Printf("version check failed after install: %v (output: %s)", err, out)
		if _, rbErr := netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath); rbErr != nil {
			log.Printf("update: rollback failed: %v", rbErr)
		}
		return fmt.Errorf("version verification failed")
	}

	state := updateState{
		PendingVersion:  info.LatestVersion,
		PreviousVersion: s.currentVersion,
		BackupBinary:    backupBinary,
		AppliedAt:       time.Now().UTC(),
	}
	if err := s.saveUpdateState(state); err != nil {
		if _, rbErr := netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath); rbErr != nil {
			log.Printf("update: rollback failed: %v", rbErr)
		}
		return fmt.Errorf("save update state: %w", err)
	}

	s.pendingVersion = info.LatestVersion
	s.previousVersion = s.currentVersion
	s.backupBinary = backupBinary

	watchCtx, cancel := context.WithCancel(context.Background())
	s.watchdogCancel = cancel

	go s.watchdog(watchCtx, updateConfirmWindow)

	log.Printf("update to %s applied, waiting for confirmation (60s watchdog)", info.LatestVersion)

	s.updateBootBranding(ctx, info.LatestVersion)
	if _, err := netutil.Run(ctx, "systemctl", "restart", "lankeeper.target"); err != nil {
		log.Printf("update: systemctl restart: %v", err)
	}

	return nil
}

func (s *UpdateService) ConfirmUpdate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.watchdogCancel != nil {
		s.watchdogCancel()
		s.watchdogCancel = nil
	}

	if s.backupBinary != "" {
		if _, err := netutil.Run(ctx, "rm", "-f", s.backupBinary); err != nil {
			log.Printf("update: remove backup binary: %v", err)
		}
	}
	if err := s.clearUpdateState(); err != nil {
		log.Printf("clear update state failed: %v", err)
	}

	log.Printf("update to %s confirmed", s.pendingVersion)
	s.pendingVersion = ""
	s.previousVersion = ""
	s.backupBinary = ""

	return nil
}

func (s *UpdateService) Rollback(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.watchdogCancel != nil {
		s.watchdogCancel()
		s.watchdogCancel = nil
	}

	backupBinary := s.backupBinary
	if backupBinary == "" {
		backupBinary = s.binaryPath + ".bak"
	}
	if _, err := netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}

	log.Printf("update rolled back from %s", s.pendingVersion)
	if s.previousVersion != "" {
		s.updateBootBranding(ctx, s.previousVersion)
	}
	if err := s.clearUpdateState(); err != nil {
		log.Printf("clear update state failed: %v", err)
	}
	s.pendingVersion = ""
	s.previousVersion = ""
	s.backupBinary = ""

	if _, err := netutil.Run(ctx, "systemctl", "restart", "lankeeper.target"); err != nil {
		log.Printf("update: systemctl restart after rollback: %v", err)
	}

	return nil
}

func (s *UpdateService) watchdog(ctx context.Context, delay time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
		log.Println("update watchdog: no confirmation received, rolling back")
		rollbackCtx := context.Background()
		if err := s.Rollback(rollbackCtx); err != nil {
			log.Printf("update watchdog rollback failed: %v", err)
		}
	}
}

func (s *UpdateService) restorePendingUpdate() {
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}

	var state updateState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("update: ignoring invalid state file: %v", err)
		return
	}
	if state.PendingVersion == "" || state.BackupBinary == "" {
		return
	}

	s.pendingVersion = state.PendingVersion
	s.previousVersion = state.PreviousVersion
	s.backupBinary = state.BackupBinary

	elapsed := time.Since(state.AppliedAt)
	remaining := updateConfirmWindow - elapsed
	if remaining < 0 {
		remaining = 0
	}

	watchCtx, cancel := context.WithCancel(context.Background())
	s.watchdogCancel = cancel
	go s.watchdog(watchCtx, remaining)

	log.Printf("update: restored pending update to %s, rollback in %s", state.PendingVersion, remaining)
}

func (s *UpdateService) saveUpdateState(state updateState) error {
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.statePath, data, 0o600)
}

func (s *UpdateService) clearUpdateState() error {
	if err := os.Remove(s.statePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *UpdateService) updateBootBranding(ctx context.Context, version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}

	content := fmt.Sprintf("GRUB_DISTRIBUTOR=\"LANKeeper %s\"\n", version)
	if err := netutil.WriteFile("/etc/default/grub.d/lankeeper.cfg", []byte(content), 0o644); err != nil {
		log.Printf("update: GRUB branding write failed: %v", err)
		return
	}
	if _, err := netutil.Run(ctx, "update-grub"); err != nil {
		log.Printf("update: update-grub failed: %v", err)
	}
}

func (s *UpdateService) runBinaryVersion(ctx context.Context) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	out, err := exec.CommandContext(ctx, s.binaryPath, "version").CombinedOutput()
	return string(out), err
}

func safeUpdateFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "lankeeper-update.tar.gz"
	}
	return name
}

func (s *UpdateService) downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lankeeper/"+s.currentVersion)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = io.Copy(f, resp.Body)
	return err
}

func (s *UpdateService) extractBinary(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip open: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		if filepath.Base(header.Name) == "lankeeper" && header.Typeflag == tar.TypeReg {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("close binary: %w", err)
			}
			if err := os.Chmod(destPath, 0o755); err != nil {
				return fmt.Errorf("chmod binary: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("binary 'lankeeper' not found in archive")
}

func CompareSemver(a, b string) int {
	aParts := parseSemver(a)
	bParts := parseSemver(b)

	for i := 0; i < 3; i++ {
		if aParts[i] > bParts[i] {
			return 1
		}
		if aParts[i] < bParts[i] {
			return -1
		}
	}
	return 0
}

func (s *UpdateService) verifyChecksum(ctx context.Context, info *UpdateInfo, archivePath string) error {
	if info.ChecksumURL == "" {
		log.Println("update: no checksum file in release, skipping verification")
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", info.ChecksumURL, nil)
	if err != nil {
		return fmt.Errorf("create checksum request: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download checksum file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksum file returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read checksum file: %w", err)
	}

	archiveName := filepath.Base(archivePath)
	var expectedHash string
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(fields[1], archiveName) {
			expectedHash = strings.ToLower(fields[0])
			break
		}
	}

	if expectedHash == "" {
		return fmt.Errorf("no checksum found for %s in release checksum file", archiveName)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive for checksum: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	log.Printf("update: SHA-256 verified for %s", archiveName)
	return nil
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")

	if idx := strings.Index(v, "-"); idx != -1 {
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	var result [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		result[i], _ = strconv.Atoi(parts[i])
	}
	return result
}
