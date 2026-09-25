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

	first, err := svc.SamplePerClient(context.Background())
	if err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if len(first) != 1 || first[0].InBytes != 1000 || first[0].OutBytes != 2000 ||
		first[0].InBPS != 0 || first[0].OutBPS != 0 || first[0].Hostname != "alice" {
		t.Fatalf("first sample = %+v", first)
	}

	svc.mu.Lock()
	svc.lastSample = time.Now().Add(-2 * time.Second)
	svc.mu.Unlock()
	agent.stdout = qosCountersJSON(mac, 3000, 2500)

	second, err := svc.SamplePerClient(context.Background())
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	// 2000 bytes in and 500 bytes out over a little more than 2 s.
	if in := second[0].InBPS; in < 7900 || in > 8000 {
		t.Errorf("InBPS = %d, want about 8000", in)
	}
	if out := second[0].OutBPS; out < 1975 || out > 2000 {
		t.Errorf("OutBPS = %d, want about 2000", out)
	}
	if got := svc.ClientHistory(mac); len(got) != 2 {
		t.Errorf("history holds %d samples, want 2", len(got))
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
