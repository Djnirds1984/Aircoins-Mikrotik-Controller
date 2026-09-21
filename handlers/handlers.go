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
	// DefaultRedirect is used when the client provides no link-orig.
	DefaultRedirect string
	// SecureCookies sets the Secure flag on admin cookies. Enable it behind
	// HTTPS.
	SecureCookies bool
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
	return c
}

// Handler owns the shared dependencies of every HTTP endpoint.
type Handler struct {
	db      *database.DB
	tpl     *template.Template
	cfg     Config
	log     *slog.Logger
	limiter *ipLimiter
}

// New builds a Handler. The template set must already be parsed.
func New(db *database.DB, tpl *template.Template, cfg Config) *Handler {
	cfg = cfg.withDefaults()
	return &Handler{
		db:      db,
		tpl:     tpl,
		cfg:     cfg,
		log:     cfg.Logger,
		limiter: newIPLimiter(cfg.PortalLoginBurst, cfg.PortalLoginWindow),
	}
}

// Routes wires every endpoint and wraps them in the middleware chain.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Discovery and health.
	mux.HandleFunc("GET /{$}", h.Dashboard)
	mux.HandleFunc("GET /healthz", h.Health)

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

	// Session history.
	mux.HandleFunc("GET /sessions", h.SessionsList)

	// Voucher engine.
	mux.HandleFunc("GET /vouchers", h.VouchersList)
	mux.HandleFunc("GET /vouchers/export.csv", h.VouchersExport)
	mux.HandleFunc("POST /vouchers/generate", h.VouchersGenerate)
	mux.HandleFunc("POST /vouchers/batch/delete", h.VoucherBatchDelete)
	mux.HandleFunc("POST /vouchers/{id}/delete", h.VoucherDelete)
	mux.HandleFunc("POST /vouchers/{id}/status", h.VoucherSetStatus)
	mux.HandleFunc("POST /vouchers/{id}/push", h.VoucherPush)
	mux.HandleFunc("POST /vouchers/{id}/validate", h.VoucherValidate)

	// Captive portal.
	mux.HandleFunc("GET /portal/login", h.PortalLogin)
	mux.HandleFunc("POST /portal/login", h.PortalAuthenticate)
	mux.HandleFunc("GET /portal/status", h.PortalStatus)

	// REST API (RouterOS v7+ compatible).
	h.RoutesAPI(mux)
	return h.recoverer(h.logRequests(h.securityHeaders(h.csrfGuard(mux))))
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
func (h *Handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		head := w.Header()
		head.Set("X-Content-Type-Options", "nosniff")
		head.Set("X-Frame-Options", "DENY")
		head.Set("Referrer-Policy", "no-referrer")
		head.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
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
		if strings.HasPrefix(r.URL.Path, "/portal/") {
			next.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
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
