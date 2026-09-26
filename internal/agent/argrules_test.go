package agent

import (
	"strings"
	"testing"
)

// The argv shapes the services send today must keep working.
func TestArgRulesAcceptTheServiceCalls(t *testing.T) {
	ok := [][]string{
		{"cp", "-f", "/usr/local/bin/lankeeper", "/usr/local/bin/lankeeper.bak"},
		{"cp", "-f", "/var/lib/lankeeper/update/lankeeper", "/usr/local/bin/lankeeper"},
		{"cp", "-f", "/usr/local/bin/lankeeper.bak", "/usr/local/bin/lankeeper"},
		{"cp", "--", "/var/log/queries.log", "/var/log/queries.log.1"},
		{"rm", "-f", "/var/lib/lankeeper/staging/export-1.tar.gz"},
		{"rm", "-f", "/usr/local/bin/lankeeper.bak"},
		{"rm", "-f", "/var/backups/lankeeper-pre-update-v1.2.0.tar.gz"},
		{"rm", "-f", "/var/lib/lankeeper/mkcert/cert.pem", "/var/lib/lankeeper/mkcert/key.pem"},
		{"rm", "-f", "--", "/var/log/queries.log.1"},
		{"chmod", "600", "/var/backups/lankeeper-pre-update-v1.2.0.tar.gz"},
		{"chmod", "640", "/var/lib/lankeeper/staging/export-1.tar.gz"},
		{"chmod", "+x", "/usr/local/bin/lankeeper"},
		{"usermod", "-p", "$6$salt$hash", "root"},
		{"systemctl", "reboot"},
		{"systemctl", "restart", "lankeeper.target"},
		{"systemctl", "stop", "lankeeper-update-guard.timer"},
		{"systemctl", "start", "openvpn@server"},
		{"systemctl", "restart", "chrony"},
		{"systemctl", "reload-or-restart", "dnsmasq"},
		{"systemctl", "enable", "--now", "lankeeper-dhcp6c.service"},
		{"systemctl", "enable", "dnscrypt-proxy"},
		{"mkdir", "-p", "/mnt/raid0"},
		{"mount", "/dev/md0", "/mnt/raid0"},
		{"tar", "czf", "/var/lib/lankeeper/staging/export-1.tar.gz", "-C", "/etc", "lankeeper", "-C", "/etc", "unbound"},
		{"tar", "czf", "/var/backups/lankeeper-pre-update-v1.tar.gz", "-C", "/etc", "lankeeper"},
	}
	for _, argv := range ok {
		if err := argValidators[argv[0]](argv[1:]); err != nil {
			t.Errorf("%s refused: %v", strings.Join(argv, " "), err)
		}
	}
}

// Each of these hands the service account root through a whitelisted
// command name.
func TestArgRulesRefuseEscalation(t *testing.T) {
	bad := [][]string{
		{"cp", "/var/lib/lankeeper/x", "/etc/cron.d/x"},
		{"cp", "-f", "/var/lib/lankeeper/x", "/etc/cron.d/x"},
		{"cp", "-f", "/etc/shadow", "/var/lib/lankeeper/shadow"},
		{"cp", "-f", "/var/lib/lankeeper/../../etc/shadow", "/var/lib/lankeeper/s"},
		{"rm", "-rf", "/"},
		{"rm", "-f", "/etc/passwd"},
		{"chmod", "4755", "/usr/local/bin/lankeeper"},
		{"chmod", "600", "/etc/shadow"},
		{"chmod", "+x", "/var/lib/lankeeper/x"},
		{"usermod", "-aG", "sudo", "lankeeper"},
		{"usermod", "-p", "$6$x", "lankeeper"},
		{"systemctl", "start", "foo"},
		{"systemctl", "enable", "--now", "evil.service"},
		{"systemctl", "link", "/var/lib/lankeeper/x.service"},
		{"mkdir", "-p", "/etc/cron.d"},
		{"mkdir", "-p", "/mnt/../etc/x"},
		{"mount", "/dev/sda1", "/etc"},
		{"mount", "--bind", "/var/lib/lankeeper", "/mnt/x"},
		{"tar", "czf", "/etc/x.tar.gz", "-C", "/etc", "lankeeper"},
		{"tar", "czf", "/var/lib/lankeeper/x", "-C", "/", "etc"},
		{"tar", "xzf", "/var/lib/lankeeper/x", "-C", "/"},
	}
	for _, argv := range bad {
		if err := argValidators[argv[0]](argv[1:]); err == nil {
			t.Errorf("%s was accepted", strings.Join(argv, " "))
		}
	}
}

func TestUnusedRootCommandsAreNotWhitelisted(t *testing.T) {
	for _, c := range []string{"chpasswd", "mv"} {
		if allowedCommands[c] {
			t.Errorf("%s is still whitelisted", c)
		}
	}
}
