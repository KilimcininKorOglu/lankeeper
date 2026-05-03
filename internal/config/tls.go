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
	certDir := filepath.Join(dataDir, "tls")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return nil, fmt.Errorf("create tls dir: %w", err)
	}

	certPath := filepath.Join(certDir, "server.crt")
	keyPath := filepath.Join(certDir, "server.key")

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		certPath = cfg.CertFile
		keyPath = cfg.KeyFile
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
			Organization: []string{"Home Router"},
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
	os.Chmod(keyPath, 0o600)

	return readCertInfo(certPath, keyPath)
}

func readCertInfo(certPath, keyPath string) (*TLSCertInfo, error) {
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
