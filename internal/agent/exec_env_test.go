package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExecParamsCarriesNoEnv is the regression test. The RPC used to
// take an Env slice and append it to the child's environment verbatim,
// while the whitelist governed only the command name. That gated which
// binary ran but not the environment it ran under, and the dynamic
// loader honours LD_PRELOAD and friends at execve time whatever the
// binary is. A caller reaching exec.run could therefore influence
// execution inside a legitimate whitelisted command without planting a
// fake one.
func TestExecParamsCarriesNoEnv(t *testing.T) {
	raw, err := json.Marshal(ExecParams{Cmd: "nft", Args: []string{"list", "ruleset"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "env") {
		t.Errorf("the exec request still has an env field: %s", raw)
	}

	// An old or hostile client sending the field must have it ignored
	// rather than honoured, which decoding into the struct guarantees.
	var p ExecParams
	if err := json.Unmarshal([]byte(`{"cmd":"nft","args":["list"],"env":["LD_PRELOAD=/tmp/evil.so"]}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Cmd != "nft" {
		t.Errorf("Cmd = %q, want nft", p.Cmd)
	}
	// Nothing in the struct can hold the injected variable.
	round, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if strings.Contains(string(round), "LD_PRELOAD") {
		t.Errorf("the injected variable survived decoding: %s", round)
	}
}

// TestCommandEnvIsFixedPerCommand pins what the agent supplies itself,
// which is the only reason the field existed.
func TestCommandEnvIsFixedPerCommand(t *testing.T) {
	cases := map[string]string{
		"easyrsa": "EASYRSA_PKI=/etc/openvpn/pki",
		// mkcert reads its CA location from the environment. If the
		// caller could set it, the agent would run mkcert as root
		// against a root of the caller's choosing, and the CA the web
		// UI hands out would stop being the one that signed what the
		// server presents.
		"mkcert": "CAROOT=/var/lib/lankeeper/mkcert",
	}
	for cmd, want := range cases {
		got := commandEnv(cmd)
		if len(got) != 1 || got[0] != want {
			t.Errorf("commandEnv(%s) = %v, want [%s]", cmd, got, want)
		}
	}
}

// TestCommandEnvIsEmptyForEverythingElse keeps the addition narrow: no
// other whitelisted command gains an environment it did not have.
func TestCommandEnvIsEmptyForEverythingElse(t *testing.T) {
	for _, cmd := range []string{"nft", "ip", "wg", "systemctl", "openvpn", "tar", "rm", ""} {
		if got := commandEnv(cmd); len(got) != 0 {
			t.Errorf("commandEnv(%q) = %v, want nothing", cmd, got)
		}
	}
}

// TestCommandEnvCannotBeInfluencedByArguments guards the shape of the
// lookup: it keys on the command alone, so no argument can select a
// different environment.
func TestCommandEnvCannotBeInfluencedByArguments(t *testing.T) {
	if a, b := commandEnv("easyrsa"), commandEnv("easyrsa"); len(a) != len(b) {
		t.Fatal("the lookup is not deterministic")
	}
	for _, spoof := range []string{"easyrsa ", " easyrsa", "EASYRSA", "easyrsa;nft", "mkcert ", "MKCERT"} {
		if got := commandEnv(spoof); len(got) != 0 {
			t.Errorf("commandEnv(%q) = %v, want nothing", spoof, got)
		}
	}
}
