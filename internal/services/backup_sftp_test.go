package services

import (
	"context"
	"strings"
	"testing"

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
