package handlers

import (
	"net/http"
	"net/url"
	"strings"
)

// isPublicPath reports whether a path is reachable without a panel session.
//
// Guest-facing endpoints must stay open or a paying customer could not sign in
// to the Wi-Fi. The list is deliberately narrow: the captive portal endpoints
// and the panel's own sign-in page. Everything else, the REST API included,
// requires a session.
//
// The path is compared after the /admin prefix has been stripped, so the same
// rule holds for /admin/portal/login.
func (h *Handler) isPublicPath(r *http.Request) bool {
	switch h.trimAdminPath(r.URL.Path) {
	case "/portal/login", "/portal/status", portalBackgroundPath, "/login", "/logout":
		return true
	}
	return false
}

// requireAuth guards the operator panel.
//
// Unauthenticated browser requests are redirected to the login form with a
// ?next= so the operator lands back where they were aiming. Machine clients
// get 401 instead of a redirect, because a JSON caller following one would
// receive a login page where it expected data.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.isPublicPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := h.adminSession(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if wantsJSON(r) {
			w.Header().Set("WWW-Authenticate", `Session realm="aircoins"`)
			h.writeAPIError(w, http.StatusUnauthorized, "unauthorized", "sign in to the panel first")
			return
		}
		target := sanitizeNext(r.URL.RequestURI(), h.cfg.AdminPath)
		http.Redirect(w, r, h.cfg.AdminPath+"/login?next="+url.QueryEscape(target), http.StatusSeeOther)
	})
}

// wantsJSON reports whether the caller is a machine rather than a browser.
func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	return r.Header.Get("X-Requested-With") == "fetch"
}

// sanitizeNext keeps post-login redirects on this host.
//
// An unvalidated "next" is an open redirect: an attacker sends an operator to
// /admin/login?next=https://evil.example, the operator types real credentials,
// and the panel hands them straight to another site - credentials included.
func sanitizeNext(raw, adminPath string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return adminPath + "/"
	}
	// Reject anything carrying a scheme or an authority. "//evil.example" is
	// a protocol-relative URL and resolves to another host, which url.Parse
	// alone does not flag as absolute.
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		return adminPath + "/"
	}
	if !strings.HasPrefix(raw, "/") {
		return adminPath + "/"
	}
	// Browsers normalise backslashes to slashes, so "/\evil.example" would
	// otherwise escape the host after parsing.
	if strings.Contains(raw, `\`) {
		return adminPath + "/"
	}
	// A newline in a redirect target enables header injection.
	if strings.ContainsAny(raw, "\r\n") {
		return adminPath + "/"
	}
	return raw
}
