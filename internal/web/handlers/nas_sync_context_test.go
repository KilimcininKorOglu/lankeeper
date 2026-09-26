package handlers

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

// A manual sync keeps running after the handler answers, so its context
// must survive the request context's cancellation.
func TestBackgroundContextOutlivesTheRequest(t *testing.T) {
	reqCtx, cancelReq := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/nas/m3u/sync", nil).WithContext(reqCtx)

	ctx, cancel := backgroundContext(r, time.Minute)
	defer cancel()
	cancelReq()

	if err := ctx.Err(); err != nil {
		t.Fatalf("background context ended with the request: %v", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("background context carries no deadline")
	}
}
