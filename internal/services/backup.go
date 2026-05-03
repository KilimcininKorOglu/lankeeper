package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type BackupService struct {
	configDir string
}

func NewBackupService(configDir string) *BackupService {
	return &BackupService{configDir: configDir}
}

func (s *BackupService) Export(ctx context.Context, outputPath, passphrase string) error {
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

	if passphrase != "" {
		plaintext, err := os.ReadFile(outputPath)
		if err != nil {
			return fmt.Errorf("read archive for encryption: %w", err)
		}

		encrypted, err := encryptBackup(plaintext, passphrase)
		if err != nil {
			return fmt.Errorf("encrypt backup: %w", err)
		}

		if err := os.WriteFile(outputPath, encrypted, 0o600); err != nil {
			return fmt.Errorf("write encrypted backup: %w", err)
		}
	}

	return nil
}

func (s *BackupService) Import(ctx context.Context, archivePath, passphrase string) error {
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}

	if passphrase != "" {
		decrypted, err := decryptBackup(data, passphrase)
		if err != nil {
			return fmt.Errorf("decrypt backup: %w", err)
		}
		if err := os.WriteFile(archivePath, decrypted, 0o600); err != nil {
			return fmt.Errorf("write decrypted backup: %w", err)
		}
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	destRoot := s.configDir
	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}

		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe tar member rejected: %s", hdr.Name)
		}

		target := filepath.Join(destRoot, clean)
		if !strings.HasPrefix(target, destRoot+string(os.PathSeparator)) && target != destRoot {
			return fmt.Errorf("tar member escapes destination: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			netutil.MkdirAll(target, os.FileMode(hdr.Mode)|0o755)
		case tar.TypeReg:
			memberData, err := io.ReadAll(io.LimitReader(tr, 10<<20))
			if err != nil {
				return fmt.Errorf("read tar member %s: %w", hdr.Name, err)
			}
			if err := netutil.WriteFile(target, memberData, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("write tar member %s: %w", hdr.Name, err)
			}
		default:
			return fmt.Errorf("unsupported tar member type %d: %s", hdr.Typeflag, hdr.Name)
		}
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

		fileData, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		netutil.WriteFile(dst, fileData, 0o644)
	}

	return nil
}

const (
	scryptN      = 1 << 15
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 16
)

func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
}

func encryptBackup(plaintext []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Format: salt + nonce + ciphertext
	result := make([]byte, 0, saltLen+len(nonce)+len(ciphertext))
	result = append(result, salt...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)
	return result, nil
}

func decryptBackup(data []byte, passphrase string) ([]byte, error) {
	if len(data) < saltLen+12 {
		return nil, fmt.Errorf("encrypted backup too short")
	}

	salt := data[:saltLen]
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < saltLen+nonceSize {
		return nil, fmt.Errorf("encrypted backup too short for nonce")
	}

	nonce := data[saltLen : saltLen+nonceSize]
	ciphertext := data[saltLen+nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w (wrong passphrase?)", err)
	}

	return plaintext, nil
}
