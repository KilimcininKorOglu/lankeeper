package buildsys_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServeRunsFromTheTemplateDirectory guards the working directory of
// the web process. Every service parses configs/sysconf/*.tmpl relative
// to it, the installers copy the templates only to the data directory,
// and systemd starts the unit in /. Without the chdir, no service can
// render a config at runtime.
func TestServeRunsFromTheTemplateDirectory(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "cmd", "lankeeper", "serve.go"))
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	if !strings.Contains(string(src), `"cwd", "/var/lib/lankeeper"`) {
		t.Error("serve has no -cwd flag defaulting to /var/lib/lankeeper")
	}
	if !strings.Contains(string(src), "os.Chdir(*cwd)") {
		t.Error("serve does not chdir to the template directory")
	}

	for _, installer := range []string{"install.sh", filepath.Join("iso", "post-install.sh")} {
		script, err := os.ReadFile(filepath.Join("..", "deploy", installer))
		if err != nil {
			t.Fatalf("read %s: %v", installer, err)
		}
		if !strings.Contains(string(script), `DATA_DIR="/var/lib/lankeeper"`) ||
			!strings.Contains(string(script), `$DATA_DIR/configs/sysconf`) {
			t.Errorf("%s no longer installs the templates under /var/lib/lankeeper/configs/sysconf", installer)
		}
	}
}
