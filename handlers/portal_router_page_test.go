package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readAllBody reads an HTTP response body, failing the test on a read error.
func readAllBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

// TestRouterLoginPageRedirectsToThePanel covers the file that puts the panel's
// portal in front of guests at all.
//
// A stock RouterOS hotspot serves its own login.html, so without this page the
// guest sees the built-in MikroTik portal and the controller is never in the
// path.
func TestRouterLoginPageRedirectsToThePanel(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := http.Get(base + portalRouterLoginPath)
	if err != nil {
		t.Fatalf("GET %s: %v", portalRouterLoginPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 so the router can fetch it", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	page := readAllBody(t, resp)
	if !strings.Contains(page, "http-equiv=\"refresh\"") {
		t.Error("the page does not redirect, so a guest would sit on a blank page")
	}
	// The sign-in path has to be there or the redirect goes nowhere useful.
	if !strings.Contains(page, "/portal/login?") {
		t.Errorf("the redirect does not point at the sign-in page: %s", page)
	}
	// The RouterOS variables must be written LITERALLY: the router substitutes
	// them when it serves the page, not the panel now.
	for _, variable := range []string{"$(mac)", "$(ip)", "$(link-orig-esc)"} {
		if !strings.Contains(page, variable) {
			t.Errorf("the page is missing the RouterOS variable %s", variable)
		}
	}
	// It must point at the address the router actually used.
	if !strings.Contains(page, base[len("http://"):]) {
		t.Errorf("the redirect does not use the address the router reached us on: %s", page)
	}
}

// TestRouterLoginPageRejectsHostileHost pins the redirect against a spoofed Host
// header. The built URL ends up inside a meta refresh served to guests, and an
// unvalidated host there turns the hotspot into an open redirector.
func TestRouterLoginPageRejectsHostileHost(t *testing.T) {
	h := &Handler{cfg: Config{}.withDefaults()}

	hostile := []string{
		"evil.example/x",
		"evil.example\\x",
		"user@evil.example",
		"evil.example:80/path",
		"evil.example?a=b",
		"evil.example#frag",
		"evil.example\r\nX-Injected: 1",
		"ev il.example",
	}
	for _, host := range hostile {
		req := httptest.NewRequest(http.MethodGet, portalRouterLoginPath, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.PortalRouterLogin(rec, req)
		body := rec.Body.String()

		if strings.Contains(body, "evil.example/") || strings.Contains(body, "@evil.example") {
			t.Errorf("host %q leaked into the redirect: %s", host, body)
		}
		if strings.Contains(body, "X-Injected") {
			t.Errorf("host %q injected a header into the page", host)
		}
	}
}

// TestRouterLoginPageAcceptsRealHosts keeps the validation from being so strict
// that a normal board address stops working.
func TestRouterLoginPageAcceptsRealHosts(t *testing.T) {
	h := &Handler{cfg: Config{}.withDefaults()}

	good := map[string]string{
		"10.0.0.254":      "http://10.0.0.254/portal/login?",
		"10.0.0.254:8080": "http://10.0.0.254:8080/portal/login?",
		"portal.local":    "http://portal.local/portal/login?",
		"portal.local:80": "http://portal.local:80/portal/login?",
	}
	for host, want := range good {
		req := httptest.NewRequest(http.MethodGet, portalRouterLoginPath, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.PortalRouterLogin(rec, req)
		if body := rec.Body.String(); !strings.Contains(body, want) {
			t.Errorf("host %q did not produce %q", host, want)
		}
	}
}

// TestRouterLoginPageHonoursForwardedProto proves the file points at https when
// the panel is published behind TLS, instead of downgrading the guest to http.
func TestRouterLoginPageHonoursForwardedProto(t *testing.T) {
	h := &Handler{cfg: Config{}.withDefaults()}

	req := httptest.NewRequest(http.MethodGet, portalRouterLoginPath, nil)
	req.Host = "portal.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.PortalRouterLogin(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "https://portal.example.com/portal/login?") {
		t.Errorf("the redirect did not use https: %s", body)
	}
}
