package buildsys_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// randReadCall matches a call to the CSPRNG under any of the import
// aliases this tree uses.
var randReadCall = regexp.MustCompile(`\b(?:rand|cryptorand|crypto_rand)\.Read\(`)

// checkedRandRead matches the same call when its return is captured, in
// either the `if _, err := ...` or the plain `_, err = ...` form.
var checkedRandRead = regexp.MustCompile(`(?:_,\s*err\s*:?=|n,\s*err\s*:?=)\s*(?:rand|cryptorand|crypto_rand)\.Read\(`)

// TestNoCSPRNGReadDiscardsItsError is the regression test. The CSRF
// token and the session-signing secret were both built by allocating a
// buffer, calling rand.Read, and encoding the buffer without looking at
// the return. On this toolchain crypto/rand.Read never returns an error
// and crashes the program if its source fails, so the reported
// zero-filled buffer is not reachable today. The discarded return still
// read as an unchecked error to every reviewer, and it is one reader
// swap away from being one: both values feed a security decision, so a
// failure has to be a refusal rather than whatever the buffer held.
//
// Scanning the tree rather than the two sites keeps the other eight
// CSPRNG reads honest too.
func TestNoCSPRNGReadDiscardsItsError(t *testing.T) {
	roots := []string{"../internal", "../cmd"}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			for i, line := range strings.Split(string(raw), "\n") {
				if !randReadCall.MatchString(line) {
					continue
				}
				if checkedRandRead.MatchString(line) {
					continue
				}
				t.Errorf("%s:%d discards the CSPRNG read result: %s",
					path, i+1, strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

// TestTheScanWouldCatchAnUncheckedRead guards the test itself: a pattern
// that matches nothing would pass silently for ever.
func TestTheScanWouldCatchAnUncheckedRead(t *testing.T) {
	unchecked := []string{
		"	rand.Read(b)",
		"	crypto_rand.Read(b)",
		"	cryptorand.Read(raw[:])",
	}
	for _, line := range unchecked {
		if !randReadCall.MatchString(line) {
			t.Errorf("the call pattern does not match %q", line)
		}
		if checkedRandRead.MatchString(line) {
			t.Errorf("%q was wrongly treated as checked", line)
		}
	}

	checked := []string{
		"	if _, err := rand.Read(b); err != nil {",
		"	_, err = rand.Read(salt)",
		"	if n, err := cryptorand.Read(raw[:]); err != nil {",
	}
	for _, line := range checked {
		if !checkedRandRead.MatchString(line) {
			t.Errorf("the checked pattern does not match %q", line)
		}
	}
}
