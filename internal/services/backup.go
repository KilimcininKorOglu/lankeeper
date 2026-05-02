package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type BackupService struct {
	configDir string
}

func NewBackupService(configDir string) *BackupService {
	return &BackupService{configDir: configDir}
}

func (s *BackupService) Export(ctx context.Context, outputPath string) error {
	if outputPath == "" {
		outputPath = fmt.Sprintf("/tmp/home-router-backup-%s.tar.gz",
			time.Now().Format("20060102-150405"))
	}

	_, err := netutil.Run(ctx, "tar", "czf", outputPath,
		"-C", filepath.Dir(s.configDir), filepath.Base(s.configDir),
		"-C", "/etc", "unbound",
		"-C", "/etc", "dnsmasq.d",
	)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}

	return nil
}

func (s *BackupService) Import(ctx context.Context, archivePath string) error {
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("backup file not found: %w", err)
	}

	_, err := netutil.Run(ctx, "tar", "xzf", archivePath,
		"-C", filepath.Dir(s.configDir))
	if err != nil {
		return fmt.Errorf("extract backup: %w", err)
	}

	return nil
}

func (s *BackupService) FactoryReset(ctx context.Context) error {
	defaultsDir := filepath.Join(filepath.Dir(s.configDir), "configs", "defaults")

	entries, err := os.ReadDir(defaultsDir)
	if err != nil {
		return fmt.Errorf("read defaults: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(defaultsDir, entry.Name())
		dst := filepath.Join(s.configDir, entry.Name())

		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		os.WriteFile(dst, data, 0o644)
	}

	return nil
}
