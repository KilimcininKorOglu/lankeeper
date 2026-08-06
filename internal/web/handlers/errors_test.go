package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// agentStderrError reproduces what the chain actually delivers: the root
// agent embeds the failed process's stderr, and every layer above wraps
// that string unchanged.
func agentStderrError() error {
	inner := fmt.Errorf("rpc error -32000: exec nft: exit status 1 " +
		"(stderr: /tmp/nftables-1837.conf:42:1-9: Error: Could not process rule: No such file or directory)")
	return &netutil.AgentError{Op: "exec.run", Target: "nft", Err: inner}
}

// TestFailHidesAgentDetailFromTheBrowser is the regression test. The
// handlers passed err.Error() straight to http.Error, so raw nft,
// wg-quick, easyrsa and openvpn stderr, exact command names and internal
// temp-file paths were rendered in the browser and kept in its history.
func TestFailHidesAgentDetailFromTheBrowser(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/firewall/apply", nil)

	fail(rec, req, http.StatusInternalServerError, agentStderrError())

	body := rec.Body.String()
	for _, leak := range []string{"nft", "stderr", "/tmp/nftables-", "exit status", "rpc error"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaked %q: %s", leak, body)
		}
	}
	if got := strings.TrimSpace(body); got != http.StatusText(http.StatusInternalServerError) {
		t.Errorf("body = %q, want the plain status text", got)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", rec.Code)
	}
}

// TestFailKeepsOurOwnValidationMessages ensures the sanitizer does not
// flatten every error into "Bad Request": a message our own code wrote
// tells the operator what to change.
func TestFailKeepsOurOwnValidationMessages(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/vpn/peers", nil)

	fail(rec, req, http.StatusBadRequest, errors.New(`peer "phone" not found`))

	if got := strings.TrimSpace(rec.Body.String()); got != `peer "phone" not found` {
		t.Errorf("body = %q, want our own message", got)
	}
}

// TestFailHidesAgentDetailWrappedDeeper covers the real call shape,
// where a service wraps the agent error in its own context before the
// handler sees it.
func TestFailHidesAgentDetailWrappedDeeper(t *testing.T) {
	wrapped := fmt.Errorf("apply nftables: %w", agentStderrError())

	rec := httptest.NewRecorder()
	fail(rec, httptest.NewRequest(http.MethodPost, "/firewall/apply", nil), http.StatusInternalServerError, wrapped)

	if strings.Contains(rec.Body.String(), "stderr") {
		t.Errorf("a wrapped agent error still leaked: %s", rec.Body.String())
	}
}

// TestNoHandlerForwardsErrErrorToTheBrowser keeps the pattern from
// creeping back in. Every one of these sites has to go through fail so
// the agent check cannot be skipped.
func TestNoHandlerForwardsErrErrorToTheBrowser(t *testing.T) {
	pattern := regexp.MustCompile(`http\.Error\([^,]+,\s*err\.Error\(\)`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if loc := pattern.FindIndex(b); loc != nil {
			line := 1 + strings.Count(string(b[:loc[0]]), "\n")
			t.Errorf("%s:%d forwards err.Error() to the browser; use fail() instead", name, line)
		}
	}
}

// TestSafeErrorMessageHandlesNil guards the helper's own edge case.
func TestSafeErrorMessageHandlesNil(t *testing.T) {
	if got := safeErrorMessage(nil, http.StatusInternalServerError); got != http.StatusText(http.StatusInternalServerError) {
		t.Errorf("got %q", got)
	}
}
