package services

import (
	"errors"
	"net"
	"testing"
)

func withInterfaceNets(t *testing.T, cidrs ...string) {
	t.Helper()
	orig := localInterfaceNets
	t.Cleanup(func() { localInterfaceNets = orig })
	var nets []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		nets = append(nets, n)
	}
	localInterfaceNets = func() ([]*net.IPNet, error) { return nets, nil }
}

// A LAN host's SLAAC address is global unicast, so only the router's
// own topology marks it as internal.
func TestInternalIPRefusesALANGlobalIPv6Address(t *testing.T) {
	withInterfaceNets(t, "2001:db8:10:1::1/64")
	if !isInternalIP(net.ParseIP("2001:db8:10:1::abcd")) {
		t.Fatal("a host on the LAN's global /64 passed the outbound guard")
	}
	if isInternalIP(net.ParseIP("2606:4700::1111")) {
		t.Fatal("a public address off every local network was refused")
	}
}

func TestInternalIPFailsClosedWithoutTheInterfaceList(t *testing.T) {
	orig := localInterfaceNets
	t.Cleanup(func() { localInterfaceNets = orig })
	localInterfaceNets = func() ([]*net.IPNet, error) { return nil, errors.New("no netlink") }
	if !isInternalIP(net.ParseIP("2606:4700::1111")) {
		t.Fatal("an unknown topology did not fail closed")
	}
}
