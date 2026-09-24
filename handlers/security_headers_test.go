package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestSecurityHeadersAllowDashboardScript pins the two directives the
// dashboard cannot live without. default-src is the fallback for script-src
// and connect-src, so default-src 'none' without them made every browser
// refuse to execute the inline monitor script and reject its same-origin
// fetch() calls — the interface dropdown stayed disabled forever even
// though the JavaScript itself was correct. Incognito and rebuilds could
// not help; only the header could.
func TestSecurityHeadersAllowDashboardScript(t *testing.T) {
	h := &Handler{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	h.securityHeaders(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("next handler not invoked: status = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy header missing")
	}
	// Everything else stays locked down.
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP %q lost default-src 'none'", csp)
	}
	// The dashboard ships its logic as one inline <script> block.
	if !strings.Contains(csp, "script-src") || !strings.Contains(csp, "'unsafe-inline'") {
		t.Errorf("CSP %q blocks the dashboard's inline script", csp)
	}
	// loadInterfaces/loadTraffic fetch('/api/v1/...') on the same origin.
	if !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("CSP %q blocks same-origin fetch()", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestDashboardShipsInlineScript is the other half of the contract above:
// if the template ever moves to an external or non-inline script, the
// script-src 'unsafe-inline' allowance must be revisited deliberately
// rather than discovered as a dead dropdown in the browser.
func TestDashboardShipsInlineScript(t *testing.T) {
	raw, err := os.ReadFile("../templates/dashboard.html")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "<script>") {
		t.Fatal("dashboard no longer carries an inline <script> block; revisit script-src in securityHeaders")
	}
}
