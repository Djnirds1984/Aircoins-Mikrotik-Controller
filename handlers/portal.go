package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// errPortalNoRouter is returned when a portal login cannot be attributed to a
// registered device.
var errPortalNoRouter = errors.New("no hotspot router is linked to this login page yet")

// portalRequest carries the MikroTik hotspot redirect parameters. The names
// match the $(variable) replacements available in the hotspot login.html
// template, so a stock hotspot can point straight at this portal.
type portalRequest struct {
	MAC           string
	IP            string
	Username      string
	LinkLogin     string
	LinkLoginOnly string
	LinkOrig      string
	ServerName    string
	Error         string
	ChapID        string
	ChapChallenge string
}

func (p portalRequest) empty() bool {
	return p.MAC == "" && p.IP == "" && p.LinkLogin == "" && p.LinkLoginOnly == ""
}

// portalRequestFromValues reads the hotspot parameters from a query string or a
// submitted form.
func portalRequestFromValues(values url.Values) portalRequest {
	get := func(key string) string { return strings.TrimSpace(values.Get(key)) }
	request := portalRequest{
		MAC:           database.FormatMAC(get("mac")),
		IP:            database.NormalizeIP(get("ip")),
		Username:      get("username"),
		LinkLogin:     get("link-login"),
		LinkLoginOnly: get("link-login-only"),
		LinkOrig:      get("link-orig"),
		ServerName:    get("server-name"),
		Error:         get("error"),
		ChapID:        get("chap-id"),
		ChapChallenge: get("chap-challenge"),
	}
	if request.ServerName == "" {
		// Some hotspot templates use the NAS identifier instead.
		request.ServerName = get("nasid")
	}
	if request.LinkLoginOnly == "" {
		request.LinkLoginOnly = get("link-login-only")
	}
	return request
}

// portalPage backs the captive login screen.
type portalPage struct {
	page
	Portal       portalRequest
	RouterName   string
	RouterKnown  bool
	FormError    string
	Notice       string
	Success      bool
	Voucher      *database.Voucher
	RedirectTo   string
	FallbackLink string
	ShowPassword bool
	// TrialEnabled reports that the hotspot server profile allows trial (free
	// time) logins, so the captive page can offer the "Claim free time" button.
	TrialEnabled bool
	// TrialURL is the hotspot's own login endpoint offered as the free-time
	// button's action when the redirect carried no $(link-login-only).
	TrialURL string
	// Branding is the operator's theme, header name, background and extra HTML,
	// resolved from the PORTAL editor.
	Branding portalBranding
	// Coin is the state of the "Insert coin" tab.
	Coin coinPortal
}

// PortalLogin serves the captive portal login page. MikroTik redirects the
// client here with the hotspot parameters in the query string.
func (h *Handler) PortalLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	request := portalRequestFromValues(r.URL.Query())

	view := &portalPage{
		page:     page{Title: "Sign in", Nav: ""},
		Portal:   request,
		Branding: h.portalBrandingFor(ctx),
		Coin:     h.coinPortalFor(r, request),
	}

	// A hotspot reports a failed attempt by redirecting back with ?error=...
	if request.Error != "" {
		view.FormError = request.Error
	}

	router, err := h.resolvePortalRouter(ctx, request)
	if err != nil {
		view.FormError = "This hotspot is not linked to the Aircoins controller yet. Please ask the front desk for help."
		h.log.Warn("portal has no router", "remote", clientIP(r), "server_name", request.ServerName,
			"link_login", request.LinkLogin, "error", err)
	} else {
		view.RouterName = router.Name
		view.RouterKnown = true
		// The login screen is where the hotspot parameters (and therefore the
		// device's own login URL) exist, so this is where a trial can actually
		// be started. The check fails silent: an unreachable device only means
		// the free-time button stays hidden.
		view.TrialEnabled, view.TrialURL = h.portalTrial(ctx, request, router)
	}

	h.renderPortal(w, r, http.StatusOK, view)
}

