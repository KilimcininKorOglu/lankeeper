package tmpl_test

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

// TestRendererParsesEmbeddedTemplates builds the renderer from the same
// embedded filesystem the binary ships with.
//
// This is the gap that let a page template reach a tagged commit with a
// nested action in it: NewRenderer parses every page eagerly and
// returns an error, NewServer propagates it, and `lankeeper serve`
// exits before binding. Nothing in the suite constructed the real
// renderer, so the whole product failing to start was invisible to CI.
//
// Any hand edit or bulk replace that breaks template syntax now fails
// here rather than on the operator's router.
func TestRendererParsesEmbeddedTemplates(t *testing.T) {
	loc, err := i18n.New("en")
	if err != nil {
		t.Fatalf("init i18n: %v", err)
	}
	if err := loc.LoadFromFS(webfs.EmbeddedFS, "locales"); err != nil {
		t.Fatalf("load locales: %v", err)
	}

	if _, err := tmpl.NewRenderer(webfs.EmbeddedFS, loc); err != nil {
		t.Fatalf("the shipped templates do not parse, so the server cannot start: %v", err)
	}
}
