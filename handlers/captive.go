package handlers

import (
	"context"
	"net/http"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// captivePage backs the welcome page every hotspot client sees when it opens
// the IP address of the controller: http://<board-ip>/ .
//
// The panel itself lives one level down, under /admin, so a plain visit never
// exposes the fleet dashboard to a paying guest.
type captivePage struct {
	page
	// Portal carries the MikroTik hotspot parameters when the client was
	// redirected here by the device, and is empty on a direct visit.
	Portal      portalRequest
	RouterName  string
	RouterKnown bool
	// Online reports that this client address already holds a live session,
	// so the page congratulates it instead of asking it to sign in again.
	Online      bool
	OnlineUser  string
	OnlineSince string
	OnlineUsed  string
	// LoginURL is the sign-in form, with the hotspot parameters carried over
	// so the login POST can still complete the MikroTik handshake.
	LoginURL string
	// Tagline is the operator supplied welcome line.
	Tagline string
	// Support is the operator supplied contact line ("Ask at the counter...").
	Support string
	// AdminPath is where the panel lives, so the page can link to it.
	AdminPath string
}

// PortalIndex serves the captive portal welcome page at the root of the
// controller: http://<board-ip>/ .
//
// A hotspot that redirects a client here appends its own parameters
// (?link-login=..., ?mac=..., ?link-orig=..., ?server-name=...). Those clients
// are handed straight to the sign-in form, because the welcome page has nothing
// to add for them and the login flow needs the parameters intact.
func (h *Handler) PortalIndex(w http.ResponseWriter, r *http.Request) {
	request := portalRequestFromValues(r.URL.Query())
	if !request.empty() {
		h.log.Debug("portal index received hotspot parameters, forwarding to sign-in",
			"remote", clientIP(r), "mac", request.MAC, "ip", request.IP)
		h.PortalLogin(w, r)
		return
	}

	ctx := r.Context()
	view := &captivePage{
		page:      page{Title: "Wi-Fi sign in", Nav: ""},
		Portal:    request,
		Tagline:   h.cfg.PortalTagline,
		Support:   h.cfg.PortalSupport,
		AdminPath: h.cfg.AdminPath,
		LoginURL:  "/portal/login",
	}

	// The router is only used to brand the page; an unresolvable one must not
	// turn a guest's welcome page into an error page.
	if router, err := h.resolvePortalRouter(ctx, request); err == nil {
		view.RouterName = router.Name
		view.RouterKnown = true
	} else {
		h.log.Warn("portal welcome page has no router", "remote", clientIP(r), "error", err)
	}

	if session, ok := h.portalClientSession(ctx, r); ok {
		view.Online = true
		view.OnlineUser = session.Username
		view.OnlineSince = session.StartedAt.Local().Format("15:04")
		view.OnlineUsed = durationLabel(session.Duration())
	}

	h.render(w, r, http.StatusOK, "captive.html", view)
}

// portalClientSession looks for a live session owned by the address of the
// current request. The controller stores the guest address, so this is what
// lets the welcome page greet a client that already signed in instead of
// showing it a form it does not need.
//
// A database error degrades to "not online": the guest still gets the page.
func (h *Handler) portalClientSession(ctx context.Context, r *http.Request) (database.Session, bool) {
	ip := clientIP(r)
	if ip == "" {
		return database.Session{}, false
	}
	sessions, err := h.db.Sessions().List(ctx, database.SessionFilter{Status: "open", Query: ip, Limit: 10})
	if err != nil {
		h.log.Warn("portal cannot read active sessions", "remote", ip, "error", err)
		return database.Session{}, false
	}
	for _, session := range sessions {
		// The filter is a substring match on username, address and MAC, so
		// confirm the address really is this client before believing it.
		if session.Address == ip {
			return session, true
		}
	}
	return database.Session{}, false
}
