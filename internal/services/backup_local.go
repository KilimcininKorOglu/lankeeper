package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// LocalBackupRoot is the only path local backups may live under.
// Defended in depth: agent's WriteFile path-whitelist already rejects
// arbitrary paths, but we narrow further so the operator can't aim
// the backup at, say, /etc/passwd or /var/log/journal.
const LocalBackupRoot = "/var/lib/lankeeper/backups/"

// backupRootForTesting overrides LocalBackupRoot so tests can run
// against a TempDir without touching the system path. Production
// code never assigns to it.
var backupRootForTesting = LocalBackupRoot

// SetBackupRootForTesting is the cross-package test hook for
// backup_orchestration_test.go (which lives in services_test, so
// it cannot touch the unexported variable directly). Production
// callers must NEVER invoke this.
func SetBackupRootForTesting(root string) { backupRootForTesting = root }

// validateLocalPath returns the cleaned absolute path or an error.
// Empty defaults to the configured root. Trailing-slash normalised
// so the prefix check below cannot be bypassed by trickery.
func validateLocalPath(spec string) (string, error) {
	root := backupRootForTesting
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	if spec == "" {
		return strings.TrimSuffix(root, "/"), nil
	}
	clean := filepath.Clean(spec)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("local target path must be absolute, got %q", spec)
	}
	withSlash := clean
	if !strings.HasSuffix(withSlash, "/") {
		withSlash += "/"
	}
	if !strings.HasPrefix(withSlash, root) {
		return "", fmt.Errorf("local target path must live under %s", root)
	}
	return clean, nil
}

// uploadLocal copies srcPath into the configured directory under a
// stable filename. Atomic via tmp-then-rename so a crash mid-copy
// never leaves a half-written backup that retention would later
// keep around. Returns the final path so the caller can record it
// in the run history.
func uploadLocal(ctx context.Context, srcPath string, t config.BackupTarget) (string, error) {
	dir, err := validateLocalPath(t.Path)
	if err != nil {
		return "", err
	}
	if err := netutil.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}

	final := filepath.Join(dir, filepath.Base(srcPath))
	tmp := final + ".tmp"

	// srcPath is the archive this service just produced.
	// #nosec G304
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open src: %w", err)
	}
	defer func() { _ = src.Close() }()

	// tmp is derived from the destination, which is confined to
	// the whitelisted backups directory.
	// #nosec G304
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create tmp: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("copy to tmp: %w", err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("fsync tmp: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// cleanupLocal removes the oldest backup files from the target
// directory until at most `keep` remain. Returns the list of
// deleted paths so the orchestrator can log them. We filter on
// the lankeeper- prefix so user-dropped files in the same dir
// (manual rsync, scratch tarballs) aren't molested.
func cleanupLocal(t config.BackupTarget, keep int) ([]string, error) {
	if keep < 1 {
		return nil, errors.New("retention must be >= 1")
	}
	dir, err := validateLocalPath(t.Path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir: %w", err)
	}

	type fileWithMtime struct {
		path  string
		mtime int64
	}
	var files []fileWithMtime
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "lankeeper-backup-") {
			continue
		}
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileWithMtime{
			path:  filepath.Join(dir, name),
			mtime: info.ModTime().UnixNano(),
		})
	}

	// Newest first.
	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime })

	var deleted []string
	for i := keep; i < len(files); i++ {
		if err := os.Remove(files[i].path); err != nil {
			return deleted, fmt.Errorf("remove %s: %w", files[i].path, err)
		}
		deleted = append(deleted, files[i].path)
	}
	return deleted, nil
}
