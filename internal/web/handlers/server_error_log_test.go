package handlers

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerErrorLogsTheCause(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/system/backup/import", nil)
	serverError(rec, req, "error.importFailed", errors.New("write tar member lankeeper/router.yaml: disk full"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(buf.String(), "disk full") || !strings.Contains(buf.String(), "/system/backup/import") {
		t.Fatalf("journal line lacks the cause or route: %q", buf.String())
	}
	if strings.Contains(rec.Body.String(), "disk full") {
		t.Fatal("the internal cause reached the browser")
	}
}

// A 500 answered through clientError writes only the translated text,
// so the cause must be logged in the lines just before it.
func TestNoHandlerAnswers500WithoutLogging(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		// errors.go defines the helpers themselves.
		if strings.HasSuffix(f, "_test.go") || f == "errors.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "clientError(w, r, http.StatusInternalServerError") {
				continue
			}
			ctx := strings.Join(lines[max(0, i-4):i], "\n")
			// saveVLANs logs the save error itself.
			if !strings.Contains(ctx, "log.Printf") && !strings.Contains(ctx, "h.saveVLANs(") {
				t.Errorf("%s:%d answers 500 without logging the cause; use serverError", f, i+1)
			}
		}
	}
}
