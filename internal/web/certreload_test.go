package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestPair(t *testing.T, certFile, keyFile, cn string, mod time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

func servedCN(t *testing.T, r *certReloader) string {
	t.Helper()
	c, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}

// A background ACME renewal replaces the files on disk; the listener has
// to serve the new pair without a restart.
func TestCertReloaderServesARenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writeTestPair(t, certFile, keyFile, "old", time.Now().Add(-time.Hour))

	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := servedCN(t, r); got != "old" {
		t.Fatalf("served %q, want old", got)
	}

	writeTestPair(t, certFile, keyFile, "renewed", time.Now())
	r.lastCheck = time.Time{}
	if got := servedCN(t, r); got != "renewed" {
		t.Fatalf("served %q after renewal, want renewed", got)
	}
}

func TestCertReloaderKeepsThePairWhenTheNewOneIsBroken(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writeTestPair(t, certFile, keyFile, "old", time.Now().Add(-time.Hour))
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(certFile, []byte("half-written"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.lastCheck = time.Time{}
	if got := servedCN(t, r); got != "old" {
		t.Fatalf("served %q after a broken write, want old", got)
	}
}
