package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
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
	"github.com/KilimcininKorOglu/lankeeper/internal/config"
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

// backupExtraDirs are the system directories archived alongside the
// config directory.
//
// /etc/openvpn holds the easy-rsa PKI: the CA key and certificate, the
// server certificate, every issued client certificate, and ta.key. None
// of that is mirrored into router.yaml, which stores only names, ports,
// ciphers, and per-client metadata, so a restore without this directory
// leaves the OpenVPN server unable to start and forces every client
// certificate to be reissued. The WireGuard private keys live in
// router.yaml, encrypted with the credential key, which an encrypted
// export carries as archiveKeyMember.
var backupExtraDirs = []string{
	"/etc/unbound",
	"/etc/dnsmasq.d",
	"/etc/openvpn",
}

// buildExportArgs assembles the tar argument list for an export. A
// directory that does not exist is skipped and logged: a subsystem that
// was never configured has no directory, and tar would fail the whole
// archive over one missing path.
func buildExportArgs(outputPath, configDir string, extraDirs []string) []string {
	args := []string{"czf", outputPath,
		"-C", filepath.Dir(configDir), filepath.Base(configDir),
	}
	for _, dir := range extraDirs {
		if _, err := os.Stat(dir); err != nil {
			log.Printf("backup: skipping %s: %v", dir, err)
			continue
		}
		args = append(args, "-C", filepath.Dir(dir), filepath.Base(dir))
	}
	return args
}

func (s *BackupService) Export(ctx context.Context, outputPath, passphrase string) error {
	if outputPath == "" {
		outputPath = fmt.Sprintf("/tmp/lankeeper-backup-%s.tar.gz",
			time.Now().Format("20060102-150405"))
	}

	if passphrase == "" {
		return s.exportPlain(ctx, outputPath)
	}
	return s.exportEncrypted(ctx, outputPath, passphrase)
}

// exportPlain has the agent write the archive straight to outputPath,
// which only the agent reads afterwards (the pre-update snapshot).
//
// The archive holds every secret on the device, and tar running under
// systemd's default umask would create it 0644 for the whole run. So the
// file is created owner-only first: tar truncates an existing archive and
// keeps its mode. Any earlier file is removed so a stale 0644 copy cannot
// lend its mode, and a failed run removes the partial archive.
func (s *BackupService) exportPlain(ctx context.Context, outputPath string) error {
	if _, err := netutil.Run(ctx, "rm", "-f", "--", outputPath); err != nil {
		return fmt.Errorf("remove stale backup archive: %w", err)
	}
	if err := netutil.WriteFile(outputPath, nil, 0o600); err != nil {
		return fmt.Errorf("create backup archive: %w", err)
	}
	if _, err := netutil.Run(ctx, "tar", buildExportArgs(outputPath, s.configDir, backupExtraDirs)...); err != nil {
		if _, rmErr := netutil.Run(ctx, "rm", "-f", "--", outputPath); rmErr != nil {
			log.Printf("backup: remove partial archive %s: %v", outputPath, rmErr)
		}
		return fmt.Errorf("create backup: %w", err)
	}
	return nil
}

// backupStagingDir is where the agent leaves a plaintext archive for this
// process to encrypt. The installer creates it root:<service group> with
// mode 2750: root writes into it, the setgid bit hands each file the
// service group, and this process can read but never create an entry,
// so it cannot plant a symlink for root's tar to follow.
func backupStagingDir() string {
	return filepath.Join(tlsDataDir(), "staging")
}

