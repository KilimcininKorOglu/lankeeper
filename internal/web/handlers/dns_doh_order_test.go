package handlers

import (
	"reflect"
	"testing"
)

// recordOrder returns the sequence the two applies ran in.
func recordOrder(enableDoH bool) []string {
	var seq []string
	applyDNSPlane(enableDoH,
		func() { seq = append(seq, "doh") },
		func() { seq = append(seq, "dns") },
	)
	return seq
}

// TestDoHTeardownReloadsUnboundFirst is the regression test. The handler
// applied the DoH plane and then the DNS plane in one fixed order. That
// is correct when turning DoH on, and wrong when turning it off: the new
// settings are already persisted by then, so the DoH service takes its
// disabled branch and stops dnscrypt-proxy immediately, while
// unbound.conf still carries forward-addr 127.0.0.1@5353. Every query
// arriving between that stop and the later unbound reload was forwarded
// to a closed port.
func TestDoHTeardownReloadsUnboundFirst(t *testing.T) {
	got := recordOrder(false)
	want := []string{"dns", "doh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("disable order = %v, want %v: unbound must drop the forwarder "+
			"before the proxy it points at is stopped", got, want)
	}
}

// TestDoHEnableStartsTheProxyFirst keeps the direction that was already
// right from regressing while the other one is fixed.
func TestDoHEnableStartsTheProxyFirst(t *testing.T) {
	got := recordOrder(true)
	want := []string{"doh", "dns"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enable order = %v, want %v: the proxy must be listening "+
			"before unbound reloads with it as a forwarder", got, want)
	}
}

// TestBothPlanesAlwaysApply guards against a branch that skips one of
// them, which would leave the two halves of the configuration disagreeing.
func TestBothPlanesAlwaysApply(t *testing.T) {
	for _, enable := range []bool{true, false} {
		seq := recordOrder(enable)
		if len(seq) != 2 {
			t.Errorf("enableDoH=%v ran %d applies, want 2: %v", enable, len(seq), seq)
		}
	}
}
