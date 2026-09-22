package handlers

import (
	"html/template"
	"testing"
)

// templateNames are the pages the router must be able to render.
var templateNames = []string{
	"dashboard.html",
	"routers.html",
	"router.html",
	"network.html",
	"sessions.html",
	"vouchers.html",
	"portal.html",
	"error.html",
}

// partialNames are the blocks the pages above include.
var partialNames = []string{"partials.html", "styles", "nav", "flash", "csrf", "voucherRows"}

// TestTemplatesParse catches the two mistakes a template edit usually makes: an
// unbalanced {{if}}/{{end}} and a {{template "x"}} naming a block that does not
// exist. Both would otherwise only surface when the page is first requested.
func TestTemplatesParse(t *testing.T) {
	tmpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse %s: %v", TemplatePattern, err)
	}
	for _, name := range templateNames {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q is missing", name)
		}
	}
	for _, name := range partialNames {
		if tmpl.Lookup(name) == nil {
			t.Errorf("partial %q is missing", name)
		}
	}
}
