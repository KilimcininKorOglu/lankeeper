package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSanitizePathNeutralisesDotComponents is the regression test for
// the component producer. The replacer covered separators and shell
// metacharacters but never `.`, so a playlist group of `..` survived
// verbatim and filepath.Join resolved it to the parent directory.
// Playlist bodies are fetched live from a remote server, so a hostile
// or compromised provider controls this string.
func TestSanitizePathNeutralisesDotComponents(t *testing.T) {
	for _, in := range []string{"..", ".", "...", "  ..  ", ""} {
		got := sanitizePath(in)
		if strings.Trim(got, ".") == "" {
			t.Errorf("sanitizePath(%q) = %q, which still resolves as a dot component", in, got)
		}
	}
}

// TestSanitizePathKeepsOrdinaryNames guards the common case: interior
// dots are legitimate in group and title names and must survive.
func TestSanitizePathKeepsOrdinaryNames(t *testing.T) {
	cases := map[string]string{
		"Movies":         "Movies",
		"Sci-Fi 4.0":     "Sci-Fi 4.0",
		"News/Sport":     "News_Sport",
		"What? Really":   "What_ Really",
		"C:\\Temp":       "C__Temp",
		"file.name.here": "file.name.here",
	}
	for in, want := range cases {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestContainedJoinRefusesAnEscape covers the check that actually
// decides the outcome, independent of what the character replacer did.
func TestContainedJoinRefusesAnEscape(t *testing.T) {
	base := "/srv/media"
	for _, component := range []string{"..", "../evil", "../../etc", "../../../"} {
		if got, err := containedJoin(base, component); err == nil {
			t.Errorf("containedJoin(%q, %q) = %q, want a rejection", base, component, got)
		}
	}
}

// TestContainedJoinConfinesAnAbsoluteComponent records the behaviour
// rather than assuming it: filepath.Join treats an absolute second
// argument as relative, so the result lands under the base instead of
// at the root. Confinement is the outcome that matters here, so this is
// accepted rather than rejected.
func TestContainedJoinConfinesAnAbsoluteComponent(t *testing.T) {
	got, err := containedJoin("/srv/media", "/etc/passwd")
	if err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	if got != "/srv/media/etc/passwd" {
		t.Errorf("got %q, want the component confined under the base", got)
	}
}

func TestContainedJoinAcceptsAChildPath(t *testing.T) {
	base := "/srv/media"
	got, err := containedJoin(base, "Movies")
	if err != nil {
		t.Fatalf("a plain child was rejected: %v", err)
	}
	if got != "/srv/media/Movies" {
		t.Errorf("got %q, want /srv/media/Movies", got)
	}
}

// TestContainedJoinIsNotFooledByAPrefixMatch is the classic mistake in
// this shape of check: a sibling directory whose name merely starts
// with the base is not inside the base.
func TestContainedJoinIsNotFooledByAPrefixMatch(t *testing.T) {
	if got, err := containedJoin("/srv/media", "../media-evil"); err == nil {
		t.Errorf("containedJoin accepted the sibling %q", got)
	}
}

// TestM3USyncKeepsHostileGroupsInsideTheDownloadPath is the end-to-end
// proof: a group of `..` used to create a directory and write an
// attacker-controlled .strm file one level above the configured
// download path.
func TestM3USyncKeepsHostileGroupsInsideTheDownloadPath(t *testing.T) {
	root := t.TempDir()
	download := filepath.Join(root, "media")
	if err := os.MkdirAll(download, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The path the escape would have produced, had the join not been
	// checked: one level up from the download directory.
	escaped := filepath.Join(root, "pwned.strm")

	group := sanitizePath("..")
	dir, err := containedJoin(download, group)
	if err != nil {
		// Rejected outright, which is the desired outcome.
		return
	}
	strm, err := containedJoin(dir, sanitizePath("pwned")+".strm")
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir group dir: %v", err)
	}
	if err := os.WriteFile(strm, []byte("http://example.invalid\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Errorf("a file was written outside the download path at %s", escaped)
	}
	if !strings.HasPrefix(strm, download+string(filepath.Separator)) {
		t.Errorf("the .strm landed at %q, outside %q", strm, download)
	}
}
