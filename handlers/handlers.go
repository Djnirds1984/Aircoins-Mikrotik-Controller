// Package handlers contains the HTTP layer of the Aircoins MikroTik
// controller: the admin dashboard, the router inventory CRUD, the live device
// manager, the voucher engine and the captive portal.
//
// Every device interaction goes through MikrotikClient in mikrotik.go, which
// isolates the RouterOS API behind a small, retrying, error classified client.
package handlers

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// Config tunes the HTTP layer.
type Config struct {
	// APITimeout bounds a single RouterOS API call, including the dial.
	APITimeout time.Duration
	// PortalName is shown on the captive portal and in the admin footer.
	PortalName string
	// PortalTagline is the welcome line on the captive portal landing page.
	PortalTagline string
	// PortalSupport is the contact line shown on the captive portal, e.g.
	// "Ask at the counter for a voucher".
	PortalSupport string
	// AdminPath is the URL prefix the operator panel is served under, so a
	// guest who opens the controller IP only sees the captive portal.
	AdminPath string
	// DashboardAtRoot keeps the operator dashboard on / and moves the
	// captive portal to /portal only. It is opt-in: by default / serves the
	// captive portal and the panel lives under AdminPath. A bool named for
	// the exception cannot be silently flipped by a zero Config value, which
	// would hand every guest the fleet dashboard.
	DashboardAtRoot bool
	// DefaultRedirect is used when the client provides no link-orig.
	DefaultRedirect string
	// SecureCookies sets the Secure flag on admin cookies. Enable it behind
	// HTTPS.
	SecureCookies bool
	// AdminUser is the operator name seeded on first boot. An existing
	// account is never overwritten by it.
	AdminUser string
	// AdminPassword seeds the operator password on first boot. When it is
	// empty a random one is generated and logged once.
	AdminPassword string
	// AdminSessionTTL is how long a panel login stays valid.
	AdminSessionTTL time.Duration
	// PortalLoginBurst is how many captive portal logins one client IP may
	// attempt inside PortalLoginWindow before being throttled.
	PortalLoginBurst int
	// PortalLoginWindow is the throttling window for portal logins.
	PortalLoginWindow time.Duration
	// Version is displayed in the footer.
	Version string
	// Logger receives request and error logs.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.APITimeout <= 0 {
		c.APITimeout = 12 * time.Second
	}
	if strings.TrimSpace(c.PortalName) == "" {
		c.PortalName = "Aircoins Hotspot"
	}
	if strings.TrimSpace(c.PortalTagline) == "" {
		c.PortalTagline = "Connect to the Wi-Fi to get online"
	}
	if strings.TrimSpace(c.PortalSupport) == "" {
		c.PortalSupport = "Ask the front desk for a voucher code."
	}
	// The panel prefix must be a clean absolute path with no trailing slash:
	// it is concatenated with route suffixes by http.StripPrefix, and a
	// trailing slash there would leave "/admin/routers" as "routers".
	c.AdminPath = "/" + strings.Trim(strings.TrimSpace(c.AdminPath), "/")
	if c.AdminPath == "/" {
		c.AdminPath = "/admin"
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.PortalLoginBurst <= 0 {
		c.PortalLoginBurst = 12
	}
	if c.PortalLoginWindow <= 0 {
		c.PortalLoginWindow = time.Minute
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	if c.AdminSessionTTL <= 0 {
		c.AdminSessionTTL = 12 * time.Hour
	}
	return c
}

// Handler owns the shared dependencies of every HTTP endpoint.
type Handler struct {
	db      *database.DB
	tpl     *template.Template
	cfg     Config
	log     *slog.Logger
	limiter *ipLimiter
	// loginGuard throttles password guesses against the panel sign-in form.
	loginGuard *loginGuard
	traffic    trafficStore
	// portalFull serves the operator's own portal document when the PORTAL
	// editor is in full-page mode. It caches the compiled template.
	portalFull *portalFullRenderer
}

// New builds a Handler. The template set must already be parsed.
func New(db *database.DB, tpl *template.Template, cfg Config) *Handler {
	cfg = cfg.withDefaults()
	h := &Handler{
		db:         db,
		tpl:        tpl,
		cfg:        cfg,
		log:        cfg.Logger,
		limiter:    newIPLimiter(cfg.PortalLoginBurst, cfg.PortalLoginWindow),
		loginGuard: defaultLoginGuard(),
	}
	h.portalFull = newPortalFullRenderer(h)
	return h
}

// captiveProbePaths are the URLs operating systems request while deciding
// whether they are behind a captive portal.
//
// A joining phone does not only ask for "/": Android probes /generate_204 and
// /genindex.html, iOS probes /library/test/success.html and Windows probes
// /connecttest.txt. Every one of them used to fall through to the guarded
// catch-all, which answered with a redirect to the operator login form - so a
// guest that joined the SSID was shown the panel's sign-in page and handed the
// credentials of the router fleet.
//
// They are served the portal instead, which is what makes the phone pop the
// login window. Serving the sign-in page for a probe is also what a stock
// MikroTik hotspot does, so a walled-garden phone behaves identically whichever
// portal it is pointed at.
var captiveProbePaths = []string{
	"/generate_204",
	"/genindex.html",
	"/library/test/success.html",
	"/connecttest.txt",
	"/ncsi.txt",
	"/canonical.html",
	"/success.txt",
	"/hotspot-detect.html",
	"/msftconnecttest",
	"/redirect",
}

// Routes wires every endpoint and wraps them in the middleware chain.
//
// Two front doors are exposed on purpose:
//
//   - "/" is the captive portal a hotspot client lands on. Opening the IP of
//     the controller in a browser must never reveal the fleet dashboard.
//   - "/admin" is the operator panel, mounted with http.StripPrefix so the
//     whole existing route table keeps its original paths and templates.
//
// The admin routes stay reachable at the root as well ("/routers",
// "/vouchers", "/api/v1/...", "/portal/login") so existing bookmarks, the
// dashboard's own absolute links and the REST clients keep working.
func (h *Handler) Routes() http.Handler {
	admin := h.adminRoutes()

	mux := http.NewServeMux()

	// The captive portal at the root, or the dashboard when the operator
	// explicitly asked for the old layout.
	if h.cfg.DashboardAtRoot {
		mux.HandleFunc("GET /{$}", h.Dashboard)
		mux.HandleFunc("GET /portal", h.PortalIndex)
		mux.HandleFunc("GET /portal/", h.PortalIndex)
	} else {
		mux.HandleFunc("GET /{$}", h.PortalIndex)
	}

	// The operating-system captive probes are answered with the portal rather
	// than being left to the guarded catch-all below. Registering them here, and
	// not in adminRoutes, is what keeps them public: the catch-all mounts the
	// whole admin table behind requireAuth, which is what used to send a guest
	// to the operator login form.
	//
	// They are registered on the outer mux only. Mounted under /admin they would
	// be a path an attacker could reach to bypass the session, and the panel
	// prefix is a deployment detail no phone knows about.
	for _, probe := range captiveProbePaths {
		mux.HandleFunc("GET "+probe, h.PortalProbe)
	}

	// The panel under its prefix, behind the session guard. "/admin" (no
	// slash) has to redirect by hand because the subtree pattern only
	// matches "/admin/".
	mux.Handle(h.cfg.AdminPath+"/", h.requireAuth(http.StripPrefix(h.cfg.AdminPath, admin)))
	mux.HandleFunc("GET "+h.cfg.AdminPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, h.cfg.AdminPath+"/", http.StatusMovedPermanently)
	})

	// Health stays a flat, prefix-free probe for systemd and load balancers.
	mux.HandleFunc("GET /healthz", h.Health)

	// The same route table at the root, also guarded.
	mux.Handle("/", h.requireAuth(admin))

	return h.recoverer(h.logRequests(h.securityHeaders(h.csrfGuard(mux))))
}

