package services

import (
	"fmt"
	"net"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// ovpnClientDirectives are the directives a pasted outbound client config
// may use. openvpn runs as root, and a config can run commands (up, down,
// script-security), load code (plugin), write files as root (log, status,
// writepid) or read them (config, auth-user-pass with a path), so the
// list is an allowlist of plain client and transport settings.
var ovpnClientDirectives = map[string]bool{
	"client": true, "dev": true, "dev-type": true, "proto": true, "remote": true,
	"remote-random": true, "resolv-retry": true, "nobind": true, "persist-key": true,
	"persist-tun": true, "remote-cert-tls": true, "cipher": true, "data-ciphers": true,
	"data-ciphers-fallback": true, "auth": true, "verb": true, "mute": true,
	"compress": true, "comp-lzo": true, "key-direction": true, "tls-client": true,
	"tls-version-min": true, "tls-cipher": true, "tls-ciphersuites": true,
	"auth-nocache": true, "reneg-sec": true, "pull": true, "float": true,
	"mssfix": true, "tun-mtu": true, "fragment": true, "keepalive": true, "ping": true,
	"ping-restart": true, "connect-retry": true, "connect-retry-max": true,
	"server-poll-timeout": true, "verify-x509-name": true, "route-nopull": true,
	"redirect-gateway": true, "pull-filter": true, "explicit-exit-notify": true,
	"auth-user-pass": true, "remote-cert-ku": true, "remote-cert-eku": true,
	"ns-cert-type": true, "sndbuf": true, "rcvbuf": true, "fast-io": true,
}

// ovpnInlineBlocks are the inline sections whose body is key material.
var ovpnInlineBlocks = map[string]bool{
	"ca": true, "cert": true, "key": true, "tls-auth": true, "tls-crypt": true,
	"tls-crypt-v2": true, "extra-certs": true,
}

// ValidateOutboundClient checks an outbound OpenVPN client before it is
// stored or written, because the root agent runs openvpn on the result.
func ValidateOutboundClient(c config.OVPNClientConfig) error {
	if err := ValidateOpenVPNClientName(c.Name); err != nil {
		return err
	}
	if c.ConfigFile != "" {
		return validateOVPNClientConfigFile(c.ConfigFile)
	}
	if net.ParseIP(c.RemoteHost) == nil && ValidateDomain(c.RemoteHost) != nil {
		return fmt.Errorf("remote host %q is not an IP address or a DNS name", c.RemoteHost)
	}
	if hasControl(c.Username) || hasControl(c.Password) {
		return fmt.Errorf("username and password may not contain control characters")
	}
	return nil
}

// validateOVPNClientConfigFile accepts only allowlisted directives and
// inline key blocks. auth-user-pass must take no argument, because the
// argument is a path openvpn reads as root.
func validateOVPNClientConfigFile(content string) error {
	block := ""
	for n, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if block != "" {
			if line == "</"+block+">" {
				block = ""
			}
			continue
		}
		name, err := ovpnLineDirective(line)
		if err != nil {
			return fmt.Errorf("config line %d: %w", n+1, err)
		}
		if strings.HasPrefix(name, "<") {
			block = strings.Trim(name, "<>")
		}
	}
	if block != "" {
		return fmt.Errorf("config: inline block <%s> is not closed", block)
	}
	return nil
}

// ovpnLineDirective returns the directive of one config line, or "" for
// a blank or comment line, and refuses anything outside the allowlist.
func ovpnLineDirective(line string) (string, error) {
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", nil
	}
	fields := strings.Fields(line)
	name := strings.TrimPrefix(fields[0], "--")
	if strings.HasPrefix(name, "<") && strings.HasSuffix(name, ">") && len(fields) == 1 {
		return name, checkInlineBlock(name)
	}
	return name, checkDirective(name, len(fields)-1)
}

func checkInlineBlock(tag string) error {
	if !ovpnInlineBlocks[strings.Trim(tag, "<>")] {
		return fmt.Errorf("inline block %s is not allowed", tag)
	}
	return nil
}

func checkDirective(name string, args int) error {
	if !ovpnClientDirectives[name] {
		return fmt.Errorf("directive %q is not allowed", name)
	}
	if name == "auth-user-pass" && args > 0 {
		return fmt.Errorf("auth-user-pass may not name a file")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
