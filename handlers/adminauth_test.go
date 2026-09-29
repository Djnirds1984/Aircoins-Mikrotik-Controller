package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// TestAdminPanelRequiresASession is the whole point of the feature: the panel
// and its API must not serve a single byte to an anonymous visitor. Before
// this, /admin/ handed the entire router fleet to anyone who typed the IP.
func TestAdminPanelRequiresASession(t *testing.T) {
	base, _ := newUnauthE2E(t, Config{})

	guarded := []string{
		"/admin/", "/admin/routers", "/admin/vouchers", "/admin/sessions",
		"/admin/settings", "/admin/portal-editor", "/routers", "/vouchers", "/api/v1/routers",
	}
	for _, path := range guarded {
		resp, err := noRedirect().Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if path == "/api/v1/routers" {
			// A JSON caller gets 401, not a redirect to an HTML form.
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET %s status = %d, want 401 for a machine client", path, resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s status = %d, want 303 to the login form", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/admin/login") {
			t.Errorf("GET %s redirected to %q, want the login form", path, loc)
		}
	}
}

// TestCaptivePortalStaysOpenWithoutASession is the other half: locking the
// panel must not lock paying guests out of the Wi-Fi.
func TestCaptivePortalStaysOpenWithoutASession(t *testing.T) {
	base, _ := newUnauthE2E(t, Config{})

	for _, path := range []string{"/", "/portal/login", "/portal/status", "/healthz", "/admin/login"} {
		resp, err := noRedirect().Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 for an anonymous guest", path, resp.StatusCode)
		}
	}
}

