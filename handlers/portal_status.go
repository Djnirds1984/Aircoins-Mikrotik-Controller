package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// portalStatusPagePath is the guest facing session page.
//
// It is deliberately NOT /portal/status: that path is the operator's JSON
// probe, it has existed since before this page, and replacing it with HTML
// would break anyone using it to check the portal from a script.
const portalStatusPagePath = "/portal/session"

// portalStatusPage backs the session page a guest opens to watch the clock on
// their own connection.
type portalStatusPage struct {
	page
	// Branding is the operator's theme, background and header name, so the page
	// looks like the rest of the portal.
	Branding portalBranding
	// Online reports whether this address currently holds a session.
	Online bool
	// User, MAC, IP and RouterName identify the connection.
	User       string
	MAC        string
	IP         string
	RouterName string
	// ConnectedAt is when the session started, for a human reader.
	ConnectedAt string
	// ElapsedSeconds is how long the session has been running. The page ticks it
	// in the browser, so this only has to be right when the page is served.
	ElapsedSeconds int
	// Elapsed is ElapsedSeconds already split for display.
	Elapsed countdownParts
	// HasAllowance and RemainingSeconds describe a prepaid voucher's remaining
	// time, when the session was created from one. They are separate from the
	// elapsed clock: a customer cares about how long they have left.
	HasAllowance     bool
	RemainingSeconds int
	// Remaining is RemainingSeconds already split for display.
	Remaining countdownParts
	ExpiresAt string
	// LoginURL and AdminPath link onwards.
	LoginURL  string
	AdminPath string
	// Coin is the state of the "Insert coin" tab on the sign-in page.
	Coin coinPortal
}

// coinPortalFor resolves everything the guest's coin tab needs for one page
// render.
//
// The balance is read here rather than left to the first poll so the counter is
// correct the instant the page appears: a customer who has already inserted a
// coin and reloads the page must not watch it sit on zero for two seconds. A
// read failure is deliberately not fatal - the tab falls back to its
// server-rendered zero and the poller repairs it a moment later, which is far
// better than refusing to render a sign-in page because a credit lookup failed.
func (h *Handler) coinPortalFor(r *http.Request, request portalRequest) coinPortal {
	ctx := r.Context()
	subject := database.CoinSubject(request.MAC, request.IP)

	coin := coinPortal{
		Enabled:              strings.TrimSpace(h.cfg.CoinNodeToken) != "",
		Subject:              subject,
		StatusURL:            coinStatusPath,
		ConnectURL:           coinConnectPath,
		SecondsPerPulse:      h.cfg.CoinPulseSeconds,
		SecondsPerPulseLabel: database.FormatCoinSeconds(h.cfg.CoinPulseSeconds),
		IdleMinutes:          int(h.cfg.CoinIdleTTL.Minutes()),
	}

	// A page rendered without hotspot parameters still knows who is asking: it
	// is the address the request came from.
	if subject == "" {
		subject = database.CoinSubject("", clientIP(r))
		coin.Subject = subject
	}
	if subject == "" {
		return coin
	}

	credit, err := h.db.Coins().Get(ctx, subject)
	if err != nil {
		if !errors.Is(err, database.ErrNotFound) {
			h.log.Warn("portal cannot read the coin balance", "subject", subject, "error", err)
		}
		return coin
	}

	coin.RemainingSeconds = credit.RemainingSeconds()
	coin.SessionLabel = database.FormatCoinSeconds(credit.RemainingSeconds())
	coin.MoneyLabel = fmt.Sprintf("%.2f", float64(credit.AmountCents)/100)
	return coin
}

// PortalStatusPage serves the guest's own session view.
func (h *Handler) PortalStatusPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := &portalStatusPage{
		page:      page{Title: "Your session", Nav: ""},
		Branding:  h.portalBrandingFor(ctx),
		LoginURL:  portalLoginPath,
		AdminPath: h.cfg.AdminPath,
	}
	if view.Branding.HeaderName == "" {
		view.Branding.HeaderName = h.cfg.PortalName
	}

	if session, ok := h.portalClientSession(ctx, r); ok {
		view.Online = true
		view.User = session.Username
		view.MAC = database.FormatMAC(session.MACAddress)
		view.IP = session.Address
		view.RouterName = session.RouterName
		view.ConnectedAt = session.StartedAt.Local().Format("02 Jan 15:04:05")
		// Counted from StartedAt rather than from Session.Duration(), which ends
		// at LastSeenAt: that value only advances when a device poll lands, so a
		// clock built on it would visibly stutter between polls.
		view.ElapsedSeconds = secondsSince(session.StartedAt)
		view.Elapsed = splitCountdown(view.ElapsedSeconds)
		// A voucher-created session knows its own allowance; a username login
		// has none, and that is not an error.
		if voucher, err := h.db.Vouchers().FindByCode(ctx, session.Username); err == nil {
			view.HasAllowance = true
			view.ExpiresAt = voucher.ExpiresAt.Local().Format("02 Jan 15:04:05")
			view.RemainingSeconds = secondsUntil(voucher.ExpiresAt)
			view.Remaining = splitCountdown(view.RemainingSeconds)
		}
	}

	h.render(w, r, http.StatusOK, "status.html", view)
}

// secondsSince is how long ago t was, floored to whole seconds and clamped at
// zero so a session stamped in the future by a clock skew cannot render a
// negative countdown.
func secondsSince(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	elapsed := int(time.Since(t).Seconds())
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// secondsUntil is how much longer t is, floored to whole seconds and clamped at
// zero. An expired voucher reports 0 rather than a negative number.
func secondsUntil(t *time.Time) int {
	if t == nil || t.IsZero() {
		return 0
	}
	remaining := int(time.Until(*t).Seconds())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// countdownParts splits a number of seconds into the day/hour/minute/second
// fields the status page displays. The template prints it with {{.}}, which
// uses String.
type countdownParts struct {
	Days    int
	Hours   int
	Minutes int
	Seconds int
}

// splitCountdown breaks seconds into whole days, hours, minutes and seconds.
// Every field except days is clamped to two digits so the display keeps its
// width as the numbers grow.
func splitCountdown(total int) countdownParts {
	if total < 0 {
		total = 0
	}
	return countdownParts{
		Days:    total / 86400,
		Hours:   (total % 86400) / 3600,
		Minutes: (total % 3600) / 60,
		Seconds: total % 60,
	}
}

// String renders the clock as "3D. 04HR. 12MIN. 07SEC.".
//
// The shape is fixed at four fields on purpose: a counter whose width changes
// every second is harder to read, and the day field means a long-running
// session does not overflow into something else.
func (p countdownParts) String() string {
	return fmt.Sprintf("%dD. %02dHR. %02dMIN. %02dSEC.", p.Days, p.Hours, p.Minutes, p.Seconds)
}
