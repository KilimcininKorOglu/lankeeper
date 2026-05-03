package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type UpdateService struct {
	currentVersion string
	currentCommit  string
	currentDate    string
	binaryPath     string
	repoOwner      string
	repoName       string
	backup         *BackupService
	mu             sync.Mutex
	watchdogCancel context.CancelFunc
	pendingVersion string
}

func NewUpdateService(version, commit, date string, backup *BackupService) *UpdateService {
	return &UpdateService{
		currentVersion: version,
		currentCommit:  commit,
		currentDate:    date,
		binaryPath:     "/usr/local/bin/home-router",
		repoOwner:      "KilimcininKorOglu",
		repoName:       "home-router",
		backup:         backup,
	}
}

type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseNotes   string `json:"releaseNotes"`
	DownloadURL    string `json:"downloadURL"`
	PublishedAt    string `json:"publishedAt"`
	AssetSize      int64  `json:"assetSize"`
}

type VersionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func (s *UpdateService) GetVersionInfo() *VersionInfo {
	return &VersionInfo{
		Version: s.currentVersion,
		Commit:  s.currentCommit,
		Date:    s.currentDate,
	}
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
	TagName    string    `json:"tag_name"`
	Body       string    `json:"body"`
	Published  time.Time `json:"published_at"`
	Assets     []ghAsset `json:"assets"`
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
	req.Header.Set("User-Agent", "home-router/"+s.currentVersion)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()

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
		ReleaseNotes:   release.Body,
		PublishedAt:    release.Published.Format("2006-01-02"),
	}

	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, "linux-amd64") && strings.HasSuffix(asset.Name, ".tar.gz") {
			info.DownloadURL = asset.BrowserDownloadURL
			info.AssetSize = asset.Size
			break
		}
	}

	info.Available = CompareSemver(release.TagName, s.currentVersion) > 0

	return info, nil
}

func (s *UpdateService) ApplyUpdate(ctx context.Context, info *UpdateInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if info.DownloadURL == "" {
		return fmt.Errorf("no download URL for linux-amd64 asset")
	}

	log.Printf("starting update from %s to %s", s.currentVersion, info.LatestVersion)

	if s.backup != nil {
		backupPath := fmt.Sprintf("/var/lib/home-router/backups/pre-update-%s.tar.gz", info.LatestVersion)
		os.MkdirAll(filepath.Dir(backupPath), 0o755)
		if err := s.backup.Export(ctx, backupPath); err != nil {
			log.Printf("pre-update backup failed (continuing): %v", err)
		}
	}

	tmpArchive := fmt.Sprintf("/tmp/home-router-update-%s.tar.gz", info.LatestVersion)
	if err := s.downloadFile(ctx, info.DownloadURL, tmpArchive); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(tmpArchive)

	tmpBinary := "/tmp/home-router-new"
	if err := s.extractBinary(tmpArchive, tmpBinary); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	defer os.Remove(tmpBinary)

	backupBinary := s.binaryPath + ".bak"
	if _, err := netutil.Run(ctx, "cp", "-f", s.binaryPath, backupBinary); err != nil {
		return fmt.Errorf("backup binary: %w", err)
	}

	if _, err := netutil.Run(ctx, "cp", "-f", tmpBinary, s.binaryPath); err != nil {
		netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath)
		return fmt.Errorf("install binary: %w", err)
	}

	if _, err := netutil.Run(ctx, "chmod", "+x", s.binaryPath); err != nil {
		netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath)
		return fmt.Errorf("chmod: %w", err)
	}

	out, err := netutil.RunSimple(ctx, s.binaryPath, "version")
	if err != nil || !strings.Contains(out, strings.TrimPrefix(info.LatestVersion, "v")) {
		log.Printf("version check failed after install: %v (output: %s)", err, out)
		netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath)
		return fmt.Errorf("version verification failed")
	}

	s.pendingVersion = info.LatestVersion

	watchCtx, cancel := context.WithCancel(context.Background())
	s.watchdogCancel = cancel

	go s.watchdog(watchCtx, backupBinary)

	log.Printf("update to %s applied, waiting for confirmation (60s watchdog)", info.LatestVersion)

	netutil.Run(ctx, "systemctl", "restart", "home-router.target")

	return nil
}

func (s *UpdateService) ConfirmUpdate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.watchdogCancel != nil {
		s.watchdogCancel()
		s.watchdogCancel = nil
	}

	backupBinary := s.binaryPath + ".bak"
	os.Remove(backupBinary)

	log.Printf("update to %s confirmed", s.pendingVersion)
	s.pendingVersion = ""

	return nil
}

func (s *UpdateService) Rollback(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.watchdogCancel != nil {
		s.watchdogCancel()
		s.watchdogCancel = nil
	}

	backupBinary := s.binaryPath + ".bak"
	if _, err := netutil.Run(ctx, "cp", "-f", backupBinary, s.binaryPath); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}

	log.Printf("update rolled back from %s", s.pendingVersion)
	s.pendingVersion = ""

	netutil.Run(ctx, "systemctl", "restart", "home-router.target")

	return nil
}

func (s *UpdateService) watchdog(ctx context.Context, backupBinary string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
		log.Println("update watchdog: no confirmation received, rolling back")
		s.mu.Lock()
		s.pendingVersion = ""
		s.watchdogCancel = nil
		s.mu.Unlock()
		rollbackCtx := context.Background()
		netutil.Run(rollbackCtx, "cp", "-f", backupBinary, s.binaryPath)
		netutil.Run(rollbackCtx, "systemctl", "restart", "home-router.target")
	}
}

func (s *UpdateService) downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "home-router/"+s.currentVersion)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func (s *UpdateService) extractBinary(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip open: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		if filepath.Base(header.Name) == "home-router" && header.Typeflag == tar.TypeReg {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
			os.Chmod(destPath, 0o755)
			return nil
		}
	}

	return fmt.Errorf("binary 'home-router' not found in archive")
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
