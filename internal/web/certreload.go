package web

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// certReloader serves the certificate currently on disk.
//
// ListenAndServeTLS(certFile, keyFile) loads the pair once, so an ACME
// renewal that replaced the files in the background went unnoticed until
// the next restart and the UI kept serving the old certificate past its
// expiry. The pair is re-read when either file's modification time
// changes, checked at most once per reloadCheckInterval.
type certReloader struct {
	certFile, keyFile string

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

const reloadCheckInterval = time.Minute

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// load reads the pair and records the modification times it was read at.
func (r *certReloader) load() error {
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS key pair: %w", err)
	}
	r.cert, r.certMod, r.keyMod = &cert, certMod, keyMod
	return nil
}

func (r *certReloader) modTimes() (time.Time, time.Time, error) {
	ci, err := os.Stat(r.certFile)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("stat certificate: %w", err)
	}
	ki, err := os.Stat(r.keyFile)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("stat key: %w", err)
	}
	return ci.ModTime(), ki.ModTime(), nil
}

// GetCertificate is the tls.Config hook. A failed reload keeps serving
// the pair already loaded: a half-written renewal must not take the only
// management interface down.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastCheck) >= reloadCheckInterval {
		r.lastCheck = time.Now()
		r.reloadIfChanged()
	}
	return r.cert, nil
}

func (r *certReloader) reloadIfChanged() {
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		log.Printf("tls: check certificate: %v", err)
		return
	}
	if certMod.Equal(r.certMod) && keyMod.Equal(r.keyMod) {
		return
	}
	if err := r.load(); err != nil {
		log.Printf("tls: reload certificate, keeping the previous one: %v", err)
		return
	}
	log.Printf("tls: reloaded certificate from %s", r.certFile)
}
