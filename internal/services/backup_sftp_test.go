package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

func TestSFTPAuthMethodsValidation(t *testing.T) {
	cases := []struct {
		name    string
		t       config.BackupTarget
		wantErr string
	}{
		{
			name:    "no creds",
			t:       config.BackupTarget{Host: "host", User: "u"},
			wantErr: "KeyPath or Password",
		},
		{
			name:    "missing key file",
			t:       config.BackupTarget{Host: "host", User: "u", KeyPath: "/nonexistent/key"},
			wantErr: "read key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sftpAuthMethods(tc.t)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestUploadSFTPRequiresHost(t *testing.T) {
	_, err := uploadSFTP(context.Background(), "/tmp/whatever", config.BackupTarget{})
	if err == nil || !strings.Contains(err.Error(), "host required") {
		t.Errorf("expected host-required error, got %v", err)
	}
}

func TestCleanupSFTPRejectsZeroRetention(t *testing.T) {
	_, err := cleanupSFTP(context.Background(), config.BackupTarget{Host: "h"}, 0)
	if err == nil || !strings.Contains(err.Error(), "retention") {
		t.Errorf("expected retention error, got %v", err)
	}
}

// testHostKey builds a public key the callback can fingerprint. No
// network and no SSH server are involved: the callback is a pure
// function of the target and the presented key.
func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrap key: %v", err)
	}
	return key
}

var testHostAddr = &net.TCPAddr{IP: net.IPv4(10, 10, 10, 20), Port: 22}

// TestSFTPUnpinnedHostKeyIsRefused is the core of the fix. The previous
// behaviour accepted every key, so an on-path attacker could collect the
// archive and, when a password is configured, the credential with it.
//
// The refusal must name the presented fingerprint: that string travels
// into the backup history row, and it is the only way the operator
// learns what to pin.
func TestSFTPUnpinnedHostKeyIsRefused(t *testing.T) {
	key := testHostKey(t)
	cb := pinnedHostKeyCallback(config.BackupTarget{Host: "nas.hermes.lan"})

	err := cb("nas.hermes.lan:22", testHostAddr, key)
	if err == nil {
		t.Fatal("unpinned host key was accepted")
	}
	if !strings.Contains(err.Error(), ssh.FingerprintSHA256(key)) {
		t.Errorf("refusal does not name the presented fingerprint, operator cannot recover: %v", err)
	}
}

// TestSFTPPinnedHostKeyIsAccepted keeps the fix from being a blanket
// denial.
func TestSFTPPinnedHostKeyIsAccepted(t *testing.T) {
	key := testHostKey(t)
	cb := pinnedHostKeyCallback(config.BackupTarget{
		Host:               "nas.hermes.lan",
		HostKeyFingerprint: ssh.FingerprintSHA256(key),
	})

	if err := cb("nas.hermes.lan:22", testHostAddr, key); err != nil {
		t.Errorf("matching fingerprint was rejected: %v", err)
	}
}

// TestSFTPHostKeyMismatchIsRefused covers the case the whole mechanism
// exists for: the key the server presents changed between runs.
func TestSFTPHostKeyMismatchIsRefused(t *testing.T) {
	pinned := testHostKey(t)
	presented := testHostKey(t)

	cb := pinnedHostKeyCallback(config.BackupTarget{
		Host:               "nas.hermes.lan",
		HostKeyFingerprint: ssh.FingerprintSHA256(pinned),
	})

	err := cb("nas.hermes.lan:22", testHostAddr, presented)
	if err == nil {
		t.Fatal("mismatched host key was accepted")
	}
	for _, want := range []string{
		ssh.FingerprintSHA256(pinned),
		ssh.FingerprintSHA256(presented),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("mismatch error omits %s: %v", want, err)
		}
	}
}

// TestSFTPPinnedFingerprintIsTrimmed matters because the value is
// pasted in by hand from a history row.
func TestSFTPPinnedFingerprintIsTrimmed(t *testing.T) {
	key := testHostKey(t)
	cb := pinnedHostKeyCallback(config.BackupTarget{
		Host:               "nas.hermes.lan",
		HostKeyFingerprint: "  " + ssh.FingerprintSHA256(key) + "\n",
	})

	if err := cb("nas.hermes.lan:22", testHostAddr, key); err != nil {
		t.Errorf("padded fingerprint was rejected: %v", err)
	}
}
