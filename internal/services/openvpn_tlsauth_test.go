package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The client profile has to agree with the server on tls-auth: a client
// that HMAC-wraps its control packets cannot handshake with a server that
// does not, and the reverse.
func TestClientProfileFollowsTheServerTLSAuth(t *testing.T) {
	netutil.SetAgentClient(&pkiReadAgent{fileWriteAgent{files: map[string]string{}}})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	for _, tlsAuth := range []bool{true, false} {
		cfg := config.DefaultConfig()
		cfg.OpenVPN.Server.TLSAuth = tlsAuth
		profile, err := NewOpenVPNService(cfg).GenerateClientOVPN("laptop")
		if err != nil {
			t.Fatalf("tlsAuth=%v: profile: %v", tlsAuth, err)
		}
		for _, marker := range []string{"<tls-auth>", "key-direction 1"} {
			if strings.Contains(profile, marker) != tlsAuth {
				t.Errorf("tlsAuth=%v: profile has %q = %v", tlsAuth, marker, !tlsAuth)
			}
		}
	}
}
