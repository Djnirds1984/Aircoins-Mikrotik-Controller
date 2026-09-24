package handlers

import (
	"html/template"
	"os"
	"strings"
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

// TestDashboardScriptStructure guards the traffic monitor script against the
// corruption that once split it: a stray </script> closed the IIFE early and
// left the fetch functions as dead HTML text the browser never ran, so the
// graph could not load. There must be exactly one script block and it must
// define every function the event handlers call.
func TestDashboardScriptStructure(t *testing.T) {
	raw, err := os.ReadFile("../templates/dashboard.html")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	body := string(raw)
	open := strings.Index(body, "<script>")
	end := strings.LastIndex(body, "</script>")
	if open < 0 || end < 0 || end < open {
		t.Fatal("dashboard script block is missing")
	}
	if n := strings.Count(body, "<script>"); n != 1 {
		t.Errorf("found %d <script> tags, want exactly 1", n)
	}
	if n := strings.Count(body, "</script>"); n != 1 {
		t.Errorf("found %d </script> tags, want exactly 1", n)
	}
	script := body[open : end+len("</script>")]
	for _, want := range []string{
		"function formatBytes",
		"function formatRate",
		"function drawGraph",
		"async function loadInterfaces",
		"async function loadTrafficData",
		// Guards the "dropdown not clickable" regression: a restored router
		// selection must bootstrap the interface list on page load, stale
		// responses must be dropped, and a failed load must leave Refresh now
		// enabled so the operator can retry.
		"if(routerSelect.value)",
		"currentRouterId !== routerId",
		"refreshBtn.disabled = false;",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script block is missing %q", want)
		}
	}
}