// exportEncrypted has the agent write the plaintext archive into the
// staging directory, reads it through the group, and writes only the
// encrypted copy to outputPath.
//
// The archive cannot go to outputPath directly: the callers build it
// under this process's /tmp, which PrivateTmp hides from the agent, and
// a file tar creates is root's, which this process could not replace
// with the encrypted copy anyway.
func (s *BackupService) exportEncrypted(ctx context.Context, outputPath, passphrase string) error {
	dir := backupStagingDir()
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("backup staging directory unavailable, re-run the installer: %w", err)
	}
	staged := filepath.Join(dir, fmt.Sprintf("export-%d.tar.gz", time.Now().UnixNano()))
	if _, err := netutil.Run(ctx, "tar", buildExportArgs(staged, s.configDir, backupExtraDirs)...); err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	defer func() {
		if _, err := netutil.Run(context.WithoutCancel(ctx), "rm", "-f", staged); err != nil {
			log.Printf("backup: remove staged archive: %v", err)
		}
	}()
	if _, err := netutil.Run(ctx, "chmod", "640", staged); err != nil {
		return fmt.Errorf("share staged archive: %w", err)
	}

	// staged is composed above from the fixed staging directory.
	// #nosec G304
	plaintext, err := os.ReadFile(staged)
	if err != nil {
		return fmt.Errorf("read archive for encryption: %w", err)
	}
	plaintext, err = appendConfigKey(plaintext)
	if err != nil {
		return fmt.Errorf("add credential key to backup: %w", err)
	}
	encrypted, err := encryptBackup(plaintext, passphrase)
	if err != nil {
		return fmt.Errorf("encrypt backup: %w", err)
	}

	// The taint is the exported parameter, not a request value. Every
	// caller builds the path itself from os.TempDir or os.CreateTemp.
	// #nosec G703
	if err := os.WriteFile(outputPath, encrypted, 0o600); err != nil {
		return fmt.Errorf("write encrypted backup: %w", err)
	}
	// WriteFile keeps the mode of a file that already existed.
	if err := os.Chmod(outputPath, 0o600); err != nil {
		return fmt.Errorf("restrict backup archive: %w", err)
	}
	return nil
}

// restoreRoots maps an archive's top-level directory name back to the
// path it was taken from.
//
// Export passes each source as its own "-C parent name" pair, so members
// are stored under a plain top-level name: lankeeper/..., unbound/...,
// openvpn/.... The table is derived from the same two inputs Export
// uses, so the two halves of the feature cannot drift apart again.
func restoreRoots(configDir string, extraDirs []string) map[string]string {
	roots := map[string]string{
		filepath.Base(configDir): configDir,
	}
	for _, dir := range extraDirs {
		roots[filepath.Base(dir)] = dir
	}
	return roots
}

// resolveRestoreTarget turns a cleaned archive member name into the
// absolute path it restores to, or reports that its top-level directory
// is not one this binary restores.
//
// The containment check is re-applied against the member's own root, not
// a single shared one, so a member cannot be written outside the
// directory its prefix claims.
func resolveRestoreTarget(roots map[string]string, clean string) (string, bool) {
	top := clean
	if i := strings.IndexRune(clean, os.PathSeparator); i >= 0 {
		top = clean[:i]
	}

	root, ok := roots[top]
	if !ok {
		return "", false
	}

	rest := strings.TrimPrefix(clean, top)
	rest = strings.TrimPrefix(rest, string(os.PathSeparator))

	target := root
	if rest != "" {
		target = filepath.Join(root, rest)
	}
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", false
	}
	return target, true
}

// Restore permissions are decided here rather than read from the tar
// header. An archive is untrusted input: it may have been edited on the
// operator's machine or fetched back from remote storage that was
// compromised, and only two of its fields, the member name and the
// content, describe something this binary cannot supply itself. Taking
// the mode as well let a tampered archive rewrite router.yaml
// world-writable, which turns a one-time restore into a standing local
// path onto the session secret and the admin password hash, outside the
// web authentication model entirely.
//
// Every archived path is read by a daemon that starts as root (unbound,
// dnsmasq and openvpn all read their configuration before dropping
// privileges) and none of them is an executable, so a single tight file
// mode serves all of them. It matches what the config writer already
// uses for router.yaml.
const (
	restoreFileMode os.FileMode = 0o600
	restoreDirMode  os.FileMode = 0o755
)

// Extraction bounds.
//
// The per-entry cap was the only size control, and nothing was tracked
// across iterations, so an archive of very many small and highly
// compressible entries was unbounded in total. Every member is written
// through the root agent, so that is an unbounded volume of privileged
// writes under the config directory from one upload.
//
// The figures are far above any real backup. The archive holds the
// config directory, the two DNS directories and the OpenVPN PKI: a
// deployment with hundreds of issued client certificates is still in the
// low thousands of entries, and the largest single file is a DNS
// blocklist.
const (
	maxImportEntryBytes int64 = 10 << 20
	maxImportTotalBytes int64 = 256 << 20
	maxImportEntries          = 10000
)

// importBudget tracks what one extraction has consumed. The limits are
// fields rather than constants read in place so the accounting can be
// exercised on its own: proving the cumulative cap through Import would
// mean actually compressing and extracting a quarter of a gigabyte.
type importBudget struct {
	maxEntries int
	maxEntry   int64
	maxTotal   int64

	entries int
	total   int64
}

