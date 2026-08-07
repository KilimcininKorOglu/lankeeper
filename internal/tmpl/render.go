package tmpl

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
)

type Renderer struct {
	templates map[string]*template.Template
	loc       *i18n.I18n
}

func NewRenderer(fsys fs.FS, loc *i18n.I18n) (*Renderer, error) {
	funcMap := FuncMap(loc)
	r := &Renderer{
		templates: make(map[string]*template.Template),
		loc:       loc,
	}

	layouts, err := fs.Glob(fsys, "templates/layouts/*.html")
	if err != nil {
		return nil, fmt.Errorf("glob layouts: %w", err)
	}

	pages, err := fs.Glob(fsys, "templates/pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("glob pages: %w", err)
	}

	partials, err := fs.Glob(fsys, "templates/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("glob partials: %w", err)
	}

	shared := append(layouts, partials...)

	for _, page := range pages {
		files := append([]string{page}, shared...)

		tmpl, err := template.New("").Funcs(funcMap).ParseFS(fsys, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}

		r.templates[page] = tmpl
	}

	return r, nil
}

type PageData struct {
	Lang      string
	Page      string
	CSRFToken string
	Error     string
	Data      any
}

func (r *Renderer) Render(w http.ResponseWriter, page, layout string, data *PageData) error {
	tmplKey := "templates/pages/" + page + ".html"
	tmpl, ok := r.templates[tmplKey]
	if !ok {
		return fmt.Errorf("template not found: %s", tmplKey)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, layout+".html", data); err != nil {
		return fmt.Errorf("execute template %s: %w", page, err)
	}

	return nil
}

func (r *Renderer) RenderPartial(w http.ResponseWriter, page, partial string, data *PageData) error {
	tmplKey := "templates/pages/" + page + ".html"
	tmpl, ok := r.templates[tmplKey]
	if !ok {
		return fmt.Errorf("template not found: %s", tmplKey)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, partial, data); err != nil {
		return fmt.Errorf("execute partial %s: %w", partial, err)
	}

	return nil
}
