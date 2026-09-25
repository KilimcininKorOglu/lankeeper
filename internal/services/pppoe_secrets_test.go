package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestPPPoERefusesCredentialsThatLeaveTheirField is the regression test.
// The username was rendered into `user "..."` unescaped, so a newline in
// it added a pppd option such as connect, which pppd runs as root, and
// nothing was written before the check could refuse it.
func TestPPPoERefusesCredentialsThatLeaveTheirField(t *testing.T) {
	agent := &fstabAgent{content: shippedChapSecrets}
	useFstabAgent(t, agent)

	for _, c := range []struct{ user, pass string }{
		{"isp\"\nconnect \"/bin/sh -c id", "pass"},
		{`isp\user`, "pass"},
		{"isp-user", "pa\nss"},
	} {
		svc := pppoeSecretsService(c.user, c.pass)
		svc.cfg.Interfaces = []config.InterfaceConfig{{ID: "wan", Device: "eth0", Role: "wan"}}
		if err := svc.renderConfig(); !errors.Is(err, ErrInvalidPPPoECredentials) {
			t.Errorf("user %q pass %q: err = %v, want ErrInvalidPPPoECredentials", c.user, c.pass, err)
		}
	}
	if agent.writes != 0 {
		t.Errorf("refused credentials reached %d file writes", agent.writes)
	}
	if err := validatePPPoECredentials("user@isp.example", `p"a\ss wörd`); err != nil {
		t.Errorf("an ordinary username and password were refused: %v", err)
	}
}

const shippedChapSecrets = "# Secrets for authentication using CHAP\n" +
	"# client\tserver\tsecret\t\t\tIP addresses\n" +
	"\"other\" * \"kept\"\n"

func pppoeSecretsService(user, pass string) *PPPoEService {
	cfg := &config.Config{}
	cfg.PPPoE.Username = user
	cfg.PPPoE.Password = pass
	return NewPPPoEService(cfg)
}

// TestPPPoESecretsReplaceTheOldPassword is the regression test. A new
// password was appended beside the old one, so the file kept every
// password the account had ever used and pppd saw two entries for one
// client.
func TestPPPoESecretsReplaceTheOldPassword(t *testing.T) {
	agent := &fstabAgent{content: shippedChapSecrets}
	useFstabAgent(t, agent)

	if err := pppoeSecretsService("isp-user", "old-pass").writeSecrets(); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := pppoeSecretsService("isp-user", "new-pass").writeSecrets(); err != nil {
		t.Fatalf("second write: %v", err)
	}

	want := shippedChapSecrets + "\"isp-user\" * \"new-pass\"\n"
	if got := agent.body(); got != want {
		t.Errorf("secrets file =\n%s\nwant\n%s", got, want)
	}
}

// TestPPPoESecretsKeepTheFileWhenTheReadFails is the regression test for
// the read. Its error was discarded, so a failed read was taken for an
// empty file and the write replaced every other entry with this one.
func TestPPPoESecretsKeepTheFileWhenTheReadFails(t *testing.T) {
	agent := &fstabAgent{content: shippedChapSecrets, readErr: true}
	useFstabAgent(t, agent)

	err := pppoeSecretsService("isp-user", "pass").writeSecrets()
	if err == nil || !strings.Contains(err.Error(), "agent unavailable") {
		t.Fatalf("err = %v, want the read error", err)
	}
	if agent.writes != 0 || agent.body() != shippedChapSecrets {
		t.Errorf("the secrets file was rewritten after a failed read:\n%s", agent.body())
	}
}
