package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// pkiReadAgent records file writes like fileWriteAgent and answers
// every file.read with placeholder PEM text, so a client profile can
// be generated without a PKI on disk.
type pkiReadAgent struct{ fileWriteAgent }

func (a *pkiReadAgent) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method == "file.read" {
		return []byte(`{"content":"PEM\n"}`), nil
	}
	return a.fileWriteAgent.Call(ctx, method, params)
}

// easy-rsa leaves the private key root-only, so the web process can
// only read the PKI through the agent.
func TestGenerateClientOVPNReadsThePKIThroughTheAgent(t *testing.T) {
	netutil.SetAgentClient(&pkiReadAgent{fileWriteAgent{files: map[string]string{}}})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	profile, err := NewOpenVPNService(config.DefaultConfig()).GenerateClientOVPN("laptop")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if !strings.Contains(profile, "<key>\nPEM\n</key>") {
		t.Fatalf("profile does not carry the key read through the agent:\n%s", profile)
	}
}