func newImportBudget() *importBudget {
	return &importBudget{
		maxEntries: maxImportEntries,
		maxEntry:   maxImportEntryBytes,
		maxTotal:   maxImportTotalBytes,
	}
}

// countEntry records one archive member, whatever its kind. Directories
// and members this binary skips count too, so an archive padded with the
// cheapest possible entries is bounded as well.
func (b *importBudget) countEntry() error {
	b.entries++
	if b.entries > b.maxEntries {
		return fmt.Errorf("archive holds more than %d entries", b.maxEntries)
	}
	return nil
}

// countBytes records the extracted size of one regular file.
func (b *importBudget) countBytes(name string, n int64) error {
	if n > b.maxEntry {
		return fmt.Errorf("tar member %s exceeds the %d byte entry limit", name, b.maxEntry)
	}
	b.total += n
	if b.total > b.maxTotal {
		return fmt.Errorf("archive extracts more than %d bytes", b.maxTotal)
	}
	return nil
}

func (s *BackupService) Import(ctx context.Context, archivePath, passphrase string) error {
	if passphrase != "" {
		if err := decryptArchiveInPlace(archivePath, passphrase); err != nil {
			return err
		}
	}

	// archivePath is the temp file the upload handler created with
	// os.CreateTemp.
	// #nosec G304
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

	return s.restoreArchive(tar.NewReader(gz), passphrase != "")
}

// decryptArchiveInPlace replaces the uploaded archive with its
// plaintext.
func decryptArchiveInPlace(archivePath, passphrase string) error {
	// archivePath is the temp file the upload handler created with
	// os.CreateTemp.
	// #nosec G304
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	decrypted, err := decryptBackup(data, passphrase)
	if err != nil {
		return fmt.Errorf("decrypt backup: %w", err)
	}
	// Same temp file the handler created; the uploaded name never
	// reaches this path.
	// #nosec G703
	if err := os.WriteFile(archivePath, decrypted, 0o600); err != nil {
		return fmt.Errorf("write decrypted backup: %w", err)
	}
	return nil
}

// stagedMember is one archive member held in memory until the whole
// archive has been checked.
type stagedMember struct {
	target string
	isDir  bool
	data   []byte
}

// restoreArchive restores tr in two passes. The first reads every member
// into memory within the import budget and checks the archive as a
// whole, including the restored router.yaml against config.Validate;
// the second writes. A bad archive therefore leaves every live file as
// it was, instead of a mix of old and restored files, and a config the
// next start would refuse is never installed. The credential key is
// taken only from an encrypted archive and installed last.
func (s *BackupService) restoreArchive(tr *tar.Reader, encrypted bool) error {
	members, key, err := s.stageArchive(tr, encrypted)
	if err != nil {
		return err
	}
	if err := validateStagedConfig(members, s.configDir); err != nil {
		return err
	}
	for _, m := range members {
		if err := installMember(m); err != nil {
			return err
		}
	}
	return installArchivedKey(key)
}

// stageArchive reads every member of tr without writing anything.
func (s *BackupService) stageArchive(tr *tar.Reader, encrypted bool) ([]stagedMember, []byte, error) {
	roots := restoreRoots(s.configDir, backupExtraDirs)
	budget := newImportBudget()
	var members []stagedMember
	var key []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return members, key, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read tar header: %w", err)
		}
		if filepath.Clean(hdr.Name) == archiveKeyMember {
			if key, err = readArchivedKey(tr, hdr, encrypted, budget); err != nil {
				return nil, nil, err
			}
			continue
		}
		m, err := stageMember(tr, hdr, roots, budget)
		if err != nil {
			return nil, nil, err
		}
		if m != nil {
			members = append(members, *m)
		}
	}
}

// validateStagedConfig refuses an archive whose router.yaml the next
// start would refuse. serve stops on an invalid config, and the web UI is
// the only interface that could correct it.
func validateStagedConfig(members []stagedMember, configDir string) error {
	cfgPath := filepath.Join(configDir, "router.yaml")
	for _, m := range members {
		if m.isDir || m.target != cfgPath {
			continue
		}
		if err := config.ValidateBytes(m.data); err != nil {
			return fmt.Errorf("archived router.yaml: %w", err)
		}
	}
	return nil
}

// installMember writes one staged member to its live path.
func installMember(m stagedMember) error {
	if m.isDir {
		if err := netutil.MkdirAll(m.target, restoreDirMode); err != nil {
			return fmt.Errorf("mkdir %s: %w", m.target, err)
		}
		return nil
	}
	if err := netutil.WriteFile(m.target, m.data, restoreFileMode); err != nil {
		return fmt.Errorf("write %s: %w", m.target, err)
	}
	return nil
}

