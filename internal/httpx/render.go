// Package httpx holds HTTP plumbing shared by the admin panel and the captive
// portal: middleware, template rendering and small response helpers.
package httpx

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Renderer renders server side HTML. Pages share layout.html and partials.html;
// partials are also addressable on their own so handlers can return HTML
// fragments for in-page updates without a JavaScript build step.
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
}

// NewRenderer parses the template tree rooted at templatesDir inside fsys.
//
// Expected layout:
//
//	templates/layout.html        defines "layout"
//	templates/partials.html      defines named fragments such as "probe_report"
//	templates/pages/<name>.html  defines "content"
func NewRenderer(fsys fs.FS, funcs template.FuncMap) (*Renderer, error) {
	partials, err := template.New("partials").Funcs(funcs).ParseFS(fsys, "templates/partials.html")
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}

	files, err := fs.Glob(fsys, "templates/pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("list page templates: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no page templates found in templates/pages")
	}

	pages := make(map[string]*template.Template, len(files))
	for _, file := range files {
		name := strings.TrimSuffix(path.Base(file), ".html")
		tmpl, err := template.New("layout").
			Funcs(funcs).
			ParseFS(fsys, "templates/layout.html", "templates/partials.html", file)
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		pages[name] = tmpl
	}

	return &Renderer{pages: pages, partials: partials}, nil
}

// PageNames lists the parsed page templates, which is useful in tests.
func (r *Renderer) PageNames() []string {
	out := make([]string, 0, len(r.pages))
	for name := range r.pages {
		out = append(out, name)
	}
	return out
}

// RenderPage writes a full page through the shared layout.
func (r *Renderer) RenderPage(w http.ResponseWriter, name string, data any) error {
	tmpl, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("unknown page template %q", name)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(w, "layout", data)
}

// RenderPartial writes a named fragment with status 200.
func (r *Renderer) RenderPartial(w http.ResponseWriter, name string, data any) error {
	return r.RenderPartialStatus(w, http.StatusOK, name, data)
}

// RenderPartialStatus writes a named fragment with an explicit status code.
func (r *Renderer) RenderPartialStatus(w http.ResponseWriter, status int, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	return r.partials.ExecuteTemplate(w, name, data)
}