// adminRoutes is the operator panel and the machine facing API, mounted both
// at the root and under Config.AdminPath.
func (h *Handler) adminRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Discovery and health.
	mux.HandleFunc("GET /{$}", h.Dashboard)

	// Router inventory.
	mux.HandleFunc("GET /routers", h.RoutersList)
	mux.HandleFunc("POST /routers", h.RouterCreate)
	mux.HandleFunc("GET /routers/{id}", h.RouterDetail)
	mux.HandleFunc("POST /routers/{id}", h.RouterUpdate)
	mux.HandleFunc("POST /routers/{id}/delete", h.RouterDelete)
	mux.HandleFunc("POST /routers/{id}/test", h.RouterTest)
	mux.HandleFunc("POST /routers/{id}/refresh", h.RouterRefresh)

	// Live hotspot clients on a device.
	mux.HandleFunc("POST /routers/{id}/sessions/disconnect", h.SessionDisconnect)
	mux.HandleFunc("POST /routers/{id}/block", h.ClientBlock)
	mux.HandleFunc("POST /routers/{id}/unblock", h.ClientUnblock)

	// Network tab: hotspot configuration of one device.
	mux.HandleFunc("GET /network", h.NetworkOverview)
	mux.HandleFunc("GET /network/{id}", h.NetworkDetail)

	// Hotspot server CRUD (/ip/hotspot).
	mux.HandleFunc("POST /network/{id}/hotspot/install", h.HotspotInstall)
	mux.HandleFunc("POST /network/{id}/servers", h.HotspotServerCreate)
	mux.HandleFunc("POST /network/{id}/servers/{sid}", h.HotspotServerUpdate)

	mux.HandleFunc("POST /network/{id}/servers/{sid}/toggle", h.HotspotServerToggle)
	mux.HandleFunc("POST /network/{id}/servers/{sid}/delete", h.HotspotServerDelete)

	// Hotspot server profiles (/ip/hotspot/profile).
	mux.HandleFunc("POST /network/{id}/server-profiles", h.HotspotServerProfileCreate)
	mux.HandleFunc("POST /network/{id}/server-profiles/{sid}", h.HotspotServerProfileUpdate)
	mux.HandleFunc("POST /network/{id}/server-profiles/{sid}/delete", h.HotspotServerProfileDelete)

	// Hotspot user profiles (/ip/hotspot/user/profile).
	mux.HandleFunc("POST /network/{id}/user-profiles", h.HotspotUserProfileCreate)
	mux.HandleFunc("POST /network/{id}/user-profiles/{sid}", h.HotspotUserProfileUpdate)
	mux.HandleFunc("POST /network/{id}/user-profiles/{sid}/delete", h.HotspotUserProfileDelete)

	// Walled garden hosts (/ip/hotspot/walled-garden).
	mux.HandleFunc("POST /network/{id}/walled-garden", h.WalledGardenCreate)
	mux.HandleFunc("POST /network/{id}/walled-garden/{sid}", h.WalledGardenUpdate)
	mux.HandleFunc("POST /network/{id}/walled-garden/{sid}/toggle", h.WalledGardenToggle)
	mux.HandleFunc("POST /network/{id}/walled-garden/{sid}/delete", h.WalledGardenDelete)

	// Walled garden IP rules (/ip/hotspot/walled-garden/ip).
	mux.HandleFunc("POST /network/{id}/walled-garden-ip", h.WalledGardenIPCreate)
	mux.HandleFunc("POST /network/{id}/walled-garden-ip/{sid}", h.WalledGardenIPUpdate)
	mux.HandleFunc("POST /network/{id}/walled-garden-ip/{sid}/toggle", h.WalledGardenIPToggle)
	mux.HandleFunc("POST /network/{id}/walled-garden-ip/{sid}/delete", h.WalledGardenIPDelete)

	// IP pools (/ip/pool) - create only.
	mux.HandleFunc("POST /network/{id}/pools", h.IPPoolCreate)

	// VLAN interfaces (/interface/vlan) - create only.
	mux.HandleFunc("POST /network/{id}/vlans", h.VLANCreate)

	// Session history.
	mux.HandleFunc("GET /sessions", h.SessionsList)

	// Host maintenance tools.
	mux.HandleFunc("GET /tools", h.Tools)
	mux.HandleFunc("POST /tools/zerotier/install", h.ToolsZeroTierInstall)
	mux.HandleFunc("POST /tools/zerotier/start", h.ToolsZeroTierStart)
	mux.HandleFunc("POST /tools/zerotier/join", h.ToolsZeroTierJoin)
	mux.HandleFunc("POST /tools/zerotier/leave", h.ToolsZeroTierLeave)

	// Voucher engine.
	mux.HandleFunc("GET /vouchers", h.VouchersList)
	mux.HandleFunc("GET /vouchers/export.csv", h.VouchersExport)
	mux.HandleFunc("POST /vouchers/generate", h.VouchersGenerate)
	mux.HandleFunc("POST /vouchers/batch/delete", h.VoucherBatchDelete)
	mux.HandleFunc("POST /vouchers/{id}/delete", h.VoucherDelete)
	mux.HandleFunc("POST /vouchers/{id}/status", h.VoucherSetStatus)
	mux.HandleFunc("POST /vouchers/{id}/push", h.VoucherPush)
	mux.HandleFunc("POST /vouchers/{id}/validate", h.VoucherValidate)

	// Panel authentication. These are the only routes reachable without a
	// session (see isPublicPath).
	mux.HandleFunc("GET /login", h.AdminLogin)
	mux.HandleFunc("POST /login", h.AdminLoginSubmit)
	mux.HandleFunc("GET /logout", h.AdminLogout)
	mux.HandleFunc("POST /logout", h.AdminLogout)

	// Panel settings (change the operator credentials).
	mux.HandleFunc("GET /settings", h.Settings)
	mux.HandleFunc("POST /settings/credentials", h.SettingsCredentials)

	// Captive portal. The login form stays on its own path: the landing page
	// at "/" is a separate handler so a direct visit is not answered with a
	// form that has no hotspot parameters to submit.
	mux.HandleFunc("GET /portal/login", h.PortalLogin)
	mux.HandleFunc("POST /portal/login", h.PortalAuthenticate)
	mux.HandleFunc("GET /portal/status", h.PortalStatus)

	// The stored portal background photo. Public, because the guest facing
	// pages fetch it before anyone has signed in.
	mux.HandleFunc("GET "+portalBackgroundPath, h.PortalBackground)

	// PORTAL editor: the theme, header name, extra HTML and background image
	// shown to guests on the captive pages.
	mux.HandleFunc("GET "+portalEditorPath, h.PortalEditor)
	mux.HandleFunc("POST "+portalEditorSavePath, h.PortalEditorSave)
	mux.HandleFunc("POST "+portalEditorBackgroundPath, h.PortalEditorBackground)
	mux.HandleFunc("POST "+portalEditorBackgroundDrop, h.PortalEditorBackgroundDelete)

	// REST API (RouterOS v7+ compatible).
	h.RoutesAPI(mux)
	return mux
}