// PortalAuthenticate handles both login styles of the portal: a voucher key or
// a hotspot username/password pair.
func (h *Handler) PortalAuthenticate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.limitPortal(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the login form", http.StatusBadRequest)
		return
	}

	// The hotspot parameters arrive in the form ACTION's query string, not in
	// the POST body: the sign-in form is "/portal/login?mac=...&ip=..." and the
	// browser only puts the voucher in the body. Reading r.PostForm alone - as
	// this did - dropped every parameter, so the guest was told the page had
	// been opened directly even when the hotspot had redirected them properly.
	//
	// r.Form is the union of the query string and the body, which is what the
	// round trip actually needs. The body still wins for the voucher itself.
	request := portalRequestFromValues(r.Form)
	voucherCode := database.FormatVoucherCode(r.FormValue("voucher"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	// A hotspot that redirects without parameters leaves both fields empty.
	// The guest's address is still known, though: it is the source address of
	// this very request. Adopting it is what lets someone holding a paid
	// voucher get online through a bare redirect.
	if request.IP == "" {
		request.IP = database.NormalizeIP(clientIP(r))
	}

	view := &portalPage{
		page:         page{Title: "Sign in", Nav: ""},
		Portal:       request,
		ShowPassword: username != "",
		Coin:         h.coinPortalFor(r, request),
	}

	if request.IP == "" && request.MAC == "" {
		view.FormError = "This login page was opened directly. Connect to the hotspot Wi-Fi and the form will work."
		h.renderPortal(w, r, http.StatusBadRequest, view)
		return
	}

	router, err := h.resolvePortalRouter(ctx, request)
	if err != nil {
		view.FormError = "This hotspot is not linked to the Aircoins controller yet. Please ask the front desk for help."
		h.log.Warn("portal login without router", "remote", clientIP(r), "error", err)
		h.renderPortal(w, r, http.StatusServiceUnavailable, view)
		return
	}
	view.RouterName = router.Name
	view.RouterKnown = true

	if voucherCode == "" && username == "" {
		view.FormError = "Enter your voucher code, or your hotspot username and password."
		h.renderPortal(w, r, http.StatusBadRequest, view)
		return
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		view.FormError = "The hotspot gateway cannot be reached right now (" + routerErrorHint(err) + "). Please try again in a moment."
		h.renderPortal(w, r, http.StatusServiceUnavailable, view)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	if voucherCode != "" {
		h.portalRedeemVoucher(w, r, view, client, callCtx, router, voucherCode, request)
		return
	}
	h.portalPasswordLogin(w, r, view, client, callCtx, router, username, password, request)
}

// portalRedeemVoucher consumes a prepaid key and, when the device allows it,
// logs the client in immediately.
func (h *Handler) portalRedeemVoucher(w http.ResponseWriter, r *http.Request, view *portalPage, client *MikrotikClient,
	ctx context.Context, router database.Router, code string, request portalRequest) {

	voucher, err := h.db.Vouchers().FindByCode(r.Context(), code)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.log.Info("portal rejected unknown voucher", "remote", clientIP(r), "router", router.Name)
			view.FormError = "That voucher code was not recognised. Check the spelling, or ask the front desk."
			h.renderPortal(w, r, http.StatusOK, view)
			return
		}
		h.fail(w, r, "look up voucher", err)
		return
	}

	// A key issued for another device cannot be honoured here.
	if voucher.RouterID != nil && *voucher.RouterID != router.ID {
		view.FormError = "That voucher belongs to a different hotspot. Please use the key issued for this location."
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}

	redemption, err := h.redeemVoucher(ctx, client, voucher, request.MAC, request.IP)
	if err != nil {
		h.log.Info("voucher redemption failed", "code", voucher.Code, "router", router.Name, "error", err)
		switch {
		case errors.Is(err, database.ErrVoucherNotRedeemable):
			view.FormError = strings.TrimPrefix(err.Error(), database.ErrVoucherNotRedeemable.Error()+": ")
		case errors.Is(err, ErrRouterUnreachable), errors.Is(err, ErrRouterTimeout):
			view.FormError = "The hotspot is busy or unreachable; your voucher was not used. Please try again."
		default:
			view.FormError = "That voucher could not be activated: " + routerErrorHint(err)
		}
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}

	// Track the session locally straight away; the next device poll refreshes it.
	h.registerPortalSession(r.Context(), router, redemption.Voucher.Code, request, "voucher")

	view.Success = true
	view.Voucher = &redemption.Voucher
	view.Notice = redemption.Note
	h.finishPortalLogin(w, r, view, request, redemption.Voucher.Code, redemption.Voucher.Code, redemption.LoginViaAPI)
}