// TestLoginRejectsWrongCredentials checks the sad path, and that the error
// never says which half was wrong.
func TestLoginRejectsWrongCredentials(t *testing.T) {
	base, _ := newUnauthE2E(t, Config{})

	// The page and the POST must come from the same client: the CSRF token is
	// a double submit, so the cookie set by the GET has to be in this jar.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body := getBody(t, client, base+"/admin/login")

	resp, err := client.PostForm(base+"/admin/login", url.Values{
		"csrf_token": {csrfOf(t, body)},
		"username":   {testAdminUser},
		"password":   {"definitely-wrong"},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(readAll(t, resp), "Wrong operator name or password") {
		t.Error("the login form did not report the failure")
	}
	// No session may have been issued.
	for _, cookie := range jar.Cookies(mustParseURL(t, base)) {
		if cookie.Name == adminSessionCookie {
			t.Error("a failed login issued a session cookie")
		}
	}
}

// TestBruteForceLocksTheAccount proves the escalating lockout actually engages
// and that it applies to the account, not just one address: an attacker
// spreading guesses over many IPs must not get a fresh budget each time.
func TestBruteForceLocksTheAccount(t *testing.T) {
	// The guard is driven directly so the test exercises the real policy
	// without spending a PBKDF2 derivation per simulated attempt.
	guard := defaultLoginGuard()
	guard.maxAttempts = 3
	guard.baseDelay = time.Minute
	guard.maxDelay = time.Hour

	// maxAttempts failures are tolerated; the one after that locks. This is
	// the behaviour the settings page documents ("after 5 consecutive
	// failures"), so the threshold itself is part of the contract.
	for i := 1; i < guard.maxAttempts; i++ {
		if delay := guard.fail(testAdminUser); delay != 0 {
			t.Fatalf("failure %d of %d locked the account early (delay %s)", i, guard.maxAttempts, delay)
		}
	}
	if delay := guard.fail(testAdminUser); delay < time.Minute {
		t.Fatalf("the account was not locked after %d failures, delay = %s", guard.maxAttempts, delay)
	}
	// A different address must be refused too: the lock is per account.
	if ok, wait := guard.check("10.0.0.99", testAdminUser); ok || wait <= 0 {
		t.Error("a second address got a fresh budget against a locked account")
	}
	// Capitalisation must not buy a new budget.
	if ok, _ := guard.check("10.0.0.99", strings.ToUpper(testAdminUser)); ok {
		t.Error("the lockout was bypassed by changing the case of the name")
	}
	// A good login clears the history.
	guard.succeed(testAdminUser)
	if ok, _ := guard.check("10.0.0.99", testAdminUser); !ok {
		t.Error("a successful login did not clear the failure history")
	}
}

// TestLoginGuardBacksOffExponentially pins the growth of the delay, so a
// distributed attack cannot simply wait out a flat 15 seconds.
func TestLoginGuardBacksOffExponentially(t *testing.T) {
	guard := defaultLoginGuard()
	guard.maxAttempts = 2
	guard.baseDelay = 10 * time.Second
	guard.maxDelay = time.Minute

	guard.fail("admin") // count 1, under the threshold
	first := guard.fail("admin")
	guard.fail("admin") // count 3
	third := guard.fail("admin")

	if first <= 0 || third <= first {
		t.Fatalf("delays did not grow: first=%s third=%s", first, third)
	}
	// And it must stay capped, or a long campaign locks an account for days.
	var last time.Duration
	for i := 0; i < 40; i++ {
		last = guard.fail("admin")
	}
	if last > guard.maxDelay {
		t.Errorf("delay %s exceeded the cap %s", last, guard.maxDelay)
	}
}

// TestLoginEndpointThrottlesAfterRepeatedFailures drives the real endpoint: a
// burst of wrong passwords must start answering 429 rather than checking a
// password forever.
func TestLoginEndpointThrottlesAfterRepeatedFailures(t *testing.T) {
	base, _ := newUnauthE2E(t, Config{})
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	throttled := false
	for i := 0; i < 25; i++ {
		body := getBody(t, client, base+"/admin/login")
		resp, err := client.PostForm(base+"/admin/login", url.Values{
			"csrf_token": {csrfOf(t, body)},
			"username":   {testAdminUser},
			"password":   {"wrong-" + time.Now().Format("150405.000000000")},
		})
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled = true
			if resp.Header.Get("Retry-After") == "" {
				t.Error("a 429 without a Retry-After header")
			}
			break
		}
	}
	if !throttled {
		t.Error("25 wrong passwords were never throttled")
	}
}

// TestSettingsChangesTheCredentials walks the real SETTINGS page: change the
// name and password, then prove the old ones no longer work and the new ones
// do. It also checks the sessions are revoked, including the current one.
func TestSettingsChangesTheCredentials(t *testing.T) {
	base, db := newUnauthE2E(t, Config{})
	browser := signedInBrowser(t, base)

	page := getBody(t, browser, base+"/admin/settings")
	if !strings.Contains(page, "Change credentials") {
		t.Fatal("the settings page did not render the credentials form")
	}

	resp, err := browser.PostForm(base+"/admin/settings/credentials", url.Values{
		"csrf_token":       {csrfOf(t, page)},
		"current_password": {testAdminPass},
		"username":         {"newoperator"},
		"new_password":     {"a-brand-new-secret"},
		"confirm_password": {"a-brand-new-secret"},
	})
	if err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	defer resp.Body.Close()
	// The change signs the operator out, so the browser ends on the login form.
	if !strings.Contains(resp.Request.URL.Path, "/admin/login") {
		t.Fatalf("after changing credentials the browser is on %q, want the login form", resp.Request.URL.Path)
	}

	ctx := context.Background()
	if _, ok, _ := db.AdminUsers().VerifyPassword(ctx, "newoperator", "a-brand-new-secret"); !ok {
		t.Error("the new credentials do not verify")
	}
	if _, ok, _ := db.AdminUsers().VerifyPassword(ctx, "newoperator", testAdminPass); ok {
		t.Error("the OLD password still verifies after the change")
	}
	// Every session must be gone, so a stolen cookie dies with the change.
	n, err := db.AdminUsers().CountUserSessions(ctx, 1)
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 0 {
		t.Errorf("%d session(s) survived a credential change, want 0", n)
	}
}

// TestSettingsRefusesWithoutTheCurrentPassword is the anti-hijack check: a
// stolen session must not be enough to take the panel over permanently.
func TestSettingsRefusesWithoutTheCurrentPassword(t *testing.T) {
	base, db := newUnauthE2E(t, Config{})
	browser := signedInBrowser(t, base)

	page := getBody(t, browser, base+"/admin/settings")
	resp, err := browser.PostForm(base+"/admin/settings/credentials", url.Values{
		"csrf_token":       {csrfOf(t, page)},
		"current_password": {"not-my-password"},
		"username":         {"attacker"},
		"new_password":     {"attacker-password"},
		"confirm_password": {"attacker-password"},
	})
	if err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when the current password is wrong", resp.StatusCode)
	}
	if !strings.Contains(readAll(t, resp), "not your current password") {
		t.Error("the form did not explain that the current password was wrong")
	}
	if _, ok, _ := db.AdminUsers().VerifyPassword(context.Background(), testAdminUser, testAdminPass); !ok {
		t.Error("the original credentials were damaged by a refused change")
	}
}