// recoverer turns a panic in any handler into a 500 instead of killing the
// process, and logs the stack for post mortem analysis.
func (h *Handler) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				h.log.Error("panic recovered", "path", r.URL.Path, "panic", rec,
					"stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		h.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"remote", clientIP(r),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// securityHeaders applies conservative defaults. Styles are inlined in the
// templates so the captive portal renders on clients that have no internet
// access at all.
//
// script-src and connect-src must be stated explicitly: default-src is their
// fallback, so default-src 'none' alone blocks the dashboard's inline
// monitor script and its same-origin fetch() calls outright — the interface
// dropdown then stays disabled forever, in every browser.
func (h *Handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The background upload is the one endpoint that legitimately needs
		// more than 1 MB, so it gets a budget sized to the store's own limit
		// (plus multipart framing). Everything else keeps the tight cap that
		// stops a runaway POST from filling the server's memory.
		limit := int64(1 << 20)
		if h.trimAdminPath(r.URL.Path) == portalEditorBackgroundPath {
			limit = database.MaxPortalBackgroundBytes + (1 << 20)
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		head := w.Header()
		head.Set("X-Content-Type-Options", "nosniff")
		head.Set("X-Frame-Options", "DENY")
		head.Set("Referrer-Policy", "no-referrer")
		head.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self' 'unsafe-inline'; connect-src 'self'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// parseRequestForm populates r.Form for the CSRF check.
//
// ParseForm alone is not enough: for a multipart/form-data POST (the portal
// background upload) it leaves PostForm empty, so the submitted csrf_token
// would read as "" and every image upload would be rejected with 403. The
// multipart parser copies the ordinary fields into PostForm too, so the token
// is then read exactly as it is for a urlencoded form.
func (h *Handler) parseRequestForm(r *http.Request) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// A small in-memory budget: the only multipart form is the upload, and
		// its file part is read with an explicit limit in the handler.
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return err
		}
		return nil
	}
	return r.ParseForm()
}