// portalPasswordLogin authenticates a hotspot username/password pair.
func (h *Handler) portalPasswordLogin(w http.ResponseWriter, r *http.Request, view *portalPage, client *MikrotikClient,
	ctx context.Context, router database.Router, username, password string, request portalRequest) {

	err := client.HotspotLogin(ctx, username, password, request.MAC, request.IP)
	if err != nil {
		h.log.Info("portal password login failed", "username", username, "router", router.Name, "error", err)
		switch {
		case errors.Is(err, ErrRouterAuth):
			view.FormError = "Wrong hotspot username or password. Please try again."
		case errors.Is(err, ErrRouterUnreachable), errors.Is(err, ErrRouterTimeout):
			view.FormError = "The hotspot gateway is not answering right now. Please try again in a moment."
		case errors.Is(err, ErrRouterUnknownHost), errors.Is(err, ErrRouterNoCommand):
			// The device expects the client to authenticate on its own login
			// page; hand the credentials over through the standard GET flow.
			h.finishPortalLogin(w, r, view, request, username, password, false)
			return
		default:
			view.FormError = "Login was refused: " + routerErrorHint(err)
		}
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}

	h.registerPortalSession(r.Context(), router, username, request, "password")
	view.Success = true
	h.finishPortalLogin(w, r, view, request, username, password, true)
}

// renderPortal writes the captive portal to the client.
//
// When the operator has switched the PORTAL editor to full-page mode their own
// document is served instead. It is rendered through this one helper on every
// path that would otherwise render captive.html, so a guest sees the same page
// whether they arrived at the welcome screen, the sign-in form or the result of
// a login attempt. A page that fails to compile falls back to the built-in
// layout rather than showing the guest an error.
func (h *Handler) renderPortal(w http.ResponseWriter, r *http.Request, status int, view *portalPage) {
	settings, err := h.db.PortalSettings().Get(r.Context())
	if err != nil {
		// Degrade to the built-in page: a guest cannot fix a database read
		// error, and a working sign-in form beats a 500.
		h.log.Warn("portal settings unavailable, serving the built-in page", "error", err)
		h.render(w, r, status, "captive.html", captivePageFromPortal(view))
		return
	}

	env := portalFullContext{
		PortalName: view.Branding.HeaderName,
		AdminPath:  h.cfg.AdminPath,
		Version:    h.cfg.Version,
		Year:       time.Now().UTC().Year(),
	}
	if settings.HasBackground() {
		env.BannerURL = portalBackgroundPath
	}
	if h.portalFull.render(w, r, view, settings, env) {
		return
	}
	h.render(w, r, status, "captive.html", captivePageFromPortal(view))
}

