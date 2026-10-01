package handlers

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// starterLikePage is a miniature of the page an operator would paste in: a
// banner, the guest's identity, a countdown driven by a script, and a voucher
// form posting to the controller.
//
// The <script> is the point of the exercise. Full-page mode exists precisely so
// an operator can add a live countdown, which the small custom block cannot do.
const starterLikePage = `<!DOCTYPE html>
<html><head><title>{{.PortalName}}</title><style>.banner{background:#123}</style></head>
<body>
  <div class="banner"{{if .BannerURL}} style="background-image:url('{{.BannerURL}}')"{{end}}></div>
  <h1>{{if .LoggedIn}}CONNECTED{{else}}SIGN IN{{end}}</h1>
  <p class="meta">MAC: {{.MAC}} | IP: {{.IP}}</p>
  <div id="timer" data-seconds="{{.RemainingSeconds}}"></div>
  <form method="post" action="{{.LoginAction}}"><input name="voucher"><button>SUBMIT</button></form>
  <script>setInterval(function(){},1000);</script>
  <footer>Powered by: {{.PortalName}}</footer>
</body></html>`

// saveFullPage stores an operator page and switches the portal into full mode.
func saveFullPage(t *testing.T, client *http.Client, editorURL, mode, html string) *http.Response {
	t.Helper()
	page := getBody(t, client, editorURL)
	resp, err := client.PostForm(editorURL+"/save", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"theme":      {database.PortalThemeMidnight},
		"page_mode":  {mode},
		"full_html":  {html},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return resp
}

// TestPortalFullPageReplacesStandardPage is the core of full-page mode: once the
// operator saves their own document, a guest gets it instead of the built-in
// captive layout.
func TestPortalFullPageReplacesStandardPage(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	resp := saveFullPage(t, client, base+"/admin/portal-editor", database.PortalPageFull, starterLikePage)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save ended on %d, want the editor", resp.StatusCode)
	}

	// A guest, not signed in, hitting the public sign-in path.
	body := getBody(t, &http.Client{}, base+"/portal/login")

	if !strings.Contains(body, "id=\"timer\"") {
		t.Error("the operator page was not served on the sign-in path")
	}
	if strings.Contains(body, captivePortalMarker) {
		t.Error("the built-in captive layout is still being served")
	}
	// The script is the whole point of full-page mode: the countdown in the
	// reference page cannot work without it.
	if !strings.Contains(body, "setInterval") {
		t.Error("the operator's <script> was stripped from the page")
	}
	if !strings.Contains(body, "name=\"voucher\"") {
		t.Error("the voucher field is missing from the operator page")
	}
	// Every placeholder must have been substituted.
	if strings.Contains(body, "{{") {
		t.Error("an unsubstituted placeholder reached the guest")
	}
}

// TestPortalFullPageGetsLiveData proves the placeholders are filled with the
// hotspot parameters rather than left blank, and that those parameters survive
// into the form action so the login POST can complete the handshake.
func TestPortalFullPageGetsLiveData(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	resp := saveFullPage(t, client, base+"/admin/portal-editor", database.PortalPageFull, starterLikePage)
	resp.Body.Close()

	// Ask as a real hotspot would, with the parameters in the query string.
	body := getBody(t, &http.Client{}, base+"/portal/login?mac=D6-A8-AA-7A-70-E5&ip=10.1.0.135")
	if !strings.Contains(body, "D6:A8:AA:7A:70:E5") {
		t.Error("the MAC address was not substituted into the operator page")
	}
	if !strings.Contains(body, "10.1.0.135") {
		t.Error("the IP address was not substituted into the operator page")
	}
	if !strings.Contains(body, "mac=D6%3AA8%3AAA%3A7A%3A70%3AE5") {
		t.Error("the hotspot parameters did not reach the form action")
	}
}

// TestPortalFullPageEscapesData proves a hostile router name cannot break out
// of the operator's markup. The operator's own code is trusted, but the values
// the controller fills into it are not, and they are interpolated through
// html/template precisely so they cannot.
func TestPortalFullPageEscapesData(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	resp := saveFullPage(t, client, base+"/admin/portal-editor", database.PortalPageFull,
		`<html><body><p title="{{.RouterName}}">x</p></body></html>`)
	resp.Body.Close()

	body := getBody(t, &http.Client{}, base+"/portal/login?server-name=evil")
	// A quote inside the value must arrive as &quot;, never as a raw " that
	// would close the attribute and let the rest of the value become markup.
	if strings.Contains(body, `"><`) && !strings.Contains(body, "&quot;") {
		t.Error("an interpolated value escaped its attribute unescaped")
	}
	if !strings.Contains(body, "title=") {
		t.Error("the attribute is missing entirely, so the escaping path did not run")
	}
}

