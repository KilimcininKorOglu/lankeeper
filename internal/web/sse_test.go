package web_test

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/web"
)

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
