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
	// Branding is the operator's theme, header name, background and extra HTML,
	// resolved from the PORTAL editor.
	Branding portalBranding
	// Coin is the state of the "Insert coin" tab.
	Coin coinPortal
	// Clock is the big timer under the status title. ClockMode says which way
	// it runs: "up" for a connected session counting its own time online, "down"
	// for the credit a customer is about to spend, and "" when there is nothing
	// to show yet (a guest who has inserted nothing).
	Clock        countdownParts
	ClockSeconds int
	ClockMode    string
}

// coinPortal is everything the guest's coin tab needs.
//
// It is a value, not a pointer, and it is always populated: the tab is shown on
// every install, because an operator wires the hardware up long after the panel
// was first deployed and a tab that appears only after a restart (or worse, only
// when a coin box is detected) is one nobody will find. When the hardware is not
// connected the tab simply stays at zero, which is the honest state.
type coinPortal struct {
	// Enabled reports whether the operator configured a node token. Without one
	// the write endpoint refuses every report, so the tab would never move; it
	// is rendered disabled with an explanation instead.
	Enabled bool
	// Subject is the storage key this page's balance lives under. It is what the
	// poller asks for, and it is rendered into the page as a data attribute.
	Subject string
	// StatusURL and ConnectURL are the endpoints the tab's script talks to.
	StatusURL  string
	ConnectURL string
	// SecondsPerPulse is echoed so the customer's "each coin buys X" line is
	// computed from the server's rate, never from a number typed into the HTML.
	SecondsPerPulse int
	// SecondsPerPulseLabel renders the same value for a human.
	SecondsPerPulseLabel string
	// RemainingSeconds and SessionLabel are the server-rendered starting values,
	// so the counter is right the instant the page loads rather than after the
	// first poll.
	RemainingSeconds int
	SessionLabel     string
	// MoneyLabel is the amount inserted so far, for the operator-facing
	// reconciliation on the guest's own screen.
	MoneyLabel string
	// AmountCents is the same figure in the smallest currency unit. It is what
	// the coin poller seeds itself from, so the rendered amount is not
	// repainted as 0.00 before the first poll comes back.
	AmountCents int64
	// Points is the credit expressed in whole minutes - the same number the
	// kiosk's "Points" field shows. It is a display alias for RemainingSeconds,
	// kept so the template never has to do the division itself.
	Points int
	// IdleMinutes is how long an unspent balance survives, so the tab can set
	// the customer's expectation instead of leaving a coin to evaporate.
	IdleMinutes int
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
		Branding:  h.portalBrandingFor(ctx),
		Coin:      h.coinPortalFor(r, request),
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
		// The big clock counts a live session UP. It is measured from
		// StartedAt rather than from session.Duration(), which ends at
		// LastSeenAt: that value only advances when a device poll lands, so a
		// clock built on it would visibly stutter between polls.
		view.ClockSeconds = secondsSince(session.StartedAt)
		view.Clock = splitCountdown(view.ClockSeconds)
		view.ClockMode = "up"
	} else if view.Coin.RemainingSeconds > 0 {
		// Not connected yet, but the customer has already paid: show the credit
		// counting DOWN towards zero, because that is what pressing "Done" does.
		view.ClockSeconds = view.Coin.RemainingSeconds
		view.Clock = splitCountdown(view.ClockSeconds)
		view.ClockMode = "down"
	}

	h.render(w, r, http.StatusOK, "captive.html", view)
}

// PortalProbe answers an operating system's captive-portal detection request
// with the portal.
//
// The probe URLs carry no hotspot parameters, so this deliberately does not go
// through PortalIndex: that handler looks for a MAC/IP, finds none, and still
// renders the welcome page - correct, but it also logs a "no router" warning on
// every single probe. Keeping the two apart makes a probe cheap and quiet while
// a real visit to "/" behaves exactly as before.
func (h *Handler) PortalProbe(w http.ResponseWriter, r *http.Request) {
	// If a hotspot did manage to append its parameters, the guest should end up
	// on the real sign-in form with them intact.
	if request := portalRequestFromValues(r.URL.Query()); !request.empty() {
		h.PortalLogin(w, r)
		return
	}

	view := &captivePage{
		page:      page{Title: "Wi-Fi sign in", Nav: ""},
		Tagline:   h.cfg.PortalTagline,
		Support:   h.cfg.PortalSupport,
		AdminPath: h.cfg.AdminPath,
		LoginURL:  "/portal/login",
		Branding:  h.portalBrandingFor(r.Context()),
		Coin:      h.coinPortalFor(r, portalRequest{}),
	}
	if router, err := h.resolvePortalRouter(r.Context(), portalRequest{}); err == nil {
		view.RouterName = router.Name
		view.RouterKnown = true
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
