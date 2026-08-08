package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/web"
)

// TestSSEStreamIsNotStored pins no-store on the event stream. no-cache
// would still permit a store that revalidates, which is meaningless for
// a response that never ends and carries live interface counters and
// per-client bandwidth.
func TestSSEStreamIsNotStored(t *testing.T) {
	broker := web.NewSSEBroker()

	// The handler blocks until the request context is done, so bound it
	// here. Without the bound a regression that makes it run forever
	// would hang until the package timeout rather than failing.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/events/stats", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	broker.ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on an event stream", got)
	}
}

func TestSSEBrokerPubSub(t *testing.T) {
	broker := web.NewSSEBroker()

	ch, err := broker.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer broker.Unsubscribe(ch)

	if broker.ClientCount() != 1 {
		t.Errorf("client count = %d, want 1", broker.ClientCount())
	}

	broker.Publish("stats", map[string]int{"cpu": 42})

	msg := <-ch
	if len(msg) == 0 {
		t.Error("should receive message")
	}

	if string(msg[:6]) != "event:" {
		t.Errorf("message should start with 'event:', got %q", string(msg[:6]))
	}
}

func TestSSEBrokerUnsubscribe(t *testing.T) {
	broker := web.NewSSEBroker()

	ch, err := broker.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	broker.Unsubscribe(ch)

	if broker.ClientCount() != 0 {
		t.Errorf("client count = %d after unsubscribe, want 0", broker.ClientCount())
	}
}

func TestSSEBrokerNoClients(t *testing.T) {
	broker := web.NewSSEBroker()
	broker.Publish("test", "data")
}
