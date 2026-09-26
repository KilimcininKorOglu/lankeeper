package services

import (
	"errors"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// wg-quick routes every AllowedIPs entry, so a remote subnet outside the
// private LAN ranges would pull that traffic into the tunnel.
func TestRemoteSubnetsMustBePrivateLANRanges(t *testing.T) {
	svc := NewVPNService(config.DefaultConfig())
	for _, bad := range []string{"::/0", "0.0.0.0/0", "2000::/3", "128.0.0.0/1", "8.8.8.0/24", "10.0.0.0/7"} {
		if err := svc.checkRemoteSubnets([]string{bad}); !errors.Is(err, ErrPeerSubnetNotPrivate) {
			t.Errorf("%s: err=%v, want ErrPeerSubnetNotPrivate", bad, err)
		}
	}
	for _, good := range []string{"192.168.50.0/24", "172.16.4.0/22", "fd12:3456:789a::/48"} {
		if err := svc.checkRemoteSubnets([]string{good}); err != nil {
			t.Errorf("%s refused: %v", good, err)
		}
	}
}
