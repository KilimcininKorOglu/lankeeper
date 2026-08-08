package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type TLSCertInfo struct {
	CertPath  string
	KeyPath   string
	NotBefore time.Time
	NotAfter  time.Time
	Issuer    string
	SANs      []string
}

func EnsureTLSCert(cfg *TLSConfig, dataDir string) (*TLSCertInfo, error) {
	certPath, keyPath, err := certPaths(cfg, dataDir)
	if err != nil {
		return nil, err
	}

	switch cfg.Mode {
	case "self-signed", "":
		return ensureSelfSigned(cfg, certPath, keyPath)
	case "mkcert":
		if _, err := os.Stat(certPath); err == nil {
			return readCertInfo(certPath, keyPath)
		}
		return nil, fmt.Errorf("mkcert certificates not found at %s; run 'mkcert' to generate", certPath)
	case "acme":
		if _, err := os.Stat(certPath); err == nil {
			return readCertInfo(certPath, keyPath)
		}
		return nil, fmt.Errorf("ACME certificates not found at %s; configure DNS-01 challenge", certPath)
	default:
		return nil, fmt.Errorf("unknown TLS mode: %s", cfg.Mode)
	}
}

// RegenerateSelfSigned issues a new pair unconditionally, replacing
// whatever is on disk.
//
// EnsureTLSCert deliberately keeps an unexpired certificate, which is
// right on every boot and wrong when the operator has just changed the
// name or the SAN list: the settings they submitted would be persisted
// and then ignored until the old certificate happened to expire.
func RegenerateSelfSigned(cfg *TLSConfig, dataDir string) (*TLSCertInfo, error) {
	certPath, keyPath, err := certPaths(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	return generateSelfSigned(cfg, certPath, keyPath)
}

// ReadTLSCertInfo reports the certificate on disk without creating one.
//
// The settings page needs to be able to say "no certificate yet", and
// EnsureTLSCert cannot answer that: in self-signed mode it generates,
// so merely rendering the page would have produced a key pair.
func ReadTLSCertInfo(cfg *TLSConfig, dataDir string) (*TLSCertInfo, error) {
	certPath, keyPath, err := certPaths(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	return readCertInfo(certPath, keyPath)
}

// InstallTLSPair puts a certificate issued elsewhere into the serving
// location and reports what it contains.
//
// The PEM is parsed before either file is written. A caller that hands
// over an error message, an empty buffer or a half-downloaded response
// gets a failure here, while the pair already in place keeps serving.
// Discovering the same thing at bind time instead means the service is
// already down.
func InstallTLSPair(cfg *TLSConfig, dataDir string, certPEM, keyPEM []byte) (*TLSCertInfo, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("decode certificate PEM: no valid block found")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if keyBlock, _ := pem.Decode(keyPEM); keyBlock == nil {
		return nil, fmt.Errorf("decode key PEM: no valid block found")
	}

	certPath, keyPath, err := certPaths(cfg, dataDir)
	if err != nil {
		return nil, err
	}

	// The key first. If the certificate write fails after it, the pair
	// on disk is stale-cert plus new-key and the service refuses to
	// start, which is loud. The other order leaves new-cert plus
	// stale-key, which also refuses to start but looks like the install
	// worked.
	if err := atomicWrite(keyPath, keyPEM); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return nil, fmt.Errorf("chmod key: %w", err)
	}
	if err := atomicWrite(certPath, certPEM); err != nil {
		return nil, fmt.Errorf("write cert: %w", err)
	}

	return readCertInfo(certPath, keyPath)
}

// certPaths resolves where the pair lives, creating the directory but
// not the certificate.
func certPaths(cfg *TLSConfig, dataDir string) (certPath, keyPath string, err error) {
	certDir := filepath.Join(dataDir, "tls")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create tls dir: %w", err)
	}
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		return cfg.CertFile, cfg.KeyFile, nil
	}
	return filepath.Join(certDir, "server.crt"), filepath.Join(certDir, "server.key"), nil
}

func ensureSelfSigned(cfg *TLSConfig, certPath, keyPath string) (*TLSCertInfo, error) {
	if _, err := os.Stat(certPath); err == nil {
		info, err := readCertInfo(certPath, keyPath)
		if err == nil && time.Now().Before(info.NotAfter) {
			return info, nil
		}
	}

	return generateSelfSigned(cfg, certPath, keyPath)
}

func generateSelfSigned(cfg *TLSConfig, certPath, keyPath string) (*TLSCertInfo, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	cn := cfg.SelfSigned.CN
	if cn == "" {
		cn = "hermes.lan"
	}

	validDays := cfg.SelfSigned.ValidDays
	if validDays <= 0 {
		validDays = 3650
	}

	notBefore := time.Now()
	notAfter := notBefore.AddDate(0, 0, validDays)

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{"LANKeeper"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, san := range cfg.SelfSigned.SANs {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}

	if len(template.DNSNames) == 0 && len(template.IPAddresses) == 0 {
		template.DNSNames = []string{cn, "localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		if localIPs, err := net.InterfaceAddrs(); err == nil {
			for _, addr := range localIPs {
				if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
					template.IPAddresses = append(template.IPAddresses, ipNet.IP)
				}
			}
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := atomicWrite(certPath, certPEM); err != nil {
		return nil, fmt.Errorf("write cert: %w", err)
	}
	if err := atomicWrite(keyPath, keyPEM); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return nil, fmt.Errorf("chmod key: %w", err)
	}

	return readCertInfo(certPath, keyPath)
}

func readCertInfo(certPath, keyPath string) (*TLSCertInfo, error) {
	// certPath comes from the parsed config, not from a request.
	// #nosec G304
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("decode PEM: no valid block found")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}

	sans := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses))
	sans = append(sans, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		sans = append(sans, ip.String())
	}

	return &TLSCertInfo{
		CertPath:  certPath,
		KeyPath:   keyPath,
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		Issuer:    cert.Issuer.CommonName,
		SANs:      sans,
	}, nil
}
