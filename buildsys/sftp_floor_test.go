package buildsys

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// pkg/sftp before v1.13.11 sizes the extended-attribute slice from a
// count read off the wire, so one reply from the backup server could make
// the web process allocate about 128 GiB and die. No advisory covers it,
// so govulncheck would not report a downgrade.
func TestSFTPModuleIsAtLeastTheFixedRelease(t *testing.T) {
	mod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`github\.com/pkg/sftp v1\.13\.(\d+)`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("go.mod requires no github.com/pkg/sftp v1.13.x")
	}
	if patch, _ := strconv.Atoi(string(m[1])); patch < 11 {
		t.Errorf("github.com/pkg/sftp v1.13.%d is below v1.13.11", patch)
	}
}