// stageMember checks one archive member and reads it, or returns nil for
// a member this binary does not restore.
func stageMember(tr io.Reader, hdr *tar.Header, roots map[string]string, budget *importBudget) (*stagedMember, error) {
	// Counted before the skip below, so an archive padded with entries
	// this binary does not restore is bounded too.
	if err := budget.countEntry(); err != nil {
		return nil, err
	}

	clean := filepath.Clean(hdr.Name)
	if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return nil, fmt.Errorf("unsafe tar member rejected: %s", hdr.Name)
	}

	target, ok := resolveRestoreTarget(roots, clean)
	if !ok {
		// An archive from a newer release may carry a directory this
		// binary knows nothing about. Skipping leaves it unwritten, which
		// is harmless, whereas failing would make that archive entirely
		// unrestorable here.
		log.Printf("backup: skipping unknown archive entry %s", hdr.Name)
		return nil, nil
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		return &stagedMember{target: target, isDir: true}, nil
	case tar.TypeReg:
		data, err := readMember(tr, hdr.Name, budget)
		if err != nil {
			return nil, err
		}
		return &stagedMember{target: target, data: data}, nil
	default:
		return nil, fmt.Errorf("unsupported tar member type %d: %s", hdr.Typeflag, hdr.Name)
	}
}

// readMember reads one regular member within the budget.
//
// It reads one byte past the cap, so an oversized member is detected
// rather than silently truncated. A plain LimitReader at the cap returns
// exactly the cap with a nil error, and the member was written short with
// nothing reported: a restored blocklist would come back cut off and look
// restored.
func readMember(tr io.Reader, name string, budget *importBudget) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(tr, maxImportEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read tar member %s: %w", name, err)
	}
	if err := budget.countBytes(name, int64(len(data))); err != nil {
		return nil, err
	}
	return data, nil
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

// archiveKeyMember is where an encrypted export stores the credential
// key. Its top-level name matches no restore root, so an older binary
// skips it instead of writing it somewhere.
const archiveKeyMember = "credentials/config.key"

// maxArchivedKeyBytes bounds the key member: 64 hex characters.
const maxArchivedKeyBytes = 128

// appendConfigKey returns archive with the credential key added as
// archiveKeyMember. Without a key file there is no ciphertext in the
// config to decrypt, so the archive is returned unchanged.
func appendConfigKey(archive []byte) ([]byte, error) {
	key, err := os.ReadFile(config.ConfigKeyPath())
	if errors.Is(err, os.ErrNotExist) {
		return archive, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential key: %w", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	if err := copyTarEntries(tw, tar.NewReader(gz)); err != nil {
		return nil, err
	}
	hdr := &tar.Header{Name: archiveKeyMember, Mode: 0o600, Size: int64(len(key)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, fmt.Errorf("write key header: %w", err)
	}
	if _, err := tw.Write(key); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return out.Bytes(), nil
}

// copyTarEntries copies every member of tr into tw unchanged.
func copyTarEntries(tw *tar.Writer, tr *tar.Reader) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("copy tar header %s: %w", hdr.Name, err)
		}
		// The archive was just written by tar from local directories.
		// #nosec G110
		if _, err := io.Copy(tw, tr); err != nil {
			return fmt.Errorf("copy tar member %s: %w", hdr.Name, err)
		}
	}
}

// readArchivedKey reads the key member. A plain archive never carries
// the key, so one that does was not written by Export and is refused.
func readArchivedKey(tr io.Reader, hdr *tar.Header, encrypted bool, budget *importBudget) ([]byte, error) {
	if err := budget.countEntry(); err != nil {
		return nil, err
	}
	if !encrypted {
		return nil, errors.New("unencrypted archive carries a credential key")
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("credential key member has type %d", hdr.Typeflag)
	}
	key, err := io.ReadAll(io.LimitReader(tr, maxArchivedKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read credential key: %w", err)
	}
	if len(key) > maxArchivedKeyBytes {
		return nil, errors.New("credential key member is too large")
	}
	return key, nil
}

// installArchivedKey writes the key from the archive, if it had one.
// It runs in this process, not through the agent, so the file stays
// owned by the service account that has to read it at startup.
func installArchivedKey(key []byte) error {
	if key == nil {
		return nil
	}
	if err := config.RestoreConfigKey(key); err != nil {
		return fmt.Errorf("restore credential key: %w", err)
	}
	return nil
}
