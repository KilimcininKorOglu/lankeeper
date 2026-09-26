package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Encryption at rest for the credentials and keys in router.yaml.
//
// What this protects against, stated plainly: the key lives outside the
// config directory and is never included in an unencrypted backup
// archive, so a config file copied on its own, shared for debugging, or
// carried off-box inside a plain export no longer hands over the
// operator's object storage key, their SFTP password, or the passphrase
// that decrypts every stored archive. A passphrase-encrypted export does
// carry the key, because without it a restore onto new hardware cannot
// decrypt the WireGuard keys and every other secret; the passphrase is
// what protects it there.
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

// ConfigKeyPath is where the credential encryption key lives.
func ConfigKeyPath() string {
	return configKeyPath()
}

// RestoreConfigKey installs a key taken from a backup archive, so the
// restored router.yaml can be decrypted on the next start. The value is
// the hex encoding SaveKey writes, and is checked the same way LoadKey
// checks it before anything is written.
func RestoreConfigKey(encoded []byte) error {
	key, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return fmt.Errorf("decode archived key: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("archived key has length %d, want 32", len(key))
	}
	path := configKeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	if err := SaveKey(path, key); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
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

// secretFields returns a pointer to every secret field of c. It is the
// single list encryption, detection and clearing work from; a new
// credential goes here and gets a log line in decryptSecretsInPlace.
func (c *Config) secretFields() []*string {
	fields := []*string{
		&c.Backup.Passphrase,
		&c.VPN.Server.PrivateKey,
		&c.System.SessionSecret,
		&c.System.TLS.ACME.DNSChallenge.APIToken,
	}
	for i := range c.Backup.Targets {
		fields = append(fields, &c.Backup.Targets[i].SecretAccessKey, &c.Backup.Targets[i].Password)
	}
	for i := range c.VPN.Server.Peers {
		fields = append(fields, &c.VPN.Server.Peers[i].PrivateKey, &c.VPN.Server.Peers[i].PresharedKey)
	}
	for i := range c.VPN.Clients {
		fields = append(fields, &c.VPN.Clients[i].PrivateKey, &c.VPN.Clients[i].PresharedKey)
	}
	return fields
}

// hasSecrets reports whether the config holds any value that needs the
// key. Checked before touching the key so an appliance with no secret
// configured never creates one.
func (c *Config) hasSecrets() bool {
	return slices.ContainsFunc(c.secretFields(), func(f *string) bool { return *f != "" })
}

// withEncryptedSecrets returns a copy of the config whose secret fields
// carry ciphertext, leaving the caller's live config untouched. Copying
// matters: encrypting in place would leave the running process holding
// ciphertext where it expects an S3 key. The slices holding secrets are
// cloned too, since the shallow copy still shares their backing arrays.
func withEncryptedSecrets(cfg *Config) (*Config, error) {
	if !cfg.hasSecrets() {
		return cfg, nil
	}

	key, err := loadOrCreateConfigKey()
	if err != nil {
		return nil, fmt.Errorf("credential encryption key: %w", err)
	}

	out := *cfg
	out.Backup.Targets = slices.Clone(cfg.Backup.Targets)
	out.VPN.Server.Peers = slices.Clone(cfg.VPN.Server.Peers)
	out.VPN.Clients = slices.Clone(cfg.VPN.Clients)
	for _, f := range out.secretFields() {
		if *f, err = encryptSecret(*f, key); err != nil {
			return nil, fmt.Errorf("encrypt secret: %w", err)
		}
	}
	return &out, nil
}

// hasEncryptedSecrets reports whether any secret field holds ciphertext,
// which is the only case that needs the key on load.
func (c *Config) hasEncryptedSecrets() bool {
	return slices.ContainsFunc(c.secretFields(), func(f *string) bool { return isEncrypted(*f) })
}

// decryptInPlace replaces *field with its plaintext. On failure it clears
// the field and logs what was lost and what the operator has to do.
func decryptInPlace(field *string, key []byte, what, consequence string) {
	v, err := decryptSecret(*field, key)
	if err != nil {
		log.Printf("config: cannot decrypt %s: %v; %s", what, err, consequence)
		*field = ""
		return
	}
	*field = v
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
	if !c.hasEncryptedSecrets() {
		return
	}

	key, err := loadConfigKeyForRead()
	if err != nil {
		log.Printf("config: cannot read the credential encryption key (%v); "+
			"stored backup credentials are unavailable and must be re-entered", err)
		c.clearEncryptedSecrets()
		return
	}

	const reenterBackup = "re-enter it on the backup page"
	decryptInPlace(&c.Backup.Passphrase, key, "the backup passphrase", reenterBackup)

	for i := range c.Backup.Targets {
		t := &c.Backup.Targets[i]
		decryptInPlace(&t.SecretAccessKey, key,
			fmt.Sprintf("the secret access key for target %q", t.Name), reenterBackup)
		decryptInPlace(&t.Password, key,
			fmt.Sprintf("the password for target %q", t.Name), reenterBackup)
	}

	// A lost server key is worse than a lost peer key: without it
	// wg-quick cannot bring the interface up at all, so it is named
	// separately rather than folded into the peer loop.
	decryptInPlace(&c.VPN.Server.PrivateKey, key, "the wireguard server private key",
		"the VPN server cannot start until it is regenerated")

	for i := range c.VPN.Server.Peers {
		p := &c.VPN.Server.Peers[i]
		// The peer keeps working: the server only needs its public key.
		// What is lost is the ability to hand the operator the peer's
		// config again, which the page reports rather than hiding.
		decryptInPlace(&p.PrivateKey, key, fmt.Sprintf("the private key for peer %q", p.Name),
			"its config can no longer be re-issued")
	}
	c.decryptVPNPeerSecrets(key)

	// A cleared secret makes serve generate a new one, which signs the
	// operator out and nothing more.
	decryptInPlace(&c.System.SessionSecret, key, "the session secret",
		"a new one is generated and every session must log in again")

	// Renewal is what breaks: the token is only read when a challenge
	// record has to be published. The certificate on disk keeps serving
	// until it expires, so this is reported and the field cleared rather
	// than treated as fatal.
	decryptInPlace(&c.System.TLS.ACME.DNSChallenge.APIToken, key, "the DNS challenge API token",
		"re-enter it on the settings page or renewal will fail")
}

// decryptVPNPeerSecrets decrypts the preshared keys of server peers and
// the keys of outbound client tunnels.
func (c *Config) decryptVPNPeerSecrets(key []byte) {
	for i := range c.VPN.Server.Peers {
		p := &c.VPN.Server.Peers[i]
		decryptInPlace(&p.PresharedKey, key, fmt.Sprintf("the preshared key for peer %q", p.Name),
			"the peer cannot connect until it is re-created")
	}
	for i := range c.VPN.Clients {
		cl := &c.VPN.Clients[i]
		decryptInPlace(&cl.PrivateKey, key, fmt.Sprintf("the private key for client tunnel %q", cl.Name),
			"re-enter the tunnel on the VPN page")
		decryptInPlace(&cl.PresharedKey, key, fmt.Sprintf("the preshared key for client tunnel %q", cl.Name),
			"re-enter the tunnel on the VPN page")
	}
}

// clearEncryptedSecrets blanks every value that is ciphertext we cannot
// read. Cleartext values from a config written before encryption existed
// are left alone, since those are still usable.
func (c *Config) clearEncryptedSecrets() {
	for _, f := range c.secretFields() {
		if isEncrypted(*f) {
			*f = ""
		}
	}
}
