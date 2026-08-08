package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Encryption at rest for the third-party credentials in router.yaml.
//
// What this protects against, stated plainly: the key lives outside the
// config directory and is never included in a backup archive, so a
// config file copied on its own, shared for debugging, or carried
// off-box inside an export no longer hands over the operator's object
// storage key, their SFTP password, or the passphrase that decrypts
// every stored archive.
//
// What it does not protect against: anyone who can read both the config
// and the key. That means root, the service account itself, and a stolen
// disk. Those are outside what a file-level scheme on a single appliance
// can address.
const (
	// secretPrefix marks a value as ciphertext. Its presence, not the
	// field name, decides whether a value is decrypted on load, so a
	// config written before this existed still loads as cleartext and
	// is encrypted by the next save.
	secretPrefix = "enc:v1:"

	defaultConfigKeyPath = "/var/lib/lankeeper/credentials/config.key"
)

// configKeyPath resolves the key location. The environment override
// exists for tests, which cannot write under /var/lib.
func configKeyPath() string {
	if p := os.Getenv("LANKEEPER_CONFIG_KEY"); p != "" {
		return p
	}
	return defaultConfigKeyPath
}

// loadOrCreateConfigKey returns the encryption key, generating and
// persisting one on first use. The key directory is created 0700 and the
// key file 0600, both owned by whoever runs the process, which the
// installers set to the service account.
func loadOrCreateConfigKey() ([]byte, error) {
	path := configKeyPath()

	key, err := LoadKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key, err = GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	if err := SaveKey(path, key); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict key file: %w", err)
	}

	log.Printf("config: generated a new credential encryption key at %s", path)
	return key, nil
}

// loadConfigKeyForRead returns the key without creating one. A caller
// that has ciphertext to read and no key cannot recover the value, and
// minting a fresh key here would only produce a key that decrypts
// nothing.
func loadConfigKeyForRead() ([]byte, error) {
	return LoadKey(configKeyPath())
}

func isEncrypted(value string) bool {
	return strings.HasPrefix(value, secretPrefix)
}

