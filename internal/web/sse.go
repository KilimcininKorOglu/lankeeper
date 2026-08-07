package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	// maxSSESubscribers bounds one broker's concurrent streams. Each
	// stream costs a goroutine, a map entry and a 16-slot buffer, and
	// nothing bounded how many a client could open.
	//
	// The two brokers are counted separately, so this is 32 dashboard
	// streams and 32 bandwidth streams. A single-admin LAN appliance
	// reaches neither: one browser tab opens one of each.
	maxSSESubscribers = 32

	// sseKeepAliveInterval is how often an idle stream writes, so a
	// peer that vanished without closing the connection is noticed.
	// Without this a cap is worse than none, because dead entries
	// would fill it and lock the operator out of their own dashboard.
	sseKeepAliveInterval = 30 * time.Second

	// sseChannelBuffer is the per-subscriber queue depth. Publish
	// drops rather than blocking when it is full.
	sseChannelBuffer = 16
)

// sseKeepAliveFrame is an SSE comment. The wire format defines it as
// ignorable, so EventSource discards it and the page never sees it.
var sseKeepAliveFrame = []byte(": keep-alive\n\n")

// ErrTooManySubscribers reports that the broker is at capacity.
var ErrTooManySubscribers = errors.New("sse: too many concurrent subscribers")

type SSEBroker struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}

	// Set once at construction and read without the lock. Present so
	// a test can drive the limits without waiting half a minute.
	maxClients int
	keepAlive  time.Duration
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients:    make(map[chan []byte]struct{}),
		maxClients: maxSSESubscribers,
		keepAlive:  sseKeepAliveInterval,
	}
}

// Subscribe registers a stream, or reports that the broker is full.
func (b *SSEBroker) Subscribe() (chan []byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.clients) >= b.maxClients {
		return nil, ErrTooManySubscribers
	}

	ch := make(chan []byte, sseChannelBuffer)
	b.clients[ch] = struct{}{}
	return ch, nil
}

func (b *SSEBroker) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *SSEBroker) Publish(event string, data any) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("sse marshal error: %v", err)
		return
	}

	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, jsonData))

	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *SSEBroker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErrorT(w, r, http.StatusInternalServerError, "error.streamingUnsupported")
		return
	}

	// Subscribe before the stream headers go out: a refusal is an
	// ordinary error response, not an event stream.
	ch, err := b.Subscribe()
	if err != nil {
		w.Header().Set("Retry-After", "5")
		httpErrorT(w, r, http.StatusServiceUnavailable, "error.tooManyStreams")
		return
	}
	defer b.Unsubscribe(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// The stats broker publishes every second, so a dead peer there is
	// noticed almost at once. The bandwidth broker publishes nothing
	// while sampling is failing, and a stream that never writes never
	// learns its peer is gone.
	keepAlive := time.NewTicker(b.keepAlive)
	defer keepAlive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			if _, err := w.Write(sseKeepAliveFrame); err != nil {
				return
			}
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
