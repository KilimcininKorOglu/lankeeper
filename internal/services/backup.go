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
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/KilimcininKorOglu/lankeeper/configs"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type BackupService struct {
	configDir string
	defaults  fs.FS
	runMu     sync.Mutex
	runner    func(context.Context) error
}

func NewBackupService(configDir string) *BackupService {
	return &BackupService{configDir: configDir, defaults: configs.DefaultsFS}
}

// NewBackupServiceWithDefaults overrides the factory-default source.
// The filesystem must expose the YAML files under defaultsSubdir, the
// same layout the embedded copy has. Only tests need this.
func NewBackupServiceWithDefaults(configDir string, defaults fs.FS) *BackupService {
	return &BackupService{configDir: configDir, defaults: defaults}
}

// SetRunner installs the orchestration callback used by RunNow and
// the cron scheduler. Wired by server.go after the targets / cfg
// reference is available, so the service itself stays free of any
// config dependency.
func (s *BackupService) SetRunner(fn func(context.Context) error) {
	s.runner = fn
}

func (s *BackupService) Export(ctx context.Context, outputPath, passphrase string) error {
	if outputPath == "" {
		outputPath = fmt.Sprintf("/tmp/lankeeper-backup-%s.tar.gz",
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
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

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
			if err := netutil.MkdirAll(target, os.FileMode(hdr.Mode)|0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
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

// defaultsSubdir is the directory holding the factory YAML files inside
// the defaults filesystem.
const defaultsSubdir = "defaults"

// FactoryReset restores the shipped default configuration. The source is
// the embedded copy rather than a directory on disk, so the reset works
// on any install layout and always matches the running binary.
//
// Every write failure is collected and returned. Reporting success after
// writing nothing would reboot the router into the state the operator
// was trying to leave.
func (s *BackupService) FactoryReset(ctx context.Context) error {
	entries, err := fs.ReadDir(s.defaults, defaultsSubdir)
	if err != nil {
		return fmt.Errorf("read defaults: %w", err)
	}

	var failed []string
	written := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		dst := filepath.Join(s.configDir, entry.Name())

		fileData, err := fs.ReadFile(s.defaults, path.Join(defaultsSubdir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read default %s: %w", entry.Name(), err)
		}
		// 0640 matches the mode the installer applies. These files hold
		// the session secret and the admin password hash, so a reset
		// must not widen their permissions.
		if err := netutil.WriteFile(dst, fileData, 0o640); err != nil {
			log.Printf("factory reset: write %s: %v", dst, err)
			failed = append(failed, entry.Name())
			continue
		}
		written++
	}

	if len(failed) > 0 {
		return fmt.Errorf("factory reset: %d of %d defaults could not be written: %s",
			len(failed), len(failed)+written, strings.Join(failed, ", "))
	}
	if written == 0 {
		return fmt.Errorf("factory reset: no default configs found")
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
