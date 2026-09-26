package iso_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeUnboundControl logs its arguments and answers list_local_data with
// the records in $FAKE_LOCAL_DATA.
const fakeUnboundControl = `#!/usr/bin/env bash
if [[ "$1" == "list_local_data" ]]; then
    printf '%b' "$FAKE_LOCAL_DATA"
    exit 0
fi
echo "$*" >> "$FAKE_LOG"
`

// runLeaseScript runs the dnsmasq lease script with unbound-control
// replaced by a recorder and returns the commands it issued.
func runLeaseScript(t *testing.T, localData string, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unbound-control"), []byte(fakeUnboundControl), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "router.yaml")
	if err := os.WriteFile(config, []byte("system:\n  hostname: hermes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unbound := filepath.Join(dir, "unbound.conf")
	conf := "server:\n    local-data: \"nas.lan. IN A 10.10.10.5\"\n"
	if err := os.WriteFile(unbound, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "calls.log")

	cmd := exec.Command("bash", append([]string{"../dhcp-dns-update.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LANKEEPER_CONFIG="+config,
		"LANKEEPER_UNBOUND_CONF="+unbound,
		"FAKE_LOG="+logPath,
		"FAKE_LOCAL_DATA="+localData,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lease script: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// A client naming itself after an operator record must not publish over
// it, and releasing that lease must not delete the operator's record.
func TestLeaseScriptLeavesOperatorNamesAlone(t *testing.T) {
	for _, args := range [][]string{
		{"add", "aa:bb:cc:dd:ee:ff", "10.10.13.50", "nas"},
		{"del", "aa:bb:cc:dd:ee:ff", "10.10.13.50", "NAS"},
		{"add", "aa:bb:cc:dd:ee:ff", "10.10.13.50", "hermes"},
	} {
		if calls := runLeaseScript(t, "", args...); len(calls) != 0 {
			t.Errorf("%v touched Unbound: %v", args, calls)
		}
	}
}

func TestLeaseScriptPublishesAnUnclaimedName(t *testing.T) {
	calls := runLeaseScript(t, "", "add", "aa:bb:cc:dd:ee:ff", "10.10.10.60", "laptop")
	if len(calls) == 0 || !strings.Contains(calls[0], "laptop.lan. 300 IN A 10.10.10.60") {
		t.Errorf("lease not published: %v", calls)
	}
}

// A release removes a name only while it still points at the releasing
// lease's address.
func TestLeaseScriptRemovesOnlyItsOwnRecord(t *testing.T) {
	calls := runLeaseScript(t, `laptop.lan.\t300\tIN\tA\t10.10.10.61\n`, "del", "aa:bb:cc:dd:ee:ff", "10.10.10.60", "laptop")
	for _, c := range calls {
		if strings.Contains(c, "local_data_remove laptop") {
			t.Errorf("removed another lease's record: %v", calls)
		}
	}
	calls = runLeaseScript(t, `laptop.lan.\t300\tIN\tA\t10.10.10.60\n`, "del", "aa:bb:cc:dd:ee:ff", "10.10.10.60", "laptop")
	if len(calls) == 0 || calls[0] != "local_data_remove laptop.lan." {
		t.Errorf("own record not removed: %v", calls)
	}
}
