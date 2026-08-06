package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// secretsEnv points the key at a temp file. Without it the package
// default is /var/lib/lankeeper, which a test must never touch.
func secretsEnv(t *testing.T) (cfgPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "credentials", "config.key")
	t.Setenv("LANKEEPER_CONFIG_KEY", keyPath)
	return filepath.Join(dir, "router.yaml"), keyPath
}

func backupConfigWithSecrets(path string) *config.Config {
	cfg := &config.Config{}
	cfg.SetFilePath(path)
	cfg.Backup.Enabled = true
	cfg.Backup.Schedule = "@daily"
	cfg.Backup.Passphrase = "master-passphrase"
	cfg.Backup.Targets = []config.BackupTarget{
		{Type: "s3", Name: "offsite", Bucket: "b", AccessKeyID: "AKIA", SecretAccessKey: "s3-secret-key"},
		{Type: "sftp", Name: "nas", Host: "nas.lan", User: "u", Password: "sftp-password"},
	}
	return cfg
}

// TestSaveEncryptsBackupSecretsOnDisk is the regression test. The struct
// comment claimed these fields were AES-encrypted before SaveToFile, but
// Save marshalled the struct as-is, so router.yaml held the operator's
// object-storage key, their SFTP password, and the passphrase that
// decrypts every stored archive in the clear.
func TestSaveEncryptsBackupSecretsOnDisk(t *testing.T) {
	cfgPath, _ := secretsEnv(t)
	cfg := backupConfigWithSecrets(cfgPath)

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, secret := range []string{"master-passphrase", "s3-secret-key", "sftp-password"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q is on disk in the clear", secret)
		}
	}
	if n := strings.Count(string(raw), "enc:v1:"); n != 3 {
		t.Errorf("found %d encrypted values, want 3:\n%s", n, raw)
	}

	// Non-secret fields must stay readable, since operators edit them.
	if !strings.Contains(string(raw), "nas.lan") {
		t.Error("the SFTP host was encrypted; only the credentials should be")
	}

	// The live config must keep the usable values: encrypting in place
	// would leave the running process using ciphertext as its S3 key.
	if cfg.Backup.Passphrase != "master-passphrase" {
		t.Errorf("in-memory passphrase became %q", cfg.Backup.Passphrase)
	}
	if cfg.Backup.Targets[0].SecretAccessKey != "s3-secret-key" {
		t.Errorf("in-memory secret access key became %q", cfg.Backup.Targets[0].SecretAccessKey)
	}
	if cfg.Backup.Targets[1].Password != "sftp-password" {
		t.Errorf("in-memory SFTP password became %q", cfg.Backup.Targets[1].Password)
	}
}

func TestLoadDecryptsBackupSecrets(t *testing.T) {
	cfgPath, _ := secretsEnv(t)
	if err := backupConfigWithSecrets(cfgPath).SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Backup.Passphrase != "master-passphrase" {
		t.Errorf("passphrase = %q", loaded.Backup.Passphrase)
	}
	if loaded.Backup.Targets[0].SecretAccessKey != "s3-secret-key" {
		t.Errorf("secret access key = %q", loaded.Backup.Targets[0].SecretAccessKey)
	}
	if loaded.Backup.Targets[1].Password != "sftp-password" {
		t.Errorf("sftp password = %q", loaded.Backup.Targets[1].Password)
	}
}

// TestLoadAcceptsLegacyCleartextThenEncrypts covers the upgrade path: a
// router.yaml written before this existed has no marker on its values
// and has to keep working, then move to ciphertext on the next save.
func TestLoadAcceptsLegacyCleartextThenEncrypts(t *testing.T) {
	cfgPath, _ := secretsEnv(t)
	legacy := "" +
		"backup:\n" +
		"  enabled: true\n" +
		"  passphrase: legacy-passphrase\n" +
		"  targets:\n" +
		"    - type: s3\n" +
		"      name: offsite\n" +
		"      secretAccessKey: legacy-s3-key\n"
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if loaded.Backup.Passphrase != "legacy-passphrase" {
		t.Fatalf("legacy passphrase not usable: %q", loaded.Backup.Passphrase)
	}
	if loaded.Backup.Targets[0].SecretAccessKey != "legacy-s3-key" {
		t.Fatalf("legacy secret key not usable: %q", loaded.Backup.Targets[0].SecretAccessKey)
	}

	if err := loaded.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "legacy-passphrase") || strings.Contains(string(raw), "legacy-s3-key") {
		t.Errorf("the legacy values were rewritten in the clear:\n%s", raw)
	}
}

// TestLoadClearsSecretsItCannotDecrypt pins the chosen failure mode. The
// router must still start without its backup credentials: refusing to
// load would take DNS, DHCP and the firewall down over a lost key.
func TestLoadClearsSecretsItCannotDecrypt(t *testing.T) {
	cfgPath, keyPath := secretsEnv(t)
	if err := backupConfigWithSecrets(cfgPath).SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load must not fail when the key is gone: %v", err)
	}
	if loaded.Backup.Passphrase != "" {
		t.Errorf("passphrase = %q, want it cleared", loaded.Backup.Passphrase)
	}
	if loaded.Backup.Targets[0].SecretAccessKey != "" {
		t.Errorf("secret access key = %q, want it cleared", loaded.Backup.Targets[0].SecretAccessKey)
	}
	if loaded.Backup.Targets[1].Password != "" {
		t.Errorf("sftp password = %q, want it cleared", loaded.Backup.Targets[1].Password)
	}

	// Everything that is not a secret has to survive.
	if loaded.Backup.Schedule != "@daily" || loaded.Backup.Targets[1].Host != "nas.lan" {
		t.Error("non-secret backup settings were lost along with the credentials")
	}
}

// TestLoadClearsSecretsUnderTheWrongKey covers a key that exists but
// does not match, which is what a restored config from another
// appliance looks like.
func TestLoadClearsSecretsUnderTheWrongKey(t *testing.T) {
	cfgPath, keyPath := secretsEnv(t)
	if err := backupConfigWithSecrets(cfgPath).SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	other, err := config.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := config.SaveKey(keyPath, other); err != nil {
		t.Fatalf("save key: %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Backup.Passphrase != "" || loaded.Backup.Targets[0].SecretAccessKey != "" {
		t.Error("a value decrypted under the wrong key, which cannot be right")
	}
}

func TestKeyFileIsOwnerReadableOnly(t *testing.T) {
	cfgPath, keyPath := secretsEnv(t)
	if err := backupConfigWithSecrets(cfgPath).SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("key file mode = %o, want 600", got)
	}

	dir, err := os.Stat(filepath.Dir(keyPath))
	if err != nil {
		t.Fatalf("stat key dir: %v", err)
	}
	if got := dir.Mode().Perm(); got != 0o700 {
		t.Errorf("key directory mode = %o, want 700", got)
	}
}

// TestSaveWithoutSecretsCreatesNoKey keeps an appliance that never
// configures a backup from growing a key file it has no use for.
func TestSaveWithoutSecretsCreatesNoKey(t *testing.T) {
	cfgPath, keyPath := secretsEnv(t)
	cfg := &config.Config{}
	cfg.SetFilePath(cfgPath)
	cfg.System.Hostname = "router"

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("a key was created for a config with no secrets")
	}
}
