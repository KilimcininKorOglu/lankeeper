package netutil

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var macRegex = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)

// ifaceNameRegex matches what the kernel accepts for a network device
// name. Anything outside this set would break out of the quoted string
// it is interpolated into when rendering nftables rules.
var ifaceNameRegex = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// ruleNameRegex allows a readable label while excluding every character
// that carries meaning in an nftables file: quotes, the comment marker,
// the statement separator, and anything that could start a new line.
var ruleNameRegex = regexp.MustCompile(`^[A-Za-z0-9 _.:/()-]*$`)

// fsPathRegex is the character allowlist for a filesystem path that
// ends up inside a rendered config file. Space is allowed because share
// paths legitimately contain it; control characters, quotes, and
// statement separators are not.
var fsPathRegex = regexp.MustCompile(`^[A-Za-z0-9/._+~ -]+$`)

// ValidateFilesystemPath checks an operator-supplied path before it is
// normalised or prefix-checked.
//
// filepath.Clean collapses separators and dot segments but preserves
// control characters, and a prefix allowlist says nothing about the rest
// of the string. A path carrying a newline therefore survives both and
// can terminate the directive it is rendered into, so the character set
// has to be checked on the raw value.
func ValidateFilesystemPath(s string) error {
	if s == "" {
		return fmt.Errorf("path is empty")
	}
	if len(s) > 4096 {
		return fmt.Errorf("path is longer than 4096 characters")
	}
	if !fsPathRegex.MatchString(s) {
		return fmt.Errorf("path contains characters that are not allowed: %q", s)
	}
	return nil
}

// ValidateInterfaceName checks a network device name. IFNAMSIZ caps the
// kernel's own limit at 16 bytes including the terminator, so 15 is the
// longest usable name.
func ValidateInterfaceName(s string) error {
	if s == "" {
		return fmt.Errorf("interface name is empty")
	}
	if len(s) > 15 {
		return fmt.Errorf("invalid interface name: %q (longer than 15 characters)", s)
	}
	if !ifaceNameRegex.MatchString(s) {
		return fmt.Errorf("invalid interface name: %q", s)
	}
	return nil
}

// ValidateRuleName checks an operator-supplied label that ends up inside
// a rendered config file. An empty name is allowed; the label is
// cosmetic. A name carrying a newline or a quote is not, because the
// renderer writes it verbatim into nftables syntax.
func ValidateRuleName(s string) error {
	if len(s) > 64 {
		return fmt.Errorf("rule name is longer than 64 characters")
	}
	if !ruleNameRegex.MatchString(s) {
		return fmt.Errorf("rule name contains characters that are not allowed: %q", s)
	}
	return nil
}

func ValidateIP(s string) error {
	if net.ParseIP(s) == nil {
		return fmt.Errorf("invalid IP address: %s", s)
	}
	return nil
}

func ValidateCIDR(s string) error {
	_, _, err := net.ParseCIDR(s)
	if err != nil {
		return fmt.Errorf("invalid CIDR: %s", s)
	}
	return nil
}

func ValidateMAC(s string) error {
	if !macRegex.MatchString(s) {
		return fmt.Errorf("invalid MAC address: %s", s)
	}
	return nil
}

func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", port)
	}
	return nil
}

func ValidateVLANID(vid int) error {
	if vid < 1 || vid > 4094 {
		return fmt.Errorf("invalid VLAN ID: %d (must be 1-4094)", vid)
	}
	return nil
}

func ValidateMTU(mtu int) error {
	if mtu < 68 || mtu > 9000 {
		return fmt.Errorf("invalid MTU: %d (must be 68-9000)", mtu)
	}
	return nil
}

func ParseCIDRAddress(cidr string) (ip string, prefix int, err error) {
	parts := strings.SplitN(cidr, "/", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid CIDR: %s", cidr)
	}
	if net.ParseIP(parts[0]) == nil {
		return "", 0, fmt.Errorf("invalid IP in CIDR: %s", parts[0])
	}
	p, err := strconv.Atoi(parts[1])
	if err != nil || p < 0 || p > 128 {
		return "", 0, fmt.Errorf("invalid prefix length in CIDR: %s", parts[1])
	}
	return parts[0], p, nil
}
