package iso_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every unit the services start has to reach the target: install.sh
// copies each one by name and build-iso.sh lists each one for the ISO,
// whose post-install copies whatever the ISO carries. A unit left out of
// either list makes every start of it fail on an installed router.
func TestEveryShippedUnitIsInstalled(t *testing.T) {
	units, err := filepath.Glob("../systemd/*")
	if err != nil || len(units) == 0 {
		t.Fatalf("no units under deploy/systemd: %v", err)
	}
	install, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	iso, err := os.ReadFile("build-iso.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		name := filepath.Base(u)
		if !strings.Contains(string(install), `"$script_dir/systemd/`+name+`"`) {
			t.Errorf("install.sh does not install %s", name)
		}
		if !strings.Contains(string(iso), `"$PROJECT_ROOT/deploy/systemd/`+name+`"`) {
			t.Errorf("build-iso.sh does not ship %s", name)
		}
	}
}
