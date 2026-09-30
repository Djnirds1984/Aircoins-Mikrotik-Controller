package handlers

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// portalRouterLoginPath is the hotspot login page the operator installs on the
// MikroTik. It is public: the router fetches it, and the router has no panel
// session.
const portalRouterLoginPath = "/portal/router-login.html"

// portalRouterLoginPage is the file that goes onto the router.
//
// Why this exists: a stock RouterOS hotspot serves its own login.html from its
// html-directory, so the guest sees the built-in MikroTik portal and the
// controller is never in the path at all. The hotspot installer deliberately does
// not touch the router's files, so this page is the missing half - the operator
// fetches it onto the device and the hotspot then hands every guest to the
// controller's own portal.
//
// The "$(...)" tokens are RouterOS hotspot variables. They are NOT substituted
// here; they are written literally into the file and the ROUTER replaces them
// when it serves the page to a client. The "-esc" forms are RouterOS's
// URL-escaped variants, which is what makes a link-orig containing "&" survive
// the trip.
//
// The page is a plain meta refresh rather than a script so it works on the
// oldest phones a hotspot sees.
const portalRouterLoginTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
<meta http-equiv="refresh" content="0; url=%s">
</head>
<body>
<p>Taking you to the sign-in page&hellip;</p>
<p><a href="%s">Continue</a></p>
</body>
</html>
`

// PortalRouterLogin serves the login page to install on the MikroTik hotspot.
//
// The URL is built from the address the router used to reach the panel, so the
// file needs no editing per site: fetch it once and the guests are redirected
// back to the controller they came from.
func (h *Handler) PortalRouterLogin(w http.ResponseWriter, r *http.Request) {
	target := portalRouterLoginTarget(r)
	page := fmt.Sprintf(portalRouterLoginTemplate, target, target)

	head := w.Header()
	head.Set("Content-Type", "text/html; charset=utf-8")
	// The router stores this file; a browser hitting the path directly should
	// still get a working page rather than a cached copy from another site.
	head.Set("Cache-Control", "no-store")
	// Downloading it as a file is far more convenient than copy-pasting out of
	// a browser, so offer the name the router expects.
	head.Set("Content-Disposition", `inline; filename="login.html"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

// portalRouterLoginTarget builds the absolute sign-in URL the router should
// send guests to.
//
// The hotspot parameters are carried across so the panel knows which device the
// guest is on. link-login-only is included because it is what lets the panel
// finish a login through the router's own login URL when the RouterOS build has
// no API login command.
//
// None of them are load-bearing any more: the panel falls back to the source
// address of the request when the parameters are missing or mangled, so a guest
// still reaches the voucher box even if the substitution misbehaves.
func portalRouterLoginTarget(r *http.Request) string {
	var b strings.Builder
	b.WriteString(portalBaseURL(r))
	b.WriteString(portalLoginPath)
	b.WriteString("?mac=$(mac)&ip=$(ip)&link-login-only=$(link-login-only)")
	b.WriteString("&link-orig=$(link-orig-esc)&server-name=$(server-name)")
	return b.String()
}

// portalBaseURL returns the scheme and host of this controller as the client
// reached it, for embedding in a URL handed to the router.
//
// The Host header is attacker controlled, so it is validated: the result ends up
// inside a redirect target on a page served to guests, and an unvalidated value
// there would turn the hotspot into an open redirector.
func portalBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// A reverse proxy terminates TLS and forwards plain HTTP.
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto == "https" || proto == "http" {
		scheme = proto
	}
	host := portalRequestHost(r.Host)
	if host == "" {
		// No usable host: fall back to a relative path, which still works
		// because the router serves the page from the panel's own address.
		return ""
	}
	return scheme + "://" + host
}

// portalRequestHost validates a Host header for use in a redirect.
//
// Only a bare host or host:port is accepted. Anything carrying a path, user
// info, a query or whitespace is rejected, because those are exactly the shapes
// that turn a redirect into a hop to another site.
func portalRequestHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.ContainsAny(host, "/\\@?# \t\r\n") {
		return ""
	}
	if hostname, port, err := net.SplitHostPort(host); err == nil {
		if !validPortalHost(hostname) || port == "" {
			return ""
		}
		return host
	}
	if !validPortalHost(host) {
		return ""
	}
	return host
}

// validPortalHost reports whether a hostname is a plain IP address or a name
// made of the characters a hostname may contain.
func validPortalHost(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if net.ParseIP(name) != nil {
		return true
	}
	// A bracketed IPv6 literal.
	if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
		return net.ParseIP(strings.Trim(name, "[]")) != nil
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	return true
}
