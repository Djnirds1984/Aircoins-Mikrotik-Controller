package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// renderIconSnippet executes a throwaway template that calls the registered
// `icon` func, so the assertions below exercise the real TemplateFuncs() wiring
// in views.go end-to-end (registration + variadic class handling) rather than
// only the renderIcon helper.
//
// The returned string is the rendered output. A parse error, an execute error
// (e.g. `icon` not registered, or a func returning an error) or a panic is all
// turned into a test failure, which is how the "no error/panic" contract for an
// unknown icon name is verified.
func renderIconSnippet(t *testing.T, src string) string {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rendering %q panicked: %v", src, r)
		}
	}()
	tmpl, err := template.New("").Funcs(TemplateFuncs()).Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, nil); err != nil {
		t.Fatalf("execute %q: %v", src, err)
	}
	return buf.String()
}

// TestIconFuncRendersInlineSVG covers case (a): `icon "wifi"` emits a
// self-contained inline <svg> carrying the base "icon" class and aria-hidden so
// the decorative glyph is ignored by assistive tech.
func TestIconFuncRendersInlineSVG(t *testing.T) {
	got := renderIconSnippet(t, `{{icon "wifi"}}`)
	for _, want := range []string{"<svg", `class="icon"`, `aria-hidden="true"`} {
		if !strings.Contains(got, want) {
			t.Errorf("icon \"wifi\" output is missing %q; got %q", want, got)
		}
	}
}

// TestIconFuncAppendsExtraClass covers case (b): `icon "wifi" "brand-icon"`
// appends the caller-supplied modifier after the base class.
func TestIconFuncAppendsExtraClass(t *testing.T) {
	got := renderIconSnippet(t, `{{icon "wifi" "brand-icon"}}`)
	if !strings.Contains(got, "brand-icon") {
		t.Errorf("icon \"wifi\" \"brand-icon\" output is missing the extra class; got %q", got)
	}
	// The base "icon" class must be preserved alongside the modifier.
	if !strings.Contains(got, `class="icon brand-icon"`) {
		t.Errorf("icon \"wifi\" \"brand-icon\" did not keep the base icon class; got %q", got)
	}
}

// TestIconFuncUnknownNameRendersEmpty covers case (c): an unregistered name
// renders nothing and must not error or panic, so a missing glyph degrades
// gracefully instead of breaking the whole page.
func TestIconFuncUnknownNameRendersEmpty(t *testing.T) {
	got := renderIconSnippet(t, `{{icon "does-not-exist"}}`)
	if got != "" {
		t.Errorf("icon \"does-not-exist\" should render empty, got %q", got)
	}
}
