package tmpl_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"templates/layouts/base.html": &fstest.MapFile{
			Data: []byte(`{{ define "base.html" }}<!DOCTYPE html><html lang="{{ .Lang }}"><body>{{ block "content" . }}{{ end }}</body></html>{{ end }}`),
		},
		"templates/pages/test.html": &fstest.MapFile{
			Data: []byte(`{{ define "content" }}<h1>{{ t .Lang "test.title" }}</h1>{{ end }}`),
		},
		"templates/partials/empty.html": &fstest.MapFile{
			Data: []byte(`{{ define "nav" }}<nav>nav</nav>{{ end }}`),
		},
	}
}

func testI18n(t *testing.T) *i18n.I18n {
	t.Helper()
	loc, _ := i18n.New("en")
	locFS := fstest.MapFS{
		"locales/en.json": &fstest.MapFile{
			Data: []byte(`{"test.title": "Test Page"}`),
		},
	}
	if err := loc.LoadFromFS(locFS, "locales"); err != nil {
		panic(err)
	}
	return loc
}

func TestRendererRender(t *testing.T) {
	loc := testI18n(t)
	r, err := tmpl.NewRenderer(testFS(), loc)
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}

	rec := httptest.NewRecorder()
	data := &tmpl.PageData{Lang: "en", Page: "test"}

	if err := r.Render(rec, "test", "base", data); err != nil {
		t.Fatalf("render: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Test Page") {
		t.Errorf("expected 'Test Page' in body, got: %s", body)
	}
	if !strings.Contains(body, `lang="en"`) {
		t.Errorf("expected lang=\"en\" in body, got: %s", body)
	}
}

func TestFuncMapFormatBytes(t *testing.T) {
	fm := tmpl.FuncMap(nil)
	fn := fm["formatBytes"].(func(uint64) string)

	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		got := fn(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFuncMapHumanTime(t *testing.T) {
	fm := tmpl.FuncMap(nil)
	fn := fm["humanTime"].(func(time.Duration) string)

	tests := []struct {
		input time.Duration
		want  string
	}{
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h 30m"},
		{25 * time.Hour, "1d 1h 0m"},
	}

	for _, tt := range tests {
		got := fn(tt.input)
		if got != tt.want {
			t.Errorf("humanTime(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
