package buildsys_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// makeVar returns the value of a `NAME := value` assignment.
func makeVar(t *testing.T, makefile, name string) string {
	t.Helper()

	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*:?=\s*(.*)$`)
	m := re.FindStringSubmatch(makefile)
	if m == nil {
		t.Fatalf("%s is gone from the Makefile", name)
	}
	return strings.TrimSpace(m[1])
}

// readmeName turns a Makefile artifact path into the form the README
// documents: the dist/ prefix dropped, $(BINARY) expanded, and $(VERSION)
// written as the placeholder the README uses.
func readmeName(raw string) string {
	s := strings.ReplaceAll(raw, "$(BINARY)", "lankeeper")
	s = strings.ReplaceAll(s, "$(VERSION)", "vX.Y.Z")
	s = strings.ReplaceAll(s, "$(DIST_DIR)/", "")
	return strings.TrimPrefix(s, "dist/")
}

// TestTheReadmeNamesTheArtifactsTheMakefileBuilds is the regression
// test. The README listed the static binaries as
// lankeeper-vX.Y.Z-linux-{amd64,arm64}, a filename the Makefile never
// produces: only the tarballs and ISOs carry the version. A contributor
// looking for a file by the documented name would not find it.
func TestTheReadmeNamesTheArtifactsTheMakefileBuilds(t *testing.T) {
	makefile, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	mk, doc := string(makefile), string(readme)

	// The unversioned binaries, which is the half that was wrong.
	for _, v := range []string{"AMD64_BINARY", "ARM64_BINARY"} {
		name := readmeName(makeVar(t, mk, v))
		arch := strings.TrimPrefix(name, "lankeeper-linux-")
		// The README collapses the pair into one brace expression.
		if !strings.Contains(doc, "lankeeper-linux-{amd64,arm64}") {
			t.Errorf("the README does not document %s (built as %s)", arch, name)
		}
		if strings.Contains(name, "vX.Y.Z") {
			t.Errorf("%s carries a version after all; this test's premise is stale", v)
		}
	}

	// The versioned ISOs, which were already right and must stay right.
	for _, v := range []string{"AMD64_ISO", "ARM64_ISO"} {
		name := readmeName(makeVar(t, mk, v))
		if !strings.Contains(name, "vX.Y.Z") {
			t.Errorf("%s no longer carries a version: %s", v, name)
		}
	}
	if !strings.Contains(doc, "lankeeper-vX.Y.Z-installer-{amd64,arm64}.iso") {
		t.Error("the README does not document the installer ISO name")
	}

	// The tarball name is built inline in the release recipe rather than
	// held in a variable, so it is matched against the recipe text.
	if !strings.Contains(mk, "dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz") {
		t.Error("the tarball name changed; the README claim needs rechecking")
	}
	if !strings.Contains(doc, "lankeeper-vX.Y.Z-linux-{amd64,arm64}.tar.gz") {
		t.Error("the README does not document the release tarball name")
	}
}

// TestTheReadmeDoesNotClaimAVersionedBinary pins the specific wrong
// claim, so reintroducing it fails rather than merely leaving the
// correct line alongside it.
func TestTheReadmeDoesNotClaimAVersionedBinary(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	for line := range strings.SplitSeq(string(readme), "\n") {
		if !strings.Contains(line, "static binaries") {
			continue
		}
		if strings.Contains(line, "vX.Y.Z") {
			t.Errorf("the static binaries are still documented with a version: %s", strings.TrimSpace(line))
		}
	}
}