// csrfGuard enforces the double submit cookie on state changing admin routes.
//
// Captive portal posts are exempt on purpose: portal clients are redirected by
// the hotspot, some of them drop cookies in the walled garden, and the portal
// is additionally protected by the per IP login limiter.
func (h *Handler) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(h.trimAdminPath(r.URL.Path), "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(h.trimAdminPath(r.URL.Path), "/portal/") {
			next.ServeHTTP(w, r)
			return
		}
		if err := h.parseRequestForm(r); err != nil {
			http.Error(w, "cannot read form", http.StatusBadRequest)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		submitted := r.PostFormValue(csrfFieldName)
		if err != nil || submitted == "" ||
			subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(submitted)) != 1 {
			h.log.Warn("csrf token rejected", "path", r.URL.Path, "remote", clientIP(r))
			http.Error(w, "form expired or invalid, reload the page and try again", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const (
	csrfCookieName = "aircoins_csrf"
	csrfFieldName  = "csrf_token"
	flashCookie    = "aircoins_flash"
)

// csrfToken returns the token bound to this browser, creating one on first use.
func (h *Handler) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && len(cookie.Value) >= 22 {
		return cookie.Value
	}
	token := randomToken(24)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.SecureCookies,
		MaxAge:   8 * 3600,
	})
	return token
}

