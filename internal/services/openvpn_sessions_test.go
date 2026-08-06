package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// statusFileAgent serves a canned OpenVPN status file for file.read and
// nothing else, so a stray call is visible as an error.
type statusFileAgent struct {
	body string
	fail bool
}

func (a *statusFileAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method != "file.read" {
		return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
	}
	if a.fail {
		return nil, &netutil.AgentError{Op: "file.read", Target: ovpnStatusPath, Err: errNoStatusFile{}}
	}
	return json.Marshal(struct {
		Content string `json:"content"`
	}{Content: a.body})
}

type errNoStatusFile struct{}

func (errNoStatusFile) Error() string { return "no such file or directory" }

// version1Status is OpenVPN's default status format, which is what the
// shipped server template produces since it sets no status-version.
const version1Status = `OpenVPN CLIENT LIST
Updated,Thu Aug  7 00:12:00 2026
Common Name,Real Address,Bytes Received,Bytes Sent,Connected Since
phone,203.0.113.5:52345,12345,54321,Thu Aug  7 00:01:00 2026
laptop,203.0.113.9:41003,222,333,Thu Aug  7 00:05:00 2026
ROUTING TABLE
Virtual Address,Common Name,Real Address,Last Ref
10.8.0.2,phone,203.0.113.5:52345,Thu Aug  7 00:11:59 2026
GLOBAL STATS
Max bcast/mcast queue length,1
END
`

// TestCountOVPNSessionsReadsTheClientList is the regression test.
// lankeeper_openvpn_active_sessions was written to the exposition on
// every scrape but nothing ever assigned the field, so the series read
// zero permanently: either a constant false alert, or false confidence
// that OpenVPN was being monitored at all.
func TestCountOVPNSessionsReadsTheClientList(t *testing.T) {
	if got := countOVPNSessions(version1Status); got != 2 {
		t.Errorf("counted %d sessions, want 2", got)
	}
}

// TestCountOVPNSessionsIgnoresTheRoutingTable is the trap in this
// format: the routing table repeats a row per client, so a naive line
// count doubles the answer.
func TestCountOVPNSessionsIgnoresTheRoutingTable(t *testing.T) {
	if strings.Count(version1Status, "phone") < 2 {
		t.Fatal("the fixture no longer exercises the routing-table trap")
	}
	if got := countOVPNSessions(version1Status); got == 4 {
		t.Error("routing-table rows were counted as sessions")
	}
}

func TestCountOVPNSessionsOnAnIdleServer(t *testing.T) {
	idle := `OpenVPN CLIENT LIST
Updated,Thu Aug  7 00:12:00 2026
Common Name,Real Address,Bytes Received,Bytes Sent,Connected Since
ROUTING TABLE
Virtual Address,Common Name,Real Address,Last Ref
GLOBAL STATS
END
`
	if got := countOVPNSessions(idle); got != 0 {
		t.Errorf("counted %d sessions on an idle server, want 0", got)
	}
}

// TestCountOVPNSessionsAcceptsTheMachineReadableFormat covers an
// operator who sets status-version by hand, so they do not silently get
// zero.
func TestCountOVPNSessionsAcceptsTheMachineReadableFormat(t *testing.T) {
	v2 := `TITLE,OpenVPN 2.6.3
TIME,Thu Aug  7 00:12:00 2026,1786000320
HEADER,CLIENT_LIST,Common Name,Real Address
CLIENT_LIST,phone,203.0.113.5:52345,10.8.0.2,,12345,54321
CLIENT_LIST,laptop,203.0.113.9:41003,10.8.0.3,,222,333
ROUTING_TABLE,10.8.0.2,phone,203.0.113.5:52345
END
`
	if got := countOVPNSessions(v2); got != 2 {
		t.Errorf("counted %d sessions in the machine-readable format, want 2", got)
	}
}

func TestActiveSessionsIsZeroWhenTheServerNeverRan(t *testing.T) {
	netutil.SetAgentClient(&statusFileAgent{fail: true})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewOpenVPNService(&config.Config{})
	if got := svc.ActiveSessions(context.Background()); got != 0 {
		t.Errorf("got %d sessions with no status file, want 0", got)
	}
}

// TestSnapshotCarriesTheOpenVPNSessionCount is the wiring half: the
// service had no OpenVPN reference at all, so no value could ever reach
// the snapshot.
func TestSnapshotCarriesTheOpenVPNSessionCount(t *testing.T) {
	netutil.SetAgentClient(&statusFileAgent{body: version1Status})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	svc := NewMetricsService(cfg, nil, nil, nil, nil, nil, nil, nil, NewOpenVPNService(cfg))

	snap := svc.Snapshot(context.Background())
	if snap.OpenVPNPeers != 2 {
		t.Errorf("OpenVPNPeers = %d, want 2", snap.OpenVPNPeers)
	}

	var sb strings.Builder
	if err := snap.Write(&sb); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(sb.String(), "lankeeper_openvpn_active_sessions 2") {
		t.Errorf("the exposition still reports a stale value:\n%s", sb.String())
	}
}