// encryptSecret returns the marked, base64-encoded ciphertext. An empty
// value stays empty: there is nothing to protect and an encrypted empty
// string would still reveal that the field is set.
func encryptSecret(plaintext string, key []byte) (string, error) {
	if plaintext == "" || isEncrypted(plaintext) {
		return plaintext, nil
	}
	ciphertext, err := Encrypt([]byte(plaintext), key)
	if err != nil {
		return "", err
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptSecret reverses encryptSecret. A value without the marker is
// returned unchanged, which is how a config written before encryption
// existed keeps working.
func decryptSecret(value string, key []byte) (string, error) {
	if !isEncrypted(value) {
		return value, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("decode secret: %w", err)
	}
	plaintext, err := Decrypt(raw, key)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// encryptedSecrets reports whether the config holds any value that needs
// the key. Checked before touching the key so an appliance that never
// configures a backup target never creates one.
func (c *Config) hasSecrets() bool {
	if c.Backup.Passphrase != "" {
		return true
	}
	for _, t := range c.Backup.Targets {
		if t.SecretAccessKey != "" || t.Password != "" {
			return true
		}
	}
	if c.VPN.Server.PrivateKey != "" {
		return true
	}
	for _, p := range c.VPN.Server.Peers {
		if p.PrivateKey != "" {
			return true
		}
	}
	return false
}

// withEncryptedSecrets returns a copy of the config whose secret fields
// carry ciphertext, leaving the caller's live config untouched. Copying
// matters: encrypting in place would leave the running process holding
// ciphertext where it expects an S3 key.
func withEncryptedSecrets(cfg *Config) (*Config, error) {
	if !cfg.hasSecrets() {
		return cfg, nil
	}

	key, err := loadOrCreateConfigKey()
	if err != nil {
		return nil, fmt.Errorf("credential encryption key: %w", err)
	}

	out := *cfg
	out.Backup.Passphrase, err = encryptSecret(cfg.Backup.Passphrase, key)
	if err != nil {
		return nil, fmt.Errorf("encrypt backup passphrase: %w", err)
	}

	// The slice header is shared by the shallow copy, so the elements
	// have to be copied before any field is rewritten.
	out.Backup.Targets = make([]BackupTarget, len(cfg.Backup.Targets))
	copy(out.Backup.Targets, cfg.Backup.Targets)
	for i := range out.Backup.Targets {
		t := &out.Backup.Targets[i]
		if t.SecretAccessKey, err = encryptSecret(t.SecretAccessKey, key); err != nil {
			return nil, fmt.Errorf("encrypt secret access key for target %q: %w", t.Name, err)
		}
		if t.Password, err = encryptSecret(t.Password, key); err != nil {
			return nil, fmt.Errorf("encrypt password for target %q: %w", t.Name, err)
		}
	}

	out.VPN.Server.PrivateKey, err = encryptSecret(cfg.VPN.Server.PrivateKey, key)
	if err != nil {
		return nil, fmt.Errorf("encrypt wireguard server private key: %w", err)
	}

	// Peers sit two levels down, but the copy is just as shallow: the
	// slice inside out.VPN.Server still points at the caller's backing
	// array, so rewriting a peer in place would leave the running
	// process holding ciphertext where it expects a usable key.
	out.VPN.Server.Peers = make([]WGServerPeer, len(cfg.VPN.Server.Peers))
	copy(out.VPN.Server.Peers, cfg.VPN.Server.Peers)
	for i := range out.VPN.Server.Peers {
		p := &out.VPN.Server.Peers[i]
		if p.PrivateKey, err = encryptSecret(p.PrivateKey, key); err != nil {
			return nil, fmt.Errorf("encrypt private key for peer %q: %w", p.Name, err)
		}
	}

	return &out, nil
}

// decryptSecretsInPlace turns the ciphertext read from disk back into
// usable credentials.
//
// A value that cannot be decrypted is cleared and reported rather than
// aborting the load. This process is the router: refusing to start would
// take DNS, DHCP and the firewall down over a lost backup credential.
// The cleared field surfaces on the backup page and makes the next
// scheduled run fail with a message naming the target, so the loss is
// visible without being fatal.
func (c *Config) decryptSecretsInPlace() {
	needsKey := isEncrypted(c.Backup.Passphrase) || isEncrypted(c.VPN.Server.PrivateKey)
	for _, t := range c.Backup.Targets {
		if isEncrypted(t.SecretAccessKey) || isEncrypted(t.Password) {
			needsKey = true
		}
	}
	for _, p := range c.VPN.Server.Peers {
		if isEncrypted(p.PrivateKey) {
			needsKey = true
		}
	}
	if !needsKey {
		return
	}

	key, err := loadConfigKeyForRead()
	if err != nil {
		log.Printf("config: cannot read the credential encryption key (%v); "+
			"stored backup credentials are unavailable and must be re-entered", err)
		c.clearEncryptedSecrets()
		return
	}

	if v, err := decryptSecret(c.Backup.Passphrase, key); err != nil {
		log.Printf("config: cannot decrypt the backup passphrase: %v; re-enter it on the backup page", err)
		c.Backup.Passphrase = ""
	} else {
		c.Backup.Passphrase = v
	}

	for i := range c.Backup.Targets {
		t := &c.Backup.Targets[i]
		if v, err := decryptSecret(t.SecretAccessKey, key); err != nil {
			log.Printf("config: cannot decrypt the secret access key for target %q: %v; re-enter it on the backup page", t.Name, err)
			t.SecretAccessKey = ""
		} else {
			t.SecretAccessKey = v
		}
		if v, err := decryptSecret(t.Password, key); err != nil {
			log.Printf("config: cannot decrypt the password for target %q: %v; re-enter it on the backup page", t.Name, err)
			t.Password = ""
		} else {
			t.Password = v
		}
	}

	// A lost server key is worse than a lost peer key: without it
	// wg-quick cannot bring the interface up at all, so it is named
	// separately rather than folded into the peer loop.
	if v, err := decryptSecret(c.VPN.Server.PrivateKey, key); err != nil {
		log.Printf("config: cannot decrypt the wireguard server private key: %v; the VPN server cannot start until it is regenerated", err)
		c.VPN.Server.PrivateKey = ""
	} else {
		c.VPN.Server.PrivateKey = v
	}

	for i := range c.VPN.Server.Peers {
		p := &c.VPN.Server.Peers[i]
		if v, err := decryptSecret(p.PrivateKey, key); err != nil {
			// The peer keeps working: the server only needs its public
			// key. What is lost is the ability to hand the operator the
			// peer's config again, which the page reports rather than
			// hiding.
			log.Printf("config: cannot decrypt the private key for peer %q: %v; its config can no longer be re-issued", p.Name, err)
			p.PrivateKey = ""
		} else {
			p.PrivateKey = v
		}
	}
}

// clearEncryptedSecrets blanks every value that is ciphertext we cannot
// read. Cleartext values from a config written before encryption existed
// are left alone, since those are still usable.
func (c *Config) clearEncryptedSecrets() {
	if isEncrypted(c.Backup.Passphrase) {
		c.Backup.Passphrase = ""
	}
	for i := range c.Backup.Targets {
		t := &c.Backup.Targets[i]
		if isEncrypted(t.SecretAccessKey) {
			t.SecretAccessKey = ""
		}
		if isEncrypted(t.Password) {
			t.Password = ""
		}
	}
	if isEncrypted(c.VPN.Server.PrivateKey) {
		c.VPN.Server.PrivateKey = ""
	}
	for i := range c.VPN.Server.Peers {
		if isEncrypted(c.VPN.Server.Peers[i].PrivateKey) {
			c.VPN.Server.Peers[i].PrivateKey = ""
		}
	}
}
