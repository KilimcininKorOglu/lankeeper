package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// nftListAgent answers `nft -j list table` with a fixed stdout, or
// with err when it is set.
type nftListAgent struct {
	stdout string
	err    error
}

func (a *nftListAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method != "exec.run" {
		return nil, fmt.Errorf("unhandled %s", method)
	}
	if a.err != nil {
		return nil, a.err
	}
	return json.Marshal(map[string]any{"stdout": a.stdout, "stderr": "", "exitCode": 0})
}

// qosCountersJSON renders the nft JSON listing for one MAC's counters.
func qosCountersJSON(mac string, in, out uint64) string {
	inName, outName := counterNames(mac)
	return fmt.Sprintf(`{"nftables":[{"counter":{"family":"inet","table":"lankeeper_qos","name":%q,"bytes":%d}},`+
		`{"counter":{"family":"inet","table":"lankeeper_qos","name":%q,"bytes":%d}}]}`, inName, in, outName, out)
}

// TestSamplePerClientComputesRatesFromTheLastSample pins the sampler's
// arithmetic: the first sample carries bytes but no rate, and the next
// one reports the byte delta as bits per second over the elapsed time.
func TestSamplePerClientComputesRatesFromTheLastSample(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:01"
	agent := &nftListAgent{stdout: qosCountersJSON(mac, 1000, 2000)}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewQoSService(&config.Config{})
	svc.clientLeases = map[string]Lease{mac: {MAC: mac, IP: "10.10.10.5", Hostname: "alice"}}

	first := sampleOne(t, svc)
	first.Updated = time.Time{}
	want := ClientUsage{MAC: mac, IP: "10.10.10.5", Hostname: "alice", InBytes: 1000, OutBytes: 2000}
	if first != want {
		t.Fatalf("first sample = %+v, want %+v", first, want)
	}

	svc.mu.Lock()
	svc.lastSample = time.Now().Add(-2 * time.Second)
	svc.mu.Unlock()
	agent.stdout = qosCountersJSON(mac, 3000, 2500)

	// 2000 bytes in and 500 bytes out over a little more than 2 s.
	second := sampleOne(t, svc)
	assertRateNear(t, "InBPS", second.InBPS, 8000)
	assertRateNear(t, "OutBPS", second.OutBPS, 2000)
}

// sampleOne takes a sample and requires exactly one client in it.
func sampleOne(t *testing.T, svc *QoSService) ClientUsage {
	t.Helper()
	usages, err := svc.SamplePerClient(context.Background())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("sample holds %d clients, want 1", len(usages))
	}
	return usages[0]
}

// assertRateNear accepts a rate at most 1.25% below want, which covers
// the time the test itself takes between the two samples.
func assertRateNear(t *testing.T, name string, got, want uint64) {
	t.Helper()
	if got > want || got < want-want/80 {
		t.Errorf("%s = %d, want about %d", name, got, want)
	}
}

// TestSamplePerClientTreatsAMissingTableAsEmpty keeps the sampler loop
// running before the first rebuild has created the table.
func TestSamplePerClientTreatsAMissingTableAsEmpty(t *testing.T) {
	netutil.SetAgentClient(&nftListAgent{err: errors.New("Error: No such file or directory")})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	usages, err := NewQoSService(&config.Config{}).SamplePerClient(context.Background())
	if err != nil || usages != nil {
		t.Errorf("got %v, %v; want nil, nil", usages, err)
	}
}
