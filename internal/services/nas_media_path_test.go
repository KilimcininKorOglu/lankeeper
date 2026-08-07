package services

import (
	"errors"
	"testing"
)

// TestValidateMediaPathRejectsADotSegmentEscape is the regression test.
// The M3U sync gated each source with a literal prefix test against the
// raw configured string. A value like /srv/../../etc/cron.d satisfies
// that test verbatim while resolving elsewhere, and the kernel resolves
// the dot segments at syscall time, so directory creation and .strm
// writes genuinely landed outside /srv and /mnt.
func TestValidateMediaPathRejectsADotSegmentEscape(t *testing.T) {
	escapes := []string{
		"/srv/../../etc/cron.d",
		"/srv/../etc",
		"/mnt/../root",
		"/srv/media/../../../tmp",
		"/srv/..",
	}
	for _, p := range escapes {
		got, err := ValidateMediaPath(p)
		if err == nil {
			t.Errorf("ValidateMediaPath(%q) = %q, want a rejection", p, got)
			continue
		}
		if !errors.Is(err, ErrMediaPathPrefix) {
			t.Errorf("ValidateMediaPath(%q) = %v, want ErrMediaPathPrefix", p, err)
		}
	}
}

// TestValidateMediaPathReturnsTheCleanedForm matters as much as the
// rejection: a caller that keeps using the raw string it passed in
// gains nothing from the check.
func TestValidateMediaPathReturnsTheCleanedForm(t *testing.T) {
	cases := map[string]string{
		"/srv/media":            "/srv/media",
		"/srv/media/":           "/srv/media",
		"/srv//media":           "/srv/media",
		"/srv/./media":          "/srv/media",
		"/srv/media/sub/../tv":  "/srv/media/tv",
		"/mnt/disk1/recordings": "/mnt/disk1/recordings",
	}
	for in, want := range cases {
		got, err := ValidateMediaPath(in)
		if err != nil {
			t.Errorf("ValidateMediaPath(%q) = %v, want nil", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateMediaPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateMediaPathRejectsForeignRoots keeps the sandbox itself.
func TestValidateMediaPathRejectsForeignRoots(t *testing.T) {
	for _, p := range []string{"/etc/cron.d", "/", "/srv", "/mnt", "/srvsomething/x", "/mntx/y", "relative/path"} {
		if got, err := ValidateMediaPath(p); err == nil {
			t.Errorf("ValidateMediaPath(%q) = %q, want a rejection", p, got)
		}
	}
}

// TestValidateMediaPathChecksCharactersBeforeCleaning covers the
// ordering the helper exists to enforce. Clean collapses dot segments
// but preserves control characters, so the character set has to be
// tested on the raw value or a newline survives into whatever the path
// is later rendered into.
func TestValidateMediaPathChecksCharactersBeforeCleaning(t *testing.T) {
	hostile := []string{
		"/srv/media\n[global]",
		"/srv/media\"",
		"/srv/media\x00",
		"/srv/media;id",
		"/srv/media$(id)",
	}
	for _, p := range hostile {
		got, err := ValidateMediaPath(p)
		if err == nil {
			t.Errorf("ValidateMediaPath(%q) = %q, want a rejection", p, got)
			continue
		}
		if !errors.Is(err, ErrMediaPathCharacters) {
			t.Errorf("ValidateMediaPath(%q) = %v, want ErrMediaPathCharacters", p, err)
		}
	}
}

// TestValidateMediaPathRejectsAnEmptyPath guards the zero value, which
// would otherwise Clean to "." and fail the prefix test for the wrong
// reason.
func TestValidateMediaPathRejectsAnEmptyPath(t *testing.T) {
	if _, err := ValidateMediaPath(""); !errors.Is(err, ErrMediaPathCharacters) {
		t.Errorf("got %v, want ErrMediaPathCharacters", err)
	}
}
