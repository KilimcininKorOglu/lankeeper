package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

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
