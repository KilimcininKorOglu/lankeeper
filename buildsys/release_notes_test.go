package buildsys_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runReleaseNotes invokes the real target and returns the notes it wrote.
func runReleaseNotes(t *testing.T, version string) (string, string, error) {
	t.Helper()
	path := filepath.Join(repoRoot, "dist", "RELEASE_NOTES.md")
	saved, readErr := os.ReadFile(path)
	t.Cleanup(func() {
		if readErr == nil {
			_ = os.WriteFile(path, saved, 0o600)
			return
		}
		_ = os.Remove(path)
	})

	cmd := exec.Command("make", "release-notes", "VERSION="+version)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	notes, _ := os.ReadFile(path)
	return string(notes), string(out), err
}

// The release body is the CHANGELOG section for the tag. It has to stop
// at the next section, or every release would carry the whole history.
func TestReleaseNotesTakeOnlyTheTaggedSection(t *testing.T) {
	notes, out, err := runReleaseNotes(t, "v0.5.5")
	if err != nil {
		t.Fatalf("make release-notes: %v\n%s", err, out)
	}
	if !strings.Contains(notes, "### Security") {
		t.Errorf("the 0.5.5 section is missing its Security heading:\n%s", notes)
	}
	if strings.Contains(notes, "## [") {
		t.Errorf("the notes run into another section:\n%s", notes)
	}
}

// A tag with no CHANGELOG section must stop the release instead of
// publishing an empty body.
func TestReleaseNotesRefuseAMissingSection(t *testing.T) {
	notes, out, err := runReleaseNotes(t, "v0.0.0-nothing-matches-this")
	if err == nil {
		t.Fatalf("make release-notes succeeded for a version with no section:\n%s", out)
	}
	if !strings.Contains(out, "has no section") {
		t.Errorf("the failure does not say why:\n%s", out)
	}
	if notes != "" {
		t.Errorf("notes were left behind:\n%s", notes)
	}
}