// randomToken returns a URL safe random string of n bytes of entropy.
func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is fatal for the security model; fall back to a
		// time based value so the request still gets a token.
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// trimAdminPath removes the panel prefix from a request path so middleware can
// reason about one canonical set of routes. The portal and the API are mounted
// under /admin as well as at the root, and a portal login submitted from
// /admin/portal/login must still be recognised as a portal route.
func (h *Handler) trimAdminPath(path string) string {
	if h.cfg.AdminPath == "" || h.cfg.AdminPath == "/" {
		return path
	}
	if path == h.cfg.AdminPath {
		return "/"
	}
	if strings.HasPrefix(path, h.cfg.AdminPath+"/") {
		return strings.TrimPrefix(path, h.cfg.AdminPath)
	}
	return path
}

// page holds the values every template needs.
type page struct {
	Title      string
	Nav        string
	Flash      *flashMessage
	CSRFToken  string
	PortalName string
	Version    string
	Year       int
	// Back is the page the shared voucher/action forms return to.
	Back string
	// AdminPath is the prefix the operator panel is mounted under, so the
	// navigation can stay inside /admin instead of bouncing to the portal.
	AdminPath string
}

// pageRenderer lets render() inject the request scoped values without every
// handler repeating them.
type pageRenderer interface {
	base() *page
}

func (p *page) base() *page { return p }

// flashMessage is a one shot notice rendered after a redirect.
type flashMessage struct {
	Kind    string // "ok", "warn" or "err"
	Message string
}

func (f *flashMessage) Class() string {
	switch f.Kind {
	case "ok":
		return "alert ok"
	case "warn":
		return "alert warn"
	default:
		return "alert err"
	}
}

// setFlash stores a message in a cookie that the next render consumes.
func setFlash(w http.ResponseWriter, message, kind string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	payload, err := encodeFlash(message, kind)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    payload,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60,
	})
}

// popFlash reads and clears the flash cookie.
func popFlash(w http.ResponseWriter, r *http.Request) *flashMessage {
	cookie, err := r.Cookie(flashCookie)
	if err != nil || cookie.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	message, kind, err := decodeFlash(cookie.Value)
	if err != nil || message == "" {
		return nil
	}
	return &flashMessage{Kind: kind, Message: message}
}

// encodeFlash packs kind and message into a cookie safe value.
func encodeFlash(message, kind string) (string, error) {
	if kind != "ok" && kind != "warn" && kind != "err" {
		kind = "ok"
	}
	message = truncateText(strings.TrimSpace(message), 400)
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + message)), nil
}

func decodeFlash(value string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return "", "", errInvalidFlash
	}
	return parts[1], parts[0], nil
}

var errInvalidFlash = errors.New("invalid flash payload")

// flashAndRedirect stores the message and sends the browser back with a 303 so
// a refresh cannot repeat the POST.
func (h *Handler) flashAndRedirect(w http.ResponseWriter, r *http.Request, path, kind, message string) {
	setFlash(w, message, kind)
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// render executes a template with the shared page values injected.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name string, data pageRenderer) {
	p := data.base()
	p.PortalName = h.cfg.PortalName
	p.Version = h.cfg.Version
	p.Year = time.Now().UTC().Year()
	if p.AdminPath == "" {
		p.AdminPath = h.cfg.AdminPath
	}
	if p.Flash == nil {
		p.Flash = popFlash(w, r)
	}
	if p.CSRFToken == "" {
		p.CSRFToken = h.csrfToken(w, r)
	}

	buf := new(bytes.Buffer)
	if err := h.tpl.ExecuteTemplate(buf, name, data); err != nil {
		h.log.Error("render template", "template", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	head := w.Header()
	head.Set("Content-Type", "text/html; charset=utf-8")
	head.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// routerIDPath extracts and validates the {id} path segment, replying with a
// 400 when it is not a positive integer.
func (h *Handler) routerIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid router id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// voucherIDPath extracts and validates the {id} path segment of a voucher
// route.
func (h *Handler) voucherIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid voucher id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// clientIP returns the best guess of the client address, honouring the proxy
// headers set by the reverse proxy in front of the controller.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return strings.TrimSpace(real)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func truncateText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}
