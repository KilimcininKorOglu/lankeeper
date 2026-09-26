package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The guard trusts nothing in the state file beyond its existence. The
// file lives in a directory the service account owns, so a path read
// from it would let that account choose what root copies over the binary.
const (
	guardStatePath  = "/var/lib/lankeeper/update-state.json"
	guardBinaryPath = "/usr/local/bin/lankeeper"
	guardBackupPath = "/usr/local/bin/lankeeper.bak"
)

// runUpdateGuard is started by a transient systemd timer that ApplyUpdate
// arms, from the previous binary, so it survives the restart that
// replaces the web process and runs even when the new release cannot
// start. It restores the previous binary when the update was not
// confirmed in time.
func runUpdateGuard() error {
	return rollbackUnconfirmedUpdate(guardStatePath, guardBinaryPath, guardBackupPath, restartTarget)
}

// rollbackUnconfirmedUpdate restores backup over binary when statePath
// still exists. ConfirmUpdate removes the state file, so its absence
// means the update was accepted and there is nothing to do.
func rollbackUnconfirmedUpdate(statePath, binary, backup string, restart func() error) error {
	if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
		log.Println("update-guard: update confirmed, nothing to roll back")
		return nil
	} else if err != nil {
		return fmt.Errorf("stat update state: %w", err)
	}

	if err := replaceFile(backup, binary); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		log.Printf("update-guard: remove backup binary: %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		log.Printf("update-guard: remove update state: %v", err)
	}
	log.Println("update-guard: update was not confirmed, previous binary restored")
	return restart()
}

// replaceFile copies src next to dst and renames it over dst, so dst is
// never a half-written executable.
func replaceFile(src, dst string) error {
	// src is guardBackupPath in production, a constant; tests pass a temp
	// path. Nothing reaches it from the state file or a caller.
	// #nosec G304
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".lankeeper-restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func restartTarget() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "restart", "lankeeper.target").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart lankeeper.target: %w (%s)", err, out)
	}
	return nil
}
