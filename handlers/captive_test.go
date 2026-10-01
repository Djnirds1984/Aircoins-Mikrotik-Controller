package handlers

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/cookiejar"
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
// way a browser reaches them. The returned client is signed in, since the
// panel is behind a session guard.
func newCaptiveE2E(t *testing.T, cfg Config) (string, *database.DB) {
	t.Helper()
	base, db := newUnauthE2E(t, cfg)
	return base, db
}

// newUnauthE2E starts the front end and returns a plain, not-yet-signed-in
// browser. Tests about the login form itself use it; everything else should
// use newCaptiveE2E so it exercises the panel as a signed-in operator does.
func newUnauthE2E(t *testing.T, cfg Config) (string, *database.DB) {
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

	// Every test needs an operator account, otherwise the panel is (correctly)
	// unreachable and the test would only prove the login form renders.
	if _, err := db.AdminUsers().EnsureAdminUser(ctx, testAdminUser, testAdminPass); err != nil {
		t.Fatalf("create admin user: %v", err)
	}

	tpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	web := httptest.NewServer(New(db, tpl, cfg).Routes())
	t.Cleanup(web.Close)
	return web.URL, db
}

// Test credentials shared by the end-to-end tests. The password clears the
// 10 character minimum enforced by the real store.
const (
	testAdminUser = "tester"
	testAdminPass = "correct-horse-battery"
)

// captivePortalMarker identifies the guest-facing captive portal document.
//
// These tests used to match page copy ("Free Wi-Fi", "Connect to the
// internet"). That made the landing page's wording load-bearing: the kiosk
// redesign changed the wording, and five unrelated end-to-end tests failed
// because of a heading. The intent of every one of them is "this guest got the
// portal, not the operator panel", so they now key on a structural attribute
// the template carries for exactly that purpose.
const captivePortalMarker = `data-page="captive-portal"`

// captiveOnlineMarker identifies the portal's connected state, for the same
// reason: it is a structural attribute rather than the word "online".
const captiveOnlineMarker = `data-connected="true"`

// signInAt walks the real login form so the session cookie is issued exactly
// the way it is in production, CSRF token and all.
//
// The assertion is on the final URL, not the status: the client follows the
// 303 to the dashboard, so a successful sign-in reports the dashboard's 200.
func signInAt(t *testing.T, base, adminPath string, client *http.Client) {
	t.Helper()
	page := getBody(t, client, base+adminPath+"/login")
	resp, err := client.PostForm(base+adminPath+"/login", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"username":   {testAdminUser},
		"password":   {testAdminPass},
		"next":       {adminPath + "/"},
	})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in ended on status %d, want the dashboard (200)", resp.StatusCode)
	}
	if !strings.HasSuffix(resp.Request.URL.Path, adminPath+"/") {
		t.Fatalf("sign in landed on %q, want the panel dashboard", resp.Request.URL.Path)
	}
}

// authedClientFor returns an http.Client that already carries a valid panel
// session, for tests that call the API with plain http.Get/http.Post and do
// not want to walk the login form.
//
// The session is minted through the real store, so it exercises the same path
// a browser login does.
func authedClientFor(t *testing.T, base string, db *database.DB) *http.Client {
	t.Helper()
	user, err := db.AdminUsers().Get(context.Background())
	if err != nil {
		t.Fatalf("load admin user: %v", err)
	}
	token, expires, err := db.AdminUsers().CreateSession(context.Background(), user.ID, "test", time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	jar.SetCookies(baseURL, []*http.Cookie{{
		Name: adminSessionCookie, Value: token, Path: "/", Expires: expires,
	}})
	return &http.Client{Jar: jar}
}

// signedInBrowser returns a cookie-carrying client that has already signed in
// at the default /admin prefix.
func signedInBrowser(t *testing.T, base string) *http.Client {
	t.Helper()
	return signedInBrowserAt(t, base, "/admin")
}

// signedInBrowserAt signs in against an explicit panel prefix, for the tests
// that mount the panel somewhere other than /admin.
func signedInBrowserAt(t *testing.T, base, adminPath string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}
	signInAt(t, base, adminPath, client)
	return client
}

