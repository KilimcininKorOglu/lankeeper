package services

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestAReleaseTagCannotEscapeTheBackupDirectory is the regression test.
// The tag arrives in the GitHub API response and was used unchecked:
// interpolated into /var/lib/lankeeper/backups/pre-update-%s.tar.gz,
// which then reached MkdirAll and tar through the root agent, and into
// the GRUB boot entry and the log.
//
// Publishing a hostile tag means controlling the repository's releases,
// so the window is narrow rather than open. That bounds the severity,
// not the defect.
func TestAReleaseTagCannotEscapeTheBackupDirectory(t *testing.T) {
	rejected := map[string]string{
		"parent traversal":    "../../../etc/cron.d/x",
		"absolute path":       "/etc/cron.d/x",
		"embedded slash":      "v1.0.0/../../evil",
		"newline":             "v1.0.0\nevil",
		"carriage return":     "v1.0.0\revil",
		"space":               "v1.0.0 evil",
		"null byte":           "v1.0.0\x00evil",
		"shell metacharacter": "v1.0.0;rm -rf /",
		"backtick":            "v1.0.0`id`",
		"dollar":              "v1.0.0$(id)",
		"empty":               "",
		"bare dots":           "..",
		"single dot":          ".",
		"leading dot":         ".1.0.0",
		"letters only":        "latest",
		"tilde":               "v1.0.0~1",
		"too long":            "v1.0." + strings.Repeat("9", 64),
	}

	for name, tag := range rejected {
		t.Run(name, func(t *testing.T) {
			err := validateReleaseTag(tag)
			if err == nil {
				t.Fatalf("validateReleaseTag(%q) was accepted", tag)
			}
			if !errors.Is(err, ErrInvalidReleaseTag) {
				t.Errorf("unexpected error kind: %v", err)
			}
		})
	}
}

// TestARealReleaseTagIsAccepted keeps the guard from refusing the
// project's own releases. The first entry is the shipped tag shape.
func TestARealReleaseTagIsAccepted(t *testing.T) {
	for _, tag := range []string{
		"v0.5.0",
		"v1.2.3",
		"1.2.3",
		"v10.0.0",
		"v1.0.0-rc1",
		"v1.0.0-beta.2",
		"v0.5.0-89-g21e15a8",
		"v1",
		"v1.2",
	} {
		if err := validateReleaseTag(tag); err != nil {
			t.Errorf("validateReleaseTag(%q) was refused: %v", tag, err)
		}
	}
}

// TestTheTagGuardSitsAtTheDecodeBoundary pins where the check lives.
// The tag has seven use sites, and validating each one is the
// arrangement that lets a new one be added without a guard, so the
// whole release is refused at the single point it enters the process.
//
// CheckForUpdate builds its URL from api.github.com, so there is no
// seam to point at a test server, and adding one only for a test would
// widen production for no operational reason. The wiring is asserted
// against the source instead, which is how this codebase guards the
// properties behaviour cannot show.
func TestTheTagGuardSitsAtTheDecodeBoundary(t *testing.T) {
	raw, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatalf("read update.go: %v", err)
	}
	body := string(raw)

	decode := strings.Index(body, "json.NewDecoder(resp.Body).Decode(&release)")
	guard := strings.Index(body, "validateReleaseTag(release.TagName)")
	build := strings.Index(body, "LatestVersion:  release.TagName")

	switch {
	case decode < 0:
		t.Fatal("the release decode moved; this guard needs updating")
	case guard < 0:
		t.Fatal("CheckForUpdate no longer validates the release tag")
	case build < 0:
		t.Fatal("the UpdateInfo construction moved; this guard needs updating")
	}

	if guard < decode {
		t.Error("the tag is validated before it is decoded")
	}
	if guard > build {
		t.Error("the tag reaches UpdateInfo before it is validated")
	}
}