// finishPortalLogin sends the client on to its original destination, or renders
// the success screen when there is nowhere safe to go.
//
// When loginViaAPI is false the client is redirected to the hotspot's own
// login-only URL with the credentials appended, which is the standard MikroTik
// fallback for devices without /ip/hotspot/active/login support.
func (h *Handler) finishPortalLogin(w http.ResponseWriter, r *http.Request, view *portalPage, request portalRequest, username, password string, loginViaAPI bool) {
	if !loginViaAPI {
		if fallback := hotspotFallbackURL(request, username, password); fallback != "" {
			h.log.Info("redirecting client to the hotspot login endpoint", "remote", clientIP(r))
			http.Redirect(w, r, fallback, http.StatusSeeOther)
			return
		}
	}

	target := h.portalRedirectTarget(request)
	if target == "" {
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}
	view.RedirectTo = target
	h.log.Info("portal login complete", "remote", clientIP(r), "username", username, "redirect", target)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// registerPortalSession mirrors a fresh login in the local session table.
func (h *Handler) registerPortalSession(ctx context.Context, router database.Router, username string, request portalRequest, method string) {
	session := database.Session{
		RouterID:   router.ID,
		Username:   username,
		Address:    request.IP,
		MACAddress: request.MAC,
		LoginBy:    "controller portal (" + method + ")",
		Server:     request.ServerName,
	}
	if err := h.db.Sessions().Register(ctx, session, time.Now()); err != nil {
		h.log.Error("cannot register portal session", "username", username, "error", err)
	}
}

// portalRedirectTarget picks where to send the client after a successful login.
func (h *Handler) portalRedirectTarget(request portalRequest) string {
	if target := safeRedirectURL(request.LinkOrig); target != "" {
		return target
	}
	return safeRedirectURL(h.cfg.DefaultRedirect)
}

// hotspotFallbackURL builds the MikroTik login-only URL that completes the
// login in the browser when the API path is unavailable.
func hotspotFallbackURL(request portalRequest, username, password string) string {
	base := safeRedirectURL(request.LinkLoginOnly)
	if base == "" {
		base = safeRedirectURL(request.LinkLogin)
	}
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return ""
	}
	values := parsed.Query()
	values.Set("username", username)
	values.Set("password", password)
	if dst := safeRedirectURL(request.LinkOrig); dst != "" {
		values.Set("dst", dst)
	}
	if request.IP != "" {
		values.Set("ip", request.IP)
	}
	if request.MAC != "" {
		values.Set("mac", request.MAC)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

// safeRedirectURL accepts only absolute http(s) URLs, so a hostile link-orig
// cannot turn the portal into an open redirector.
func safeRedirectURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

// resolvePortalRouter decides which registered device a portal request belongs
// to, using the information a MikroTik hotspot is able to send.
//
// Order of preference:
//  1. the hotspot server-name matching a router portal tag,
//  2. the host of link-login matching a registered device address,
//  3. the router flagged as the default portal,
//  4. the only registered router when there is exactly one.
func (h *Handler) resolvePortalRouter(ctx context.Context, request portalRequest) (database.Router, error) {
	if request.ServerName != "" {
		if routers, err := h.db.Routers().FindByPortalTag(ctx, request.ServerName); err == nil && len(routers) > 0 {
			return routers[0], nil
		}
	}

	for _, raw := range []string{request.LinkLogin, request.LinkLoginOnly} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		if host == "" {
			continue
		}
		if routers, err := h.db.Routers().FindByHost(ctx, host); err == nil && len(routers) > 0 {
			return routers[0], nil
		}
	}

	if router, err := h.db.Routers().Default(ctx); err == nil {
		return router, nil
	}

	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		return database.Router{}, err
	}
	if len(routers) == 1 {
		return routers[0], nil
	}
	if len(routers) == 0 {
		return database.Router{}, errPortalNoRouter
	}
	return database.Router{}, errors.New("several routers are registered: set a portal tag or mark one as the default portal")
}

// PortalStatus is a small JSON endpoint operators can hit from the hotspot
// itself to prove the portal is reachable and correctly linked.
func (h *Handler) PortalStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	request := portalRequestFromValues(r.URL.Query())

	response := map[string]any{
		"status":  "ok",
		"portal":  h.cfg.PortalName,
		"version": h.cfg.Version,
		"router":  nil,
		"note":    "add ?server-name=<hotspot server name> or ?link-login=<url> to test router resolution",
	}
	if router, err := h.resolvePortalRouter(ctx, request); err == nil {
		response["router"] = map[string]any{
			"id":   router.ID,
			"name": router.Name,
			"host": router.Endpoint(),
		}
		response["note"] = "portal requests will be served by this router"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		h.log.Error("cannot write portal status", "error", err)
	}
}
