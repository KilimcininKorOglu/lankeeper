package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// pinnedHostKeyCallback verifies the server's host key against the
// fingerprint the operator pinned on the target.
//
// An unpinned target is refused rather than trusted, because accepting
// an unverified key is what let an on-path attacker collect the archive
// and, when a password is configured, the credential with it. The
// refusal carries the fingerprint the server actually presented: that
// error reaches the operator through the backup history table, which is
// how they learn the value to pin without a separate tool.
func pinnedHostKeyCallback(t config.BackupTarget) ssh.HostKeyCallback {
	want := strings.TrimSpace(t.HostKeyFingerprint)

	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)

		if want == "" {
			return fmt.Errorf(
				"host key not pinned: server presented %s, set the host key fingerprint on this target to trust it",
				got)
		}
		if got != want {
			return fmt.Errorf("host key mismatch: expected %s, server presented %s", want, got)
		}
		return nil
	}
}

// dialSFTP establishes an SSH connection then layers an SFTP client
// on top. Auth precedence: KeyPath (if set) over Password. The host
// key is verified against the fingerprint pinned on the target; see
// pinnedHostKeyCallback.
func dialSFTP(ctx context.Context, t config.BackupTarget) (*sftp.Client, *ssh.Client, error) {
	if t.Host == "" {
		return nil, nil, errors.New("sftp host required")
	}
	port := t.Port
	if port == 0 {
		port = 22
	}

	auths, err := sftpAuthMethods(t)
	if err != nil {
		return nil, nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            auths,
		HostKeyCallback: pinnedHostKeyCallback(t),
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(t.Host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("ssh handshake: %w", err)
	}
	sshClient := ssh.NewClient(c, chans, reqs)

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, nil, fmt.Errorf("sftp client: %w", err)
	}
	return sftpClient, sshClient, nil
}

func sftpAuthMethods(t config.BackupTarget) ([]ssh.AuthMethod, error) {
	var auths []ssh.AuthMethod
	if t.KeyPath != "" {
		raw, err := os.ReadFile(t.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", t.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		auths = append(auths, ssh.Password(t.Password))
	}
	if len(auths) == 0 {
		return nil, errors.New("sftp requires KeyPath or Password")
	}
	return auths, nil
}

// uploadSFTP copies srcPath into t.RemoteDir/<basename>. Atomic via
// .tmp + rename: if the SSH session drops mid-transfer the partial
// file lands in .tmp and gets cleaned up by the next run, never
// confusing retention into thinking it succeeded.
func uploadSFTP(ctx context.Context, srcPath string, t config.BackupTarget) (string, error) {
	client, ssh, err := dialSFTP(ctx, t)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = client.Close()
		_ = ssh.Close()
	}()

	if t.RemoteDir != "" {
		if err := client.MkdirAll(t.RemoteDir); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", t.RemoteDir, err)
		}
	}

	// srcPath is the archive this service just produced.
	// #nosec G304
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()

	final := path.Join(t.RemoteDir, filepathBase(srcPath))
	tmp := final + ".tmp"

	dst, err := client.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return "", fmt.Errorf("create tmp: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = client.Remove(tmp)
		return "", fmt.Errorf("copy: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = client.Remove(tmp)
		return "", fmt.Errorf("close tmp: %w", err)
	}
	// PosixRename overwrites; pkg/sftp's plain Rename refuses if the
	// target exists, which would kill an idempotent re-run.
	if err := client.PosixRename(tmp, final); err != nil {
		_ = client.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// cleanupSFTP keeps the newest `keep` files matching the
// "lankeeper-backup-" prefix and deletes the rest. Mirrors the
// local cleanup logic so an operator switching backends doesn't
// see retention drift.
func cleanupSFTP(ctx context.Context, t config.BackupTarget, keep int) ([]string, error) {
	if keep < 1 {
		return nil, errors.New("retention must be >= 1")
	}
	client, ssh, err := dialSFTP(ctx, t)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = client.Close()
		_ = ssh.Close()
	}()

	dir := t.RemoteDir
	if dir == "" {
		dir = "."
	}
	entries, err := client.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir %s: %w", dir, err)
	}

	type fileInfo struct {
		path  string
		mtime time.Time
	}
	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "lankeeper-backup-") {
			continue
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		files = append(files, fileInfo{
			path:  path.Join(dir, e.Name()),
			mtime: e.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })

	var deleted []string
	for i := keep; i < len(files); i++ {
		if err := client.Remove(files[i].path); err != nil {
			return deleted, fmt.Errorf("remove %s: %w", files[i].path, err)
		}
		deleted = append(deleted, files[i].path)
	}
	return deleted, nil
}
