package handlers

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestGuestChainHasNoFourOhFours walks every URL a guest, a phone's captive
// probe, or an installing router can hit and demands an answer that is not a
// 404.
//
// This exists because the failure mode that burned the operator most than once
// was indistinguishable from a dead panel: some hop in the chain
// (router file -> redirect target -> landing page -> probe) answered 404 or
// served stale bytes, and nothing in the test suite walked the chain the way a
// real guest does. Each row states what the answer proves:
//
//   - "/"                        the bare panel address a plain hotspot
//     redirect lands on.
//   - "/portal", "/portal/"      the same panel addressed with its portal
//     path, which is what some hotspot "hostname" settings produce.
//   - "/login", "/index.html"    entry aliases a browser or saved link uses.
//   - "/portal/login"            the redirect hand-off target and its
//     parameterised form, which is what the installed router page points at.
//   - "/portal/router-login.html" the exact URL the router fetches to install
//     its login page: it must be the marker-carrying handoff, and must never
//     contain template syntax, because whatever bytes land here are what the
//     router stores verbatim as login.html.
//   - "/portal/session"          the guest's own status link.
func TestGuestChainHasNoFourOhFours(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	// A client that reports redirects instead of following them, so every
	// hop is judged on its own answer.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	t.Run("router-login page is the clean handoff", func(t *testing.T) {
	 resp, err := client.Get(base + portalRouterLoginPath)
		if err != nil {
			t.Fatalf("GET %s: %v", portalRouterLoginPath, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d, want 200: the router stores exactly this as login.html",
				portalRouterLoginPath, resp.StatusCode)
		}
		text := string(body)
		if !strings.Contains(text, portalHandoffMarker) {
			t.Errorf("%s is missing the handoff marker, so installs cannot verify it", portalRouterLoginPath)
		}
		if strings.Contains(text, "{{") {
			t.Errorf("%s contains raw template syntax; it would scramble every guest page it is installed on", portalRouterLoginPath)
		}
	})

	walks := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/portal"},
		{http.MethodGet, "/portal/"},
		{http.MethodGet, "/login"},
		{http.MethodGet, "/login.html"},
		{http.MethodGet, "/index.html"},
		{http.MethodGet, "/portal/login"},
		{http.MethodGet, "/portal/login?ip=10.0.0.5&mac=AA:BB:CC:DD:EE:FF&link-login-only=http://10.0.0.1/login&link-orig=http://example.com/&server-name=hotspot1"},
		{http.MethodGet, portalRouterLoginPath},
		{http.MethodGet, "/portal/session"},
		{http.MethodGet, "/portal/status?ip=10.0.0.5&mac=AA:BB:CC:DD:EE:FF&server-name=hotspot1"},
	}
	for _, walk := range walks {
		walk := walk
		t.Run(walk.method+" "+walk.path, func(t *testing.T) {
			req, err := http.NewRequest(walk.method, base+walk.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", walk.method, walk.path, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("%s %s answered 404: a guest following this URL sees a dead portal",
					walk.method, walk.path)
			}
			if resp.StatusCode >= 500 {
				t.Fatalf("%s %s answered %d: %s", walk.method, walk.path, resp.StatusCode, firstLine(string(body)))
			}
			// No guest hop may leak template source: only the router ever
			// stores raw files, and the row above owns that contract.
			if strings.Contains(string(body), "{{") {
				t.Errorf("%s %s served raw template syntax", walk.method, walk.path)
			}
		})
	}
}
