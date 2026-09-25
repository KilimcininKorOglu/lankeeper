package buildsys_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unitDirectives returns every key=value line of a systemd unit, keyed by
// directive name. A directive that appears more than once keeps its last
// value, which is how systemd resolves most of them.
func unitDirectives(t *testing.T, name string) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "deploy", "systemd", name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return out
}

// namespaceDirectives each give a unit its own mount namespace when set
// to anything but off.
var namespaceDirectives = []string{
	"ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateMounts", "PrivateDevices",
	"ReadOnlyPaths", "ReadWritePaths", "InaccessiblePaths", "TemporaryFileSystem",
	"BindPaths", "BindReadOnlyPaths", "ProtectKernelTunables", "ProtectKernelModules",
	"ProtectControlGroups",
}

// TestTheAgentRunsInTheHostMountNamespace is the regression test. The
// agent unit carried ProtectSystem=full, PrivateTmp and ProtectHome.
// Verified under systemd on Debian 12: /etc and /usr were read-only for
// the agent, so every config write and the OTA binary install failed
// with EROFS, and a mount the agent made existed only inside its own
// namespace, invisible to the host and to smbd.
func TestTheAgentRunsInTheHostMountNamespace(t *testing.T) {
	unit := unitDirectives(t, "lankeeper-agent.service")
	for _, d := range namespaceDirectives {
		value, ok := unit[d]
		if !ok {
			continue
		}
		switch strings.ToLower(value) {
		case "", "no", "false", "off", "0":
			continue
		}
		t.Errorf("lankeeper-agent.service sets %s=%s, which gives the agent its own mount namespace", d, value)
	}
	if unit["User"] != "root" {
		t.Errorf("lankeeper-agent.service runs as %q, want root", unit["User"])
	}
}