// TestSettingsRejectsShortAndMismatchedPasswords keeps the stored hash strong
// even when the form is bypassed.
func TestSettingsRejectsShortAndMismatchedPasswords(t *testing.T) {
	base, _ := newUnauthE2E(t, Config{})
	browser := signedInBrowser(t, base)

	cases := []struct {
		name       string
		newPass    string
		confirm    string
		wantPhrase string
	}{
		{"too short", "short", "short", "at least"},
		{"mismatch", "long-enough-secret", "long-enough-other", "do not match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := getBody(t, browser, base+"/admin/settings")
			resp, err := browser.PostForm(base+"/admin/settings/credentials", url.Values{
				"csrf_token":       {csrfOf(t, page)},
				"current_password": {testAdminPass},
				"username":         {testAdminUser},
				"new_password":     {tc.newPass},
				"confirm_password": {tc.confirm},
			})
			if err != nil {
				t.Fatalf("save credentials: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
			if !strings.Contains(readAll(t, resp), tc.wantPhrase) {
				t.Errorf("the form did not explain the problem (%q)", tc.wantPhrase)
			}
		})
	}
}

// TestLogoutRevokesTheSession proves logout really kills the cookie instead of
// only clearing it in the browser.
func TestLogoutRevokesTheSession(t *testing.T) {
	base, _ := newUnauthE2E(t, Config{})
	browser := signedInBrowser(t, base)

	// The sign-out button lives on the settings page, which is also the only
	// page that carries a CSRF token, so that is where the form is driven from.
	page := getBody(t, browser, base+"/admin/settings")
	resp, err := browser.PostForm(base+"/admin/logout", url.Values{
		"csrf_token": {csrfOf(t, page)},
	})
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	resp.Body.Close()

	after := noRedirect()
	after.Jar = browser.Jar
	got, err := after.Get(base + "/admin/routers")
	if err != nil {
		t.Fatalf("GET after logout: %v", err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout the panel answered %d, want a redirect to the login form", got.StatusCode)
	}
}

// TestSanitizeNextBlocksOpenRedirects covers the phishing shape: an attacker
// sends the operator to the login page with a foreign "next", and the panel
// must not hand them over after a successful sign-in.
func TestSanitizeNextBlocksOpenRedirects(t *testing.T) {
	hostile := []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"http://evil.example",
		"/\\evil.example",
		"javascript:alert(1)",
		"admin/routers",
		"https://evil.example\nSet-Cookie: x=1",
	}
	for _, raw := range hostile {
		if got := sanitizeNext(raw, "/admin"); got != "/admin/" {
			t.Errorf("sanitizeNext(%q) = %q, want the safe /admin/", raw, got)
		}
	}
	// Legitimate in-site targets must survive untouched.
	for _, raw := range []string{"/admin/routers", "/admin/settings", "/routers/1?tab=servers"} {
		if got := sanitizeNext(raw, "/admin"); got != raw {
			t.Errorf("sanitizeNext(%q) = %q, want it unchanged", raw, got)
		}
	}
}

// TestSessionTokensAreStoredHashed guards the database: a stolen .db file must
// not contain a usable bearer token.
func TestSessionTokensAreStoredHashed(t *testing.T) {
	base, db := newUnauthE2E(t, Config{})
	signedInBrowser(t, base)

	rows, err := db.SQL().Query(`SELECT token_hash FROM admin_sessions`)
	if err != nil {
		t.Fatalf("read sessions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if len(stored) != 64 {
			t.Errorf("stored token is %d chars, want a 64 char SHA-256 hex digest", len(stored))
		}
	}
}

// TestExpiredSessionIsRefused keeps a stale cookie from being a permanent key.
func TestExpiredSessionIsRefused(t *testing.T) {
	base, db := newUnauthE2E(t, Config{})
	user, err := db.AdminUsers().Get(context.Background())
	if err != nil {
		t.Fatalf("load admin user: %v", err)
	}
	token, _, err := db.AdminUsers().CreateSession(context.Background(), user.ID, "test", -time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(mustParseURL(t, base), []*http.Cookie{{
		Name: adminSessionCookie, Value: token, Path: "/",
	}})
	client := noRedirect()
	client.Jar = jar

	got, err := client.Get(base + "/admin/routers")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusSeeOther {
		t.Errorf("an expired session was accepted: status %d, want a redirect to login", got.StatusCode)
	}
}

// TestWeakPasswordsAreRejectedAtTheStore proves the minimum length is enforced
// below the HTTP layer, so no other caller can create a weak account.
func TestWeakPasswordsAreRejectedAtTheStore(t *testing.T) {
	_, db := newUnauthE2E(t, Config{})
	err := db.AdminUsers().Create(context.Background(), "second", "short")
	if !errors.Is(err, database.ErrWeakPassword) {
		t.Errorf("Create with a 5 character password returned %v, want ErrWeakPassword", err)
	}
	if _, err := db.AdminUsers().FindByUsername(context.Background(), "second"); !errors.Is(err, database.ErrNotFound) {
		t.Error("a rejected account was still created")
	}
}
