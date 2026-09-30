package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// portalFullData is the context an operator's full page is rendered against.
//
// It is deliberately a small, flat struct of already-formatted strings. Two
// reasons:
//
//   - html/template escapes every interpolation, so a live MAC address or a
//     router name can never break out of an attribute. (The operator's own
//     markup is trusted; the *data* filling it is not.)
//   - The field names double as documentation. A name that does not exist is a
//     template error the operator sees in the editor, not a silently blank spot
//     on a customer's phone.
type portalFullData struct {
	// PortalName is the header name from the PORTAL editor.
	PortalName string
	// MAC and IP come from the MikroTik redirect and identify the guest.
	MAC string
	IP  string
	// RouterName is the device serving this hotspot, when it could be resolved.
	RouterName  string
	RouterKnown bool
	// LoginAction is the form action carrying the hotspot parameters, ready to
	// drop into <form action="{{.LoginAction}}">. It is template.URL so
	// html/template does not escape the "=" and "&" into one broken key.
	LoginAction template.URL
	// RedirectTo is the original destination after a successful login.
	RedirectTo string
	// LoggedIn reports that the guest authenticated successfully.
	LoggedIn bool
	// FormError and Notice carry the outcome of the last POST.
	FormError string
	Notice    string
	// ExpiresAt is when the current allowance runs out (RFC3339) or empty.
	ExpiresAt string
	// RemainingSeconds drives the countdown timer. Zero when unknown.
	RemainingSeconds int
	// BannerURL points at the uploaded image, or is empty so the template can
	// fall back to a solid colour.
	BannerURL string
	// AdminPath and LoginURL let the page link to the panel and to the
	// built-in sign-in form.
	AdminPath string
	LoginURL  string
	// Year and Version are for a footer credit line.
	Year    int
	Version string
}

// portalFullTemplate is a parsed operator page.
type portalFullTemplate struct {
	tmpl *template.Template
	src  string
}

// portalFullCache memoises the compiled operator page.
//
// One entry is enough: a controller has a single portal, and a deployment that
// flip-flopped between two pages per request would be a bug elsewhere. The
// entry is keyed on the source, so saving a new page invalidates it naturally.
type portalFullCache struct {
	mu     sync.Mutex
	cached *portalFullTemplate
	hits   int
	misses int
	// lastErr is the most recent compile error, surfaced in the editor so a
	// broken page is diagnosable without reading server logs.
	lastErr string
}

// get returns the compiled template for src, compiling it on a miss.
func (c *portalFullCache) get(src string) (*template.Template, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil && c.cached.src == src {
		c.hits++
		return c.cached.tmpl, nil
	}
	c.misses++
	// The operator's document is parsed as a template rather than injected as a
	// string, so {{.MAC}} and friends work. The FuncMap is intentionally not
	// extended: everything the page can reach is a field on portalFullData.
	tmpl, err := template.New("portal-full").Parse(src)
	if err != nil {
		c.lastErr = err.Error()
		return nil, fmt.Errorf("parse the portal page: %w", err)
	}
	c.cached = &portalFullTemplate{tmpl: tmpl, src: src}
	c.lastErr = ""
	return tmpl, nil
}

// stats reports the cache counters. It exists for the tests, which assert that
// a portal hit by every guest connection compiles the page once rather than
// re-parsing the document on each request.
func (c *portalFullCache) stats() (hits, misses int, lastErr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.lastErr
}

// portalFullRenderer serves the operator's own portal document.
//
// Safety model, stated plainly: the source is authored by an authenticated
// operator and is executed by the browser of every guest who connects, so it
// is trusted code. The only sandbox is the Content-Security-Policy, which the
// page cannot escape - it forbids remote scripts, remote styles and framing.
// The parts that carry real risk are not left to the operator: the voucher
// field and the form action are built here, so a hostile page cannot silently
// exfiltrate what a customer types into it.
type portalFullRenderer struct {
	h     *Handler
	cache *portalFullCache
}

// newPortalFullRenderer builds the renderer for a handler.
func newPortalFullRenderer(h *Handler) *portalFullRenderer {
	return &portalFullRenderer{h: h, cache: &portalFullCache{}}
}

// portalFullContext is everything the renderer needs that is not already in the
// page view. It is passed in rather than read from globals so the renderer stays
// testable without a full Handler.
type portalFullContext struct {
	// PortalName is the resolved header name (editor value or PORTAL_NAME).
	PortalName string
	// BannerURL is the uploaded image path, empty when none is stored.
	BannerURL string
	// AdminPath, Version and Year come from the config.
	AdminPath string
	Version   string
	Year      int
}

// render writes the operator page, or reports false so the caller can fall back
// to the built-in layout.
//
// A broken operator page must never leave a guest staring at an error page, so
// every failure path returns false and the caller renders the standard captive
// screen, which always works.
func (p *portalFullRenderer) render(w http.ResponseWriter, r *http.Request, view *portalPage, settings database.PortalSettings, env portalFullContext) bool {
	if !settings.FullPageActive() {
		return false
	}

	tmpl, err := p.cache.get(settings.FullHTML)
	if err != nil {
		p.h.log.Warn("portal full page failed to compile, serving the built-in page",
			"error", err, "remote", clientIP(r))
		return false
	}

	data := portalFullData{
		PortalName:       env.PortalName,
		MAC:              view.Portal.MAC,
		IP:               view.Portal.IP,
		RouterName:       view.RouterName,
		RouterKnown:      view.RouterKnown,
		LoginAction:      portalAction(view.Portal),
		RedirectTo:       view.RedirectTo,
		LoggedIn:         view.Success,
		FormError:        view.FormError,
		Notice:           view.Notice,
		BannerURL:        env.BannerURL,
		AdminPath:        env.AdminPath,
		LoginURL:         portalLoginPath,
		Year:             env.Year,
		Version:          env.Version,
		ExpiresAt:        portalExpiry(view.Voucher),
		RemainingSeconds: portalRemainingSeconds(view.Voucher),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		p.h.log.Warn("portal full page failed to render, serving the built-in page",
			"error", err, "remote", clientIP(r))
		return false
	}

	head := w.Header()
	head.Set("Content-Type", "text/html; charset=utf-8")
	head.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = buf.WriteTo(w)
	return true
}

// portalExpiry renders a voucher expiry for the operator template, empty when
// the session has no allowance attached.
func portalExpiry(voucher *database.Voucher) string {
	if voucher == nil || voucher.ExpiresAt == nil || voucher.ExpiresAt.IsZero() {
		return ""
	}
	return voucher.ExpiresAt.UTC().Format(time.RFC3339)
}

// portalRemainingSeconds is the time left on the current allowance, or zero
// when it is unknown. The operator's countdown timer reads it from the page and
// decrements locally, so a guest never has to poll the controller.
func portalRemainingSeconds(voucher *database.Voucher) int {
	if voucher == nil || voucher.ExpiresAt == nil {
		return 0
	}
	remaining := int(time.Until(*voucher.ExpiresAt).Seconds())
	if remaining < 0 {
		return 0
	}
	return remaining
}
