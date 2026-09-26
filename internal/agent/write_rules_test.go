package agent

import (
	"os"
	"testing"
)

// Root scans these directories and runs or loads what it finds, so a
// directory rule let a caller add a file there: a grub.d *.cfg is sourced
// as a shell script by update-grub, ip-up.d runs on link up, and a
// dnsmasq.d drop-in may carry dhcp-script. Only the files the services
// write are admitted.
func TestWriteRulesAdmitOnlyTheFilesTheServicesWrite(t *testing.T) {
	for _, p := range []string{
		"/etc/default/grub.d/x.cfg",
		"/etc/ppp/ip-up.d/x",
		"/etc/ppp/peers/other",
		"/etc/dnsmasq.d/evil.conf",
		"/etc/wide-dhcpv6/other-script",
	} {
		if checkPathRules(p, allowedWriteRules) {
			t.Errorf("%s is writable", p)
		}
	}
	for _, p := range []string{
		"/etc/default/grub.d/lankeeper.cfg",
		"/etc/ppp/options",
		"/etc/ppp/peers/wan",
		"/etc/ppp/chap-secrets",
		"/etc/ppp/pap-secrets",
		"/etc/dnsmasq.d/lankeeper-ipv6-ra.conf",
		"/etc/wide-dhcpv6/dhcp6c.conf",
		"/etc/wide-dhcpv6/dhcp6c-script",
		"/etc/wide-dhcpv6/lankeeper-dhcp6c.env",
	} {
		if !checkPathRules(p, allowedWriteRules) {
			t.Errorf("%s, which a service writes, is refused", p)
		}
	}
}

// Execute bits are refused everywhere but the dhcp6c script.
func TestFileWriteRefusesExecuteBitsOutsideTheKnownScript(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o744, 0o700, 0o701} {
		if validateFileExec("/etc/lankeeper/x", mode) == nil {
			t.Errorf("mode %#o accepted on an ordinary file", mode)
		}
	}
	if err := validateFileExec(dhcp6cScript, 0o755); err != nil {
		t.Errorf("the dhcp6c script: %v", err)
	}
	if err := validateFileExec("/etc/lankeeper/x", 0o644); err != nil {
		t.Errorf("mode 0644: %v", err)
	}
}
