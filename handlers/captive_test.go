package handlers

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// newCaptiveE2E starts the whole front end against an empty database and
// returns its base URL, so a test can request the two front doors exactly the
// way a browser reaches them.
func newCaptiveE2E(t *testing.T, cfg Config) (string, *database.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{
		Path:          filepath.Join(t.TempDir(), "captive.db"),
		SecretKeyPath: filepath.Join(t.TempDir(), "secret.key"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	web := httptest.NewServer(New(db, tpl, cfg).Routes())
	t.Cleanup(web.Close)
	return web.URL, db
}

// TestRootServesCaptivePortalAndAdminHidesThePanel is the contract this feature
// exists for: opening the IP address of the controller shows the hotspot
// landing page, and the operator panel is one path deeper at /admin. If "/" ever
// renders the dashboard again, every guest on the Wi-Fi sees the fleet.
func TestRootServesCaptivePortalAndAdminHidesThePanel(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	root := getBody(t, http.DefaultClient, base+"/")
	if !strings.Contains(root, "Free Wi-Fi") {
		t.Errorf("GET / did not render the captive portal landing page")
	}
	if !strings.Contains(root, `href="/admin/"`) {
		t.Error("the landing page does not link to the panel at /admin/")
	}
	// The dashboard must not leak through the landing page.
	if strings.Contains(root, "Routers</a>") {
		t.Error("the captive portal page rendered the operator navigation")
	}

	panel := getBody(t, http.DefaultClient, base+"/admin/")
	if !strings.Contains(panel, "Routers</a>") {
		t.Error("GET /admin/ did not render the operator dashboard")
	}
	if strings.Contains(panel, "Free Wi-Fi") {
		t.Error("the operator dashboard rendered the captive portal")
	}
}

// TestAdminRedirectsWithoutTrailingSlash covers the bare "/admin" an operator
// types: the subtree pattern only matches "/admin/", so it needs a real
// redirect or the browser shows a directory-style listing.
func TestAdminRedirectsWithoutTrailingSlash(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := noRedirect().Get(base + "/admin")
	if err != nil {
		t.Fatalf("GET /admin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("GET /admin status = %d, want 301", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/admin/" {
		t.Errorf("GET /admin redirected to %q, want /admin/", got)
	}
}

// TestAdminMountServesTheWholePanel checks that the StripPrefix mount really is
// the full route table and not just the dashboard: a wrong prefix silently
// rewrites "/admin/routers/1" or drops it, and the operator only finds out
// when a form 404s.
func TestAdminMountServesTheWholePanel(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	for _, path := range []string{"/admin/routers", "/admin/vouchers", "/admin/sessions", "/admin/tools"} {
		page := getBody(t, http.DefaultClient, base+path)
		if !strings.Contains(page, `class="topbar"`) {
			t.Errorf("GET %s did not render a panel page", path)
		}
	}
}

// TestAdminPathsStayInsideThePanel guards the navigation: after the split, a
// link back to "/" would drop the operator on the guest landing page.
func TestAdminPathsStayInsideThePanel(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	panel := getBody(t, http.DefaultClient, base+"/admin/")
	for _, want := range []string{`href="/admin/routers"`, `href="/admin/vouchers"`, `href="/admin/"`} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel navigation is missing %s", want)
		}
	}
	if strings.Contains(panel, `href="/routers"`) {
		t.Error("the panel still links to the unmounted /routers path")
	}
}

// TestLegacyRootAdminRoutesStillResolve protects every existing bookmark, the
// dashboard's own absolute links and the REST clients: the panel is mounted
// twice, and unmounting it from the root would break all of them at once.
func TestLegacyRootAdminRoutesStillResolve(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	if page := getBody(t, http.DefaultClient, base+"/routers"); !strings.Contains(page, `class="topbar"`) {
		t.Error("GET /routers stopped working after the /admin split")
	}
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", resp.StatusCode)
	}
}

// TestRootForwardsHotspotParametersToSignIn is what makes a stock MikroTik
// hotspot work: it redirects the client to the controller with link-login,
// mac and link-orig in the query string. The landing page must not swallow
// them, or the login POST has nothing to complete the handshake with.
func TestRootForwardsHotspotParametersToSignIn(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	target := base + "/?link-login-only=%2Flogin&link-orig=http%3A%2F%2Fexample.com%2F" +
		"&mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF&ip=10.0.0.5"
	resp, err := noRedirect().Get(target)
	if err != nil {
		t.Fatalf("GET / with hotspot parameters: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := readAll(t, resp)
	if strings.Contains(body, "Free Wi-Fi") {
		t.Error("a redirected hotspot client got the landing page instead of the sign-in form")
	}
	if !strings.Contains(body, `name="voucher"`) {
		t.Error("the forwarded page is not the sign-in form")
	}
	// The parameters must survive into the form action, or the login cannot
	// tell the router which client to activate. A URL-escaped query
	// ("ip%3d10.0.0.5%26mac%3d...") means html/template mangled the attribute
	// and every login from the portal silently fails.
	if !strings.Contains(body, "link-orig=http%3A%2F%2Fexample.com%2F") ||
		!strings.Contains(body, "mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF") {
		t.Errorf("the hotspot parameters did not reach the form action intact:\n%s", formActionOf(body))
	}
}

// TestCaptivePortalGreetsAnOnlineClient proves the landing page is not a dead
// end: a device that already holds a session must be told it is online rather
// than asked for a voucher it does not need.
func TestCaptivePortalGreetsAnOnlineClient(t *testing.T) {
	base, db := newCaptiveE2E(t, Config{})
	ctx := context.Background()

	router, err := db.Routers().Create(ctx, database.Router{
		Name: "cafe", Host: "192.168.88.1", Port: 8728,
		Username: "api", Password: "pw", DefaultPortal: true,
	})
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	now := time.Now()
	if err := db.Sessions().Register(ctx, database.Session{
		RouterID: router.ID, SessionKey: "*1", Username: "guest-7",
		Address: "127.0.0.1", MACAddress: "AA:BB:CC:DD:EE:FF",
		StartedAt: now.Add(-90 * time.Minute), LastSeenAt: now,
	}, now); err != nil {
		t.Fatalf("register session: %v", err)
	}

	body := getBody(t, http.DefaultClient, base+"/")
	if !strings.Contains(body, "You are online") {
		t.Error("a client with a live session was not greeted as online")
	}
	if !strings.Contains(body, "guest-7") {
		t.Error("the online page does not show who is signed in")
	}
	if strings.Contains(body, "Free Wi-Fi") {
		t.Error("an online client was still shown the sign-in page")
	}
}

// TestDashboardAtRootRestoresTheLegacyLayout covers the escape hatch: an
// operator who upgrades and wants the old / behaviour sets DASHBOARD_AT_ROOT
// and must get the dashboard back, with the portal still reachable.
func TestDashboardAtRootRestoresTheLegacyLayout(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{DashboardAtRoot: true})

	root := getBody(t, http.DefaultClient, base+"/")
	if !strings.Contains(root, "Routers</a>") {
		t.Error("DASHBOARD_AT_ROOT did not put the dashboard back on /")
	}
	portal := getBody(t, http.DefaultClient, base+"/portal")
	if !strings.Contains(portal, "Free Wi-Fi") {
		t.Error("the captive portal is not reachable at /portal with DASHBOARD_AT_ROOT")
	}
}

// TestAdminPathIsConfigurable proves ADMIN_PATH is honoured and normalised, so
// an operator can hide the panel behind a less obvious prefix.
func TestAdminPathIsConfigurable(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{AdminPath: "panel/"})

	if body := getBody(t, http.DefaultClient, base+"/panel/"); !strings.Contains(body, "Routers</a>") {
		t.Error("a trailing slash in ADMIN_PATH broke the panel mount")
	}
	if body := getBody(t, http.DefaultClient, base+"/"); !strings.Contains(body, "Free Wi-Fi") {
		t.Error("the captive portal did not stay on / with a custom ADMIN_PATH")
	}
}

// TestCaptivePortalSurvivesAnUnresolvableRouter guards the guest experience:
// with no router registered the landing page must still render. Turning a
// missing device into a 500 would lock every guest out of the Wi-Fi.
func TestCaptivePortalSurvivesAnUnresolvableRouter(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	body := getBody(t, http.DefaultClient, base+"/")
	if !strings.Contains(body, "Free Wi-Fi") {
		t.Error("the landing page failed without a registered router")
	}
	if !strings.Contains(body, "Staff") {
		t.Error("the landing page lost its way back to the panel")
	}
}

// readAll drains a response body for the assertions below.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

// TestPortalLoginFormKeepsHotspotQueryIntact pins a bug that made every portal
// login fail silently: html/template applies urlFilter plus urlEscaper to an
// action attribute, so a plain "?ip=10.0.0.5&mac=AA:BB" was rendered as
// "?ip%3d10.0.0.5%26mac%3dAA%3ABB". The browser then POSTed one giant query
// key instead of the hotspot parameters, and the router never learned which
// client to activate.
//
// The escaping is only safe because the helper re-encodes every value with
// url.Values; a raw value spliced into the path would be an injection.
func TestPortalLoginFormKeepsHotspotQueryIntact(t *testing.T) {
	view := &portalPage{
		page: page{Title: "Sign in", CSRFToken: "t"},
		Portal: portalRequest{
			MAC:           "AA:BB:CC:DD:EE:FF",
			IP:            "10.0.0.5",
			LinkLoginOnly: "/login",
			LinkOrig:      "http://example.com/page?a=1&b=2",
		},
	}
	body := renderPage(t, "portal.html", view)

	action := formActionOf(body)
	// The template emits "&amp;" as the HTML encoding of the "&" separator,
	// which is correct and what a browser decodes. Undo it so the query can
	// be parsed the way the browser will see it.
	action = strings.ReplaceAll(action, "&amp;", "&")
	if !strings.HasPrefix(action, "/portal/login?") {
		t.Fatalf("form action = %q, want it to start with /portal/login?", action)
	}
	// The parameters must be separable, which is exactly what the browser
	// does. The old bug produced a single key, so the count is the real
	// assertion; the substring checks below are a readable failure message.
	parts := strings.Split(strings.SplitN(action, "?", 2)[1], "&")
	if len(parts) != 4 {
		t.Errorf("the action carries %d query part(s), want 4 separate parameters: %q", len(parts), action)
	}
	// And parsing the action must give the parameters back one by one.
	parsed, err := url.Parse(action)
	if err != nil {
		t.Fatalf("parse form action: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("mac"); got != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("mac = %q, want AA:BB:CC:DD:EE:FF", got)
	}
	if got := query.Get("ip"); got != "10.0.0.5" {
		t.Errorf("ip = %q, want 10.0.0.5", got)
	}
	if got := query.Get("link-orig"); got != "http://example.com/page?a=1&b=2" {
		t.Errorf("link-orig = %q, want the original URL intact", got)
	}
	// The nested "&" of link-orig must not have split into a bogus parameter.
	if query.Has("b") {
		t.Error("link-orig leaked its own query string into the action parameters")
	}
}

// TestPortalActionIsEmptySafe checks the no-parameter case: a bare form action
// with no trailing "?" is what a client that was never redirected gets.
func TestPortalActionIsEmptySafe(t *testing.T) {
	body := renderPage(t, "portal.html", &portalPage{page: page{Title: "Sign in", CSRFToken: "t"}})
	if action := formActionOf(body); action != "/portal/login" {
		t.Errorf("form action = %q, want /portal/login", action)
	}
}

// formActionOf extracts the first login form action, for a failure message.
func formActionOf(body string) string {
	idx := strings.Index(body, `action="`)
	if idx < 0 {
		return "(no form action found)"
	}
	rest := body[idx+len(`action="`):]
	if end := strings.Index(rest, `"`); end >= 0 {
		return rest[:end]
	}
	return rest
}

// noRedirect is a client that reports the redirect instead of following it.
func noRedirect() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