// TestRootServesCaptivePortalAndAdminHidesThePanel is the contract this feature
// exists for: opening the IP address of the controller shows the hotspot
// landing page, and the operator panel is one path deeper at /admin. If "/" ever
// renders the dashboard again, every guest on the Wi-Fi sees the fleet.
func TestRootServesCaptivePortalAndAdminHidesThePanel(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	root := getBody(t, signedInBrowser(t, base), base+"/")
	if !strings.Contains(root, captivePortalMarker) {
		t.Errorf("GET / did not render the captive portal landing page")
	}
	if !strings.Contains(root, `href="/admin/"`) {
		t.Error("the landing page does not link to the panel at /admin/")
	}
	// The dashboard must not leak through the landing page.
	if strings.Contains(root, "Routers</a>") {
		t.Error("the captive portal page rendered the operator navigation")
	}

	browser := signedInBrowser(t, base)
	panel := getBody(t, browser, base+"/admin/")
	if !strings.Contains(panel, "Routers</a>") {
		t.Error("GET /admin/ did not render the operator dashboard")
	}
	if strings.Contains(panel, captivePortalMarker) {
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
	browser := signedInBrowser(t, base)

	for _, path := range []string{"/admin/routers", "/admin/vouchers", "/admin/devices", "/admin/tools"} {
		page := getBody(t, browser, base+path)
		if !strings.Contains(page, `class="topbar"`) {
			t.Errorf("GET %s did not render a panel page", path)
		}
	}
}

// TestAdminPathsStayInsideThePanel guards the navigation: after the split, a
// link back to "/" would drop the operator on the guest landing page.
func TestAdminPathsStayInsideThePanel(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	browser := signedInBrowser(t, base)
	panel := getBody(t, browser, base+"/admin/")
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

	if page := getBody(t, signedInBrowser(t, base), base+"/routers"); !strings.Contains(page, `class="topbar"`) {
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
	if !strings.Contains(body, captiveOnlineMarker) {
		t.Error("a client with a live session was not greeted as online")
	}
	if !strings.Contains(body, "guest-7") {
		t.Error("the online page does not show who is signed in")
	}
	// The sign-in affordances must be gone for a connected client. Offering
	// someone who is already online an "Insert coin" button is how a kiosk
	// talks a customer into paying twice for time they already have, so this is
	// asserted on the actual controls rather than on the document type - both
	// states render the same page.
	if strings.Contains(body, `name="voucher"`) {
		t.Error("an online client was still offered the voucher form")
	}
	if strings.Contains(body, `id="coin-tab"`) {
		t.Error("an online client was still offered the coin slot")
	}
}

// TestPortalTrialClaimRedirectsToTheHotspotLogin covers the free-trial grant:
// a POST carrying trial=1 must send the guest's browser to the hotspot's own
// login endpoint with empty credentials, which is what starts a RouterOS trial.
// The API login refuses blank credentials ("username is missing"), so the grant
// is a redirect, not a server-side call. link-login-only is present, so no
// router dial is needed.
func TestPortalTrialClaimRedirectsToTheHotspotLogin(t *testing.T) {
	base, db := newCaptiveE2E(t, Config{})
	if _, err := db.Routers().Create(context.Background(), database.Router{
		Name: "cafe", Host: "192.168.88.1", Port: 8728,
		Username: "api", Password: "pw", DefaultPortal: true,
	}); err != nil {
		t.Fatalf("create router: %v", err)
	}

	resp, err := noRedirect().PostForm(base+portalLoginPath, url.Values{
		"trial":           {"1"},
		"ip":              {"192.168.88.55"},
		"mac":             {"AA:BB:CC:DD:EE:FF"},
		"link-login-only": {"http://192.168.88.1/login"},
		"link-orig":       {"http://example.com/"},
	})
	if err != nil {
		t.Fatalf("POST trial claim: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("trial claim status = %d, want a 303 redirect", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "http://192.168.88.1/login?") {
		t.Fatalf("the trial claim did not redirect to the hotspot login URL: %q", loc)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	query := parsed.Query()
	if values, ok := query["username"]; !ok || values[0] != "" {
		t.Errorf("the trial redirect must carry an empty username, got %q", values)
	}
	if values, ok := query["password"]; !ok || values[0] != "" {
		t.Errorf("the trial redirect must carry an empty password, got %q", values)
	}
	if got := query.Get("dst"); got != "http://example.com/" {
		t.Errorf("dst = %q, want the guest's original destination", got)
	}
}

// TestDashboardAtRootRestoresTheLegacyLayout covers the escape hatch: an
// operator who upgrades and wants the old / behaviour sets DASHBOARD_AT_ROOT
// and must get the dashboard back, with the portal still reachable.
func TestDashboardAtRootRestoresTheLegacyLayout(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{DashboardAtRoot: true})

	root := getBody(t, signedInBrowser(t, base), base+"/")
	if !strings.Contains(root, "Routers</a>") {
		t.Error("DASHBOARD_AT_ROOT did not put the dashboard back on /")
	}
	portal := getBody(t, http.DefaultClient, base+"/portal")
	if !strings.Contains(portal, captivePortalMarker) {
		t.Error("the captive portal is not reachable at /portal with DASHBOARD_AT_ROOT")
	}
}

// TestAdminPathIsConfigurable proves ADMIN_PATH is honoured and normalised, so
// an operator can hide the panel behind a less obvious prefix.
func TestAdminPathIsConfigurable(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{AdminPath: "panel/"})

	if body := getBody(t, signedInBrowserAt(t, base, "/panel"), base+"/panel/"); !strings.Contains(body, "Routers</a>") {
		t.Error("a trailing slash in ADMIN_PATH broke the panel mount")
	}
	if body := getBody(t, http.DefaultClient, base+"/"); !strings.Contains(body, captivePortalMarker) {
		t.Error("the captive portal did not stay on / with a custom ADMIN_PATH")
	}
}

// TestCaptivePortalSurvivesAnUnresolvableRouter guards the guest experience:
// with no router registered the landing page must still render. Turning a
// missing device into a 500 would lock every guest out of the Wi-Fi.
func TestCaptivePortalSurvivesAnUnresolvableRouter(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	body := getBody(t, http.DefaultClient, base+"/")
	if !strings.Contains(body, captivePortalMarker) {
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

// TestPortalVoucherLoginCarriesHotspotParameters drives the real browser round
// trip, which the rendering tests above never did.
//
// The sign-in form puts the hotspot parameters in the form ACTION
// ("/portal/login?mac=...&ip=..."), and the browser posts the voucher in the
// body. The handler only looked at r.PostForm, which is body-only, so the
// parameters were dropped and every real voucher login answered "This login page
// was opened directly" - a guest who was redirected by the hotspot and typed a
// valid code could never get online.
func TestPortalVoucherLoginCarriesHotspotParameters(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	// A guest redirected by the hotspot.
	signIn := base + "/portal/login?mac=AA-BB-CC-DD-EE-FF&ip=10.5.50.42" +
		"&link-login=http://10.0.0.1/login&link-orig=http://example.com/"
	page := getBody(t, &http.Client{}, signIn)
	action := strings.ReplaceAll(formActionOf(page), "&amp;", "&")
	if !strings.HasPrefix(action, "/portal/login?") {
		t.Fatalf("form action = %q, want the hotspot parameters in the query", action)
	}

	// The browser posts the body to that action, parameters and all.
	resp, err := (&http.Client{}).PostForm(base+action, url.Values{
		"voucher": {"AIR-2X4Q-9BNM"},
	})
	if err != nil {
		t.Fatalf("post voucher: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if strings.Contains(body, "opened directly") {
		t.Fatal("a redirected guest who typed a voucher was told the page was opened directly")
	}
}

// TestPortalVoucherLoginFallsBackToTheClientAddress covers the hotspot that
// redirects without parameters at all.
//
// Plenty of MikroTik setups point the redirect at a bare URL, so the sign-in
// page arrives with no mac and no ip. The guest's address is still known: it is
// the source address of the request itself. Refusing the login instead would
// leave the customer holding a paid voucher and no way to use it.
func TestPortalVoucherLoginFallsBackToTheClientAddress(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	// No hotspot parameters anywhere: neither query nor body.
	page := getBody(t, &http.Client{}, base+"/portal/login")
	action := strings.ReplaceAll(formActionOf(page), "&amp;", "&")

	resp, err := (&http.Client{}).PostForm(base+action, url.Values{
		"voucher": {"AIR-2X4Q-9BNM"},
	})
	if err != nil {
		t.Fatalf("post voucher: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if strings.Contains(body, "opened directly") {
		t.Fatal("a guest with a voucher was refused because the redirect carried no parameters")
	}
}

// TestCaptiveWelcomeVoucherBoxCarriesParameters covers the voucher box on the
// welcome page, which is what a guest sees at "/" or "/login".
//
// Its form used to be hardcoded to action="/portal/login" with no parameters, so
// the code typed there could never be tied to the guest's device even when the
// hotspot had supplied them.
func TestCaptiveWelcomeVoucherBoxCarriesParameters(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	// The welcome page is only reached with an empty request by design, so the
	// parameters are exercised through the sign-in page that shares the helper.
	page := getBody(t, &http.Client{},
		base+"/portal/login?mac=AA-BB-CC-DD-EE-FF&ip=10.5.50.42")
	action := strings.ReplaceAll(formActionOf(page), "&amp;", "&")

	if !strings.Contains(action, "mac=") || !strings.Contains(action, "ip=") {
		t.Errorf("form action %q does not carry the hotspot parameters", action)
	}
}

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
	body := renderPage(t, "captive.html", captivePageFromPortal(view))

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
	body := renderPage(t, "captive.html", captivePageFromPortal(&portalPage{page: page{Title: "Sign in", CSRFToken: "t"}}))
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
