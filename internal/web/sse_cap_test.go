package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestBroker builds a broker with limits a test can reach. The two
// fields are set once at construction in production and never mutated,
// so writing them here before any goroutine touches the broker is not a
// race.
func newTestBroker(max int, keepAlive time.Duration) *SSEBroker {
	b := NewSSEBroker()
	b.maxClients = max
	b.keepAlive = keepAlive
	return b
}

// TestSubscribeIsBounded is the regression test. Subscribe inserted a
// new channel unconditionally, so nothing limited how many streams one
// client could hold open, and each one costs a goroutine, a map entry
// and a buffer for as long as it lives.
func TestSubscribeIsBounded(t *testing.T) {
	b := newTestBroker(3, time.Hour)

	var held []chan []byte
	for i := range 3 {
		ch, err := b.Subscribe()
		if err != nil {
			t.Fatalf("subscriber %d was refused below the cap: %v", i, err)
		}
		held = append(held, ch)
	}

	ch, err := b.Subscribe()
	if !errors.Is(err, ErrTooManySubscribers) {
		t.Errorf("the subscriber above the cap got err=%v, want ErrTooManySubscribers", err)
	}
	if ch != nil {
		t.Error("a refused subscription still returned a channel")
	}
	if got := b.ClientCount(); got != 3 {
		t.Errorf("client count = %d after a refusal, want 3", got)
	}

	for _, c := range held {
		b.Unsubscribe(c)
	}
}

// TestAClosedStreamFreesItsSlot keeps the cap from being one-way. A
// full broker that never recovers is an outage, not a limit.
func TestAClosedStreamFreesItsSlot(t *testing.T) {
	b := newTestBroker(1, time.Hour)

	first, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := b.Subscribe(); !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("the broker accepted a second stream at a cap of 1: %v", err)
	}

	b.Unsubscribe(first)

	second, err := b.Subscribe()
	if err != nil {
		t.Fatalf("the slot was not released: %v", err)
	}
	b.Unsubscribe(second)
}

// TestARefusedStreamIsNotAnEventStream covers the response shape. The
// stream headers used to be written before the subscription existed, so
// a refusal would have gone out labelled text/event-stream and the
// browser would have kept reconnecting into it.
func TestARefusedStreamIsNotAnEventStream(t *testing.T) {
	b := newTestBroker(1, time.Hour)

	held, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer b.Unsubscribe(held)

	// A refusal returns at once. The bounded context is here so that a
	// regression, which would accept the stream and block, fails in a
	// second instead of hanging until the package timeout.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events/stats", nil).WithContext(ctx)

	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("a refusal carried no Retry-After")
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("a refusal was labelled %q", got)
	}
}

// blockedWriter is a ResponseWriter whose Write fails, standing in for a
// peer that vanished without closing the connection.
type blockedWriter struct {
	httptest.ResponseRecorder
	mu      sync.Mutex
	fail    bool
	written chan struct{}
	once    sync.Once
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	fail := w.fail
	w.mu.Unlock()

	w.once.Do(func() { close(w.written) })
	if fail {
		return 0, errors.New("broken pipe")
	}
	return w.ResponseRecorder.Write(p)
}

func (w *blockedWriter) Flush() {}

// TestAnIdleStreamWritesAKeepAlive covers the liveness half. A stream
// that never writes never learns its peer is gone, and the bandwidth
// broker publishes nothing at all while sampling is failing.
func TestAnIdleStreamWritesAKeepAlive(t *testing.T) {
	b := newTestBroker(4, 5*time.Millisecond)

	w := &blockedWriter{written: make(chan struct{})}
	w.ResponseRecorder = *httptest.NewRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events/qos", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.ServeHTTP(w, req)
	}()

	select {
	case <-w.written:
	case <-time.After(2 * time.Second):
		t.Fatal("an idle stream wrote nothing")
	}

	cancel()
	<-done

	if got := w.Body.String(); !strings.Contains(got, ": keep-alive") {
		t.Errorf("the idle write was not a keep-alive comment: %q", got)
	}
}

// TestADeadPeerReleasesItsSlot is what makes the cap safe. Without a
// write to fail on, a vanished peer would hold its slot until the
// kernel gave up on the socket, and enough of them would refuse the
// operator a stream they are entitled to.
func TestADeadPeerReleasesItsSlot(t *testing.T) {
	b := newTestBroker(1, 5*time.Millisecond)

	w := &blockedWriter{fail: true, written: make(chan struct{})}
	w.ResponseRecorder = *httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/events/qos", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.ServeHTTP(w, req)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never noticed the failed write")
	}

	if got := b.ClientCount(); got != 0 {
		t.Errorf("client count = %d after the peer went away, want 0", got)
	}

	ch, err := b.Subscribe()
	if err != nil {
		t.Fatalf("the dead stream still held the only slot: %v", err)
	}
	b.Unsubscribe(ch)
}

// TestPublishStillReachesASubscriber keeps the change from breaking the
// thing it guards.
func TestPublishStillReachesASubscriber(t *testing.T) {
	b := newTestBroker(4, time.Hour)

	ch, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer b.Unsubscribe(ch)

	b.Publish("stats", map[string]int{"cpu": 42})

	select {
	case msg := <-ch:
		if !strings.HasPrefix(string(msg), "event: stats\n") {
			t.Errorf("unexpected frame: %q", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("the subscriber received nothing")
	}
}

// TestTheProductionBrokerCarriesTheLimits pins the wiring, since every
// other test here builds a broker with its own numbers.
func TestTheProductionBrokerCarriesTheLimits(t *testing.T) {
	b := NewSSEBroker()

	if b.maxClients != maxSSESubscribers {
		t.Errorf("maxClients = %d, want %d", b.maxClients, maxSSESubscribers)
	}
	if b.keepAlive != sseKeepAliveInterval {
		t.Errorf("keepAlive = %v, want %v", b.keepAlive, sseKeepAliveInterval)
	}
	if b.maxClients <= 0 {
		t.Error("a non-positive cap would refuse every stream")
	}
	if b.keepAlive <= 0 {
		t.Error("a non-positive keep-alive interval panics NewTicker")
	}
}
