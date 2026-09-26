package services

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestOutboundClientRefusesConfigsThatRunCode is the regression test. The
// form stored a pasted config verbatim and the root agent ran openvpn on
// it, so script, plugin and file directives ran or wrote as root.
func TestOutboundClientRefusesConfigsThatRunCode(t *testing.T) {
	for _, cfgText := range []string{
		"client\nscript-security 2\nup /tmp/x",
		"client\nplugin /tmp/evil.so",
		"client\nlog /etc/shadow",
		"client\nauth-user-pass /etc/shadow",
		"client\n<ca>\n-----BEGIN-----",
		"client\n<script>\nx\n</script>",
	} {
		c := config.OVPNClientConfig{Name: "work", ConfigFile: cfgText}
		if err := ValidateOutboundClient(c); err == nil {
			t.Errorf("config %q was accepted", cfgText)
		}
	}

	ok := "client\ndev tun\nproto udp\nremote vpn.example.com 1194\nauth-user-pass\n<ca>\n-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n</ca>\n"
	if err := ValidateOutboundClient(config.OVPNClientConfig{Name: "work", ConfigFile: ok}); err != nil {
		t.Errorf("an ordinary client config was refused: %v", err)
	}
}

// TestOutboundClientRefusesAnInjectedRemoteHost covers the template path:
// the host lands on the remote line, so a line break adds a directive.
func TestOutboundClientRefusesAnInjectedRemoteHost(t *testing.T) {
	bad := config.OVPNClientConfig{Name: "work", RemoteHost: "vpn.example.com\nscript-security 2", Protocol: "udp"}
	if err := ValidateOutboundClient(bad); err == nil {
		t.Error("a remote host with a line break was accepted")
	}
	good := config.OVPNClientConfig{Name: "work", RemoteHost: "vpn.example.com", Protocol: "udp"}
	if err := ValidateOutboundClient(good); err != nil {
		t.Errorf("a valid remote host was refused: %v", err)
	}
}