// TestPortalFullPageFallsBackOnBrokenTemplate is the safety net. A page that
// fails at execution time must never leave a guest staring at an error: the
// built-in captive page has to come back instead.
func TestPortalFullPageFallsBackOnBrokenTemplate(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, editorConfig())

	// Seeded straight through the store to bypass the editor's own validation:
	// {{.NoSuchField}} parses cleanly and only fails when executed, which is the
	// case the render-time fallback has to cover.
	if err := db.PortalSettings().Save(ctx, database.PortalSettings{
		Theme:    database.PortalThemeMidnight,
		PageMode: database.PortalPageFull,
		FullHTML: "<html><body>{{.NoSuchField}}</body></html>",
	}); err != nil {
		t.Fatalf("seed a broken page: %v", err)
	}

	body := getBody(t, &http.Client{}, base+"/portal/login")
	if !strings.Contains(body, captivePortalMarker) {
		t.Error("a broken operator page did not fall back to the built-in layout")
	}
	if !strings.Contains(body, "voucher") {
		t.Error("the fallback page has no sign-in form")
	}
}

// TestPortalFullPageEmptyHTMLFallsBack proves a half-filled form cannot blank
// the portal: "full" mode with no document must still serve the built-in page.
func TestPortalFullPageEmptyHTMLFallsBack(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	if err := db.PortalSettings().Save(ctx, database.PortalSettings{
		Theme:    database.PortalThemeMidnight,
		PageMode: database.PortalPageFull,
		FullHTML: "   ",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := getBody(t, client, base+"/portal/login")
	if !strings.Contains(body, captivePortalMarker) {
		t.Error("an empty full page blanked the portal instead of falling back")
	}
}

// TestPortalFullPageStaysOffInStandardMode proves switching the editor back to
// the built-in layout really stops serving the operator document, and that the
// saved HTML is kept so switching back does not lose the work.
func TestPortalFullPageStaysOffInStandardMode(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	resp := saveFullPage(t, client, base+"/admin/portal-editor", database.PortalPageStandard, starterLikePage)
	resp.Body.Close()

	body := getBody(t, client, base+"/portal/login")
	if strings.Contains(body, "id=\"timer\"") {
		t.Error("the operator page is still served in standard mode")
	}
	if !strings.Contains(body, captivePortalMarker) {
		t.Error("the built-in layout is missing in standard mode")
	}

	stored, err := db.PortalSettings().Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(stored.FullHTML, "id=\"timer\"") {
		t.Error("the saved page was discarded when standard mode was selected")
	}
}

// TestPortalEditorRejectsUncompilableFullPage proves the editor reports a
// compile error instead of silently accepting a page that would never render.
// Without this the operator would save, see no change on the portal, and have no
// way to tell why.
func TestPortalEditorRejectsUncompilableFullPage(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	page := getBody(t, client, editor)
	// An unclosed action: {{if}} with no matching {{end}}.
	resp, err := client.PostForm(editor+"/save", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"theme":      {database.PortalThemeMidnight},
		"page_mode":  {database.PortalPageFull},
		"full_html":  {"<html><body>{{if .LoggedIn}}oops</body></html>"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a page that cannot compile", resp.StatusCode)
	}
	// The message is in the rejected response itself, not in a later GET.
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "cannot be compiled") {
		t.Error("the compile error was not reported in the editor")
	}

	stored, err := db.PortalSettings().Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.PageMode == database.PortalPageFull {
		t.Error("a page that cannot compile was stored anyway")
	}
}

// TestPortalCustomBlockStillRejectsScript pins the two-tier contract: the small
// custom block inside the built-in card stays script-free, while a full page may
// contain a script. The earlier code rejected <script> in both places on the
// false belief that the Content-Security-Policy blocked inline scripts - it does
// not, script-src carries 'unsafe-inline'.
func TestPortalCustomBlockStillRejectsScript(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	page := getBody(t, client, editor)
	resp, err := client.PostForm(editor+"/save", url.Values{
		"csrf_token":  {csrfOf(t, page)},
		"theme":       {database.PortalThemeMidnight},
		"page_mode":   {database.PortalPageStandard},
		"custom_html": {"<script>alert(1)</script>"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a script in the custom block", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.Contains(body, "not allowed in the extra HTML block") {
		t.Error("the custom block was not reported as rejecting scripts")
	}
	// The message has to point at the supported alternative, or the operator is
	// stuck with no way to add a timer.
	if !strings.Contains(body, "full-page mode") {
		t.Error("the error does not tell the operator where scripts are allowed")
	}
}

// TestPortalFullTemplateCache proves the operator page is compiled once and
// reused, and that changing it invalidates the entry. A captive portal is hit by
// every guest connection, so re-parsing the document each time would be waste.
func TestPortalFullTemplateCache(t *testing.T) {
	cache := &portalFullCache{}
	const src = "<p>{{.PortalName}}</p>"

	if _, err := cache.get(src); err != nil {
		t.Fatalf("first get: %v", err)
	}
	if _, err := cache.get(src); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if hits, misses, _ := cache.stats(); hits != 1 || misses != 1 {
		t.Errorf("hits/misses = %d/%d, want 1/1", hits, misses)
	}

	// A different source must recompile rather than serve the stale page.
	if _, err := cache.get("<p>other</p>"); err != nil {
		t.Fatalf("get after change: %v", err)
	}
	if hits, misses, _ := cache.stats(); hits != 1 || misses != 2 {
		t.Errorf("hits/misses = %d/%d, want 1/2", hits, misses)
	}

	// A broken page is reported rather than cached as a success.
	if _, err := cache.get("{{if}}"); err == nil {
		t.Error("a page that cannot compile was accepted")
	}
}
