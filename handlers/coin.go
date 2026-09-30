// This file implements the Piso Wi-Fi coin slot: the hardware-facing API a
// NodeMCU posts pulses to, the balance the captive portal polls while the
// customer stands at the box, and the final "Done / Connect now" that turns
// the accumulated credit into a real MikroTik hotspot session.
//
// The design goal throughout is that a coin is never lost and never counted
// twice. The tally is durable (SQLite, not memory, so a controller restart does
// not refund every customer in the building), every hardware report carries a
// de-duplication token, and the money is only deducted after the router has
// actually authorised the client.
package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// The coin-slot HTTP surface.
//
//	POST /api/coin-pulse       a NodeMCU reports pulses for one client
//	GET  /api/coin-status      the portal polls a client's balance
//	POST /portal/coin/connect  the customer spends it on a hotspot session
//
// The two API paths are the ones a NodeMCU (which has no browser, no cookie and
// no panel session) and the portal's JavaScript (which must poll before anyone
// has signed in) can both reach, so both live outside the panel prefix and are
// authenticated by their own rules rather than by a session cookie:
//
//   - /api/coin-pulse requires the shared COIN_NODE_TOKEN. While that is unset
//     the endpoint refuses everything, so an unauthenticated POST can never
//     mint time on a controller that is, by definition, reachable from the
//     guest network.
//   - /api/coin-status is a read scoped to a single subject. The portal passes
//     the subject it was rendered with, and a client with no balance learns
//     nothing but "0".
const (
	coinPulsePath   = "/api/coin-pulse"
	coinStatusPath  = "/api/coin-status"
	coinConnectPath = "/portal/coin/connect"
	// coinTokenHeader carries the NodeMCU's shared secret.
	coinTokenHeader = "X-Coin-Token"
	// coinRateWindow is how long an unspent balance survives, mirrored from
	// Config.CoinIdleTTL. It exists only so a template can describe the rule to
	// the customer; the store enforces the same window on its expires_at.
	coinRateWindow = 20
)

// coinPulseBody is the JSON a NodeMCU POSTs.
//
// It is decoded as JSON because that is what the sketch can produce without a
// library, but the handler also accepts a urlencoded form so an operator can
// reproduce a report by hand with curl while debugging a dead acceptor.
type coinPulseBody struct {
	// Subject is the storage key, for a node that already tracks one. The
	// "mac"/"ip" pair below is the normal way in.
	Subject string `json:"subject"`
	// MAC accepts any separator style and is normalised. IP is the fallback
	// for a guest RouterOS has not identified yet.
	MAC string `json:"mac"`
	IP  string `json:"ip"`
	// Pulses is how many acceptor pulses this report covers.
	Pulses int `json:"pulses"`
	// AmountCents is the face value inserted, in the smallest currency unit.
	// Left at 0 it is derived from Pulses and the configured rate.
	AmountCents int64 `json:"amount_cents"`
	// Seconds overrides the configured per-pulse rate, for a node that knows
	// its own coin denominations. A plain pulse counter leaves it 0.
	Seconds int `json:"seconds"`
	// NodeID identifies the acceptor for the operator's audit trail.
	NodeID string `json:"node_id"`
	// EventID de-duplicates a retry. The sketch sends a monotonic counter, so
	// a POST that succeeds but whose answer is lost is not counted twice.
	EventID string `json:"event_id"`
	// RouterID attributes the credit to a known device. Optional: a kiosk with
	// one router does not need to know its id.
	RouterID int64 `json:"router_id"`
}

// coinBalance is the JSON both API endpoints answer with, so the NodeMCU and
// the browser parse one shape.
type coinBalance struct {
	// Subject is the storage key the balance belongs to.
	Subject string `json:"subject"`
	MAC     string `json:"mac,omitempty"`
	// Pulses is the running total of acceptor pulses.
	Pulses int `json:"pulses"`
	// AmountCents is the running face value inserted.
	AmountCents int64 `json:"amount_cents"`
	// RemainingSeconds is the access time still available.
	RemainingSeconds int `json:"remaining_seconds"`
	// SessionLabel renders the remaining time for the UI.
	SessionLabel string `json:"session_label"`
	// Status is "active", "connected" or "expired".
	Status string `json:"status"`
	// SecondsPerPulse is echoed so the node's own display agrees with the
	// server instead of guessing the rate.
	SecondsPerPulse int `json:"seconds_per_pulse"`
	// Duplicate reports that this event_id was already counted, so the node
	// knows to stop retrying.
	Duplicate bool `json:"duplicate,omitempty"`
	// AcceptedPulses is how many pulses this particular request added.
	AcceptedPulses int    `json:"accepted_pulses,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

// CoinPulse receives a hardware report from a coin-slot NodeMCU and adds it to
// the client's credit balance.
//
// The handler is built around one rule: an inserted coin must never be lost and
// must never be counted twice. Loss is avoided by answering 200 as soon as the
// tally is committed; duplication is avoided by the EventID check in the store.
// Anything the node gets wrong is a 400 naming the rejected field, so a
// misconfigured acceptor is diagnosable from the sketch's serial log alone.
func (h *Handler) CoinPulse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !h.coinNodeAuthorized(r) {
		h.log.Warn("coin pulse rejected", "remote", clientIP(r), "reason", "node token")
		h.writeCoinError(w, http.StatusUnauthorized, "unauthorized",
			"the coin-slot node token is missing or wrong")
		return
	}

	body, err := decodeCoinPulse(r)
	if err != nil {
		h.log.Warn("coin pulse body rejected", "remote", clientIP(r), "error", err)
		h.writeCoinError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	subject := strings.TrimSpace(body.Subject)
	if subject == "" {
		subject = database.CoinSubject(body.MAC, body.IP)
	}
	if subject == "" {
		h.writeCoinError(w, http.StatusBadRequest, "invalid_subject",
			`send "mac" (or "ip") naming the client the coins belong to`)
		return
	}

	pulses := body.Pulses
	seconds := body.Seconds
	if seconds <= 0 {
		seconds = pulses * h.cfg.CoinPulseSeconds
	}
	// A report claiming a year of access in one POST is a fault, not a sale.
	// The cap still allows a full day of coins in one burst, which covers any
	// real acceptor.
	if seconds > database.MaxCoinGrantedSecondsPerReport {
		seconds = database.MaxCoinGrantedSecondsPerReport
	}

	amount := body.AmountCents
	if amount <= 0 {
		amount = int64(pulses) * int64(h.cfg.CoinPulseCents)
	}

	pulse := database.CoinPulse{
		Subject:     subject,
		MACAddress:  body.MAC,
		NodeID:      body.NodeID,
		Pulses:      pulses,
		AmountCents: amount,
		Seconds:     seconds,
		EventID:     body.EventID,
		IdleTTL:     h.cfg.CoinIdleTTL,
	}
	if body.RouterID > 0 {
		routerID := body.RouterID
		pulse.RouterID = &routerID
	}

	credit, err := h.db.Coins().Credit(ctx, pulse, time.Now())
	switch {
	case errors.Is(err, database.ErrCoinPulseDuplicate):
		// Answered as a success on purpose: the node did nothing wrong, the
		// report simply already landed. A 4xx here would make a well-behaved
		// sketch retry a coin that is already paid for.
		h.log.Info("coin pulse already recorded", "node", body.NodeID,
			"subject", subject, "event", body.EventID)
		writeCoinJSON(w, http.StatusOK, h.coinBalanceOf(credit, 0, true))
		return
	case errors.Is(err, database.ErrCoinPulseInvalid):
		h.writeCoinError(w, http.StatusBadRequest, "invalid_pulse", err.Error())
		return
	case err != nil:
		h.fail(w, r, "apply coin pulse", err)
		return
	}

	h.log.Info("coin pulse recorded", "node", body.NodeID, "subject", subject,
		"pulses", pulses, "seconds", seconds, "remaining", credit.RemainingSeconds())
	writeCoinJSON(w, http.StatusOK, h.coinBalanceOf(credit, pulses, false))
}

// CoinStatus reports a client's current coin balance and the access time it
// buys. The portal's "Insert coin" tab polls this while the customer is
// standing at the box.
//
// The subject comes from the query string, with the requester's own address as
// the fallback so the page still works when the hotspot appended no
// parameters. "mac" is honoured because a node may have credited a hardware
// address while the page was rendered with only an IP.
func (h *Handler) CoinStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	subject := database.CoinSubject(query.Get("mac"), "")
	if subject == "" {
		subject = strings.TrimSpace(query.Get("subject"))
	}
	if subject == "" {
		subject = database.CoinSubject("", clientIP(r))
	}
	if subject == "" {
		h.writeCoinError(w, http.StatusBadRequest, "invalid_subject",
			"pass ?mac=<client mac> or ?subject=<mac:...|ip:...>")
		return
	}

	credit, err := h.db.Coins().Get(ctx, subject)
	switch {
	case errors.Is(err, database.ErrNotFound):
		// Never having inserted anything is a normal state, not an error: the
		// tab must render "0 minutes", not an error box.
		writeCoinJSON(w, http.StatusOK, h.coinBalanceOf(database.CoinCredit{
			Subject: subject,
			Status:  database.CoinActive,
		}, 0, false))
		return
	case err != nil:
		h.fail(w, r, "read coin balance", err)
		return
	}

	writeCoinJSON(w, http.StatusOK, h.coinBalanceOf(credit, 0, false))
}

// coinBalanceOf renders a stored credit as the wire shape. A zero credit (the
// "never inserted anything" case) still produces a well formed, zeroed answer
// so the browser has no special case to write.
func (h *Handler) coinBalanceOf(credit database.CoinCredit, accepted int, duplicate bool) coinBalance {
	remaining := credit.RemainingSeconds()
	return coinBalance{
		Subject:          credit.Subject,
		MAC:              database.FormatMAC(credit.MACAddress),
		Pulses:           credit.Pulses,
		AmountCents:      credit.AmountCents,
		RemainingSeconds: remaining,
		SessionLabel:     database.FormatCoinSeconds(remaining),
		Status:           credit.Status,
		SecondsPerPulse:  h.cfg.CoinPulseSeconds,
		Duplicate:        duplicate,
		AcceptedPulses:   accepted,
		UpdatedAt:        credit.UpdatedAt.Format(time.RFC3339),
	}
}

// CoinConnect spends a coin balance on a real hotspot session. It is the
// "Done / Connect now" button on the portal's coin tab.
//
// Rather than inventing a second authorisation path, the balance is turned into
// a voucher and handed to the same redeemVoucher the printed-code flow already
// uses. The device-side enforcement (limit-uptime, profile), the ledger entry,
// the session registration and the redirect therefore behave exactly as they do
// for a voucher, and there is only one path to keep working.
//
// The order is deliberate and is what makes the flow safe:
//
//  1. read the balance, and refuse if there is nothing on it;
//  2. create the voucher (a ledger row, not yet redeemed);
//  3. provision and log the client in on the router - the part that can fail;
//  4. only then deduct the spent seconds from the balance.
//
// A router that is offline at step 3 therefore leaves the customer's money on
// the balance to retry, instead of swallowing it for a session that never
// started.
func (h *Handler) CoinConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.limitPortal(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the coin form", http.StatusBadRequest)
		return
	}

	// The hotspot parameters ride on the form action exactly as they do for the
	// voucher login, so the client is still logged in against the right host and
	// returned to its original destination.
	request := portalRequestFromValues(r.Form)
	if request.IP == "" {
		request.IP = database.NormalizeIP(clientIP(r))
	}

	view := &portalPage{
		page:     page{Title: "Insert coin", Nav: ""},
		Portal:   request,
		Branding: h.portalBrandingFor(ctx),
	}

	credit, err := h.coinCreditFor(ctx, request)
	if err != nil {
		h.log.Warn("coin connect found no credit", "remote", clientIP(r), "error", err)
		view.FormError = "No coins have been credited to this device yet. Insert a coin and try again."
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}

	seconds := h.coinSessionSeconds(credit)
	if seconds <= 0 {
		view.FormError = "There is no time left on this device. Insert another coin."
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}

	router, err := h.resolvePortalRouter(ctx, request)
	if err != nil {
		view.FormError = "This hotspot is not linked to the Aircoins controller yet. Please ask the front desk for help."
		h.log.Warn("coin connect without router", "remote", clientIP(r), "error", err)
		h.renderPortal(w, r, http.StatusServiceUnavailable, view)
		return
	}
	view.RouterName = router.Name
	view.RouterKnown = true

	voucher, err := h.coinVoucher(router, credit, seconds)
	if err != nil {
		h.fail(w, r, "create the coin voucher", err)
		return
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		// Nothing has been deducted yet, so the customer can simply try again
		// once the gateway is back.
		view.FormError = "The hotspot gateway cannot be reached right now (" + routerErrorHint(err) +
			"). Your coins are still saved - please try again in a moment."
		h.renderPortal(w, r, http.StatusServiceUnavailable, view)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	redemption, err := h.redeemVoucher(callCtx, client, voucher, request.MAC, request.IP)
	if err != nil {
		h.log.Info("coin redemption failed", "subject", credit.Subject, "router", router.Name, "error", err)
		switch {
		case errors.Is(err, database.ErrVoucherNotRedeemable):
			view.FormError = strings.TrimPrefix(err.Error(), database.ErrVoucherNotRedeemable.Error()+": ")
		case errors.Is(err, ErrRouterUnreachable), errors.Is(err, ErrRouterTimeout):
			view.FormError = "The hotspot is busy or unreachable. Your coins were not used - please try again."
		default:
			view.FormError = "That credit could not be activated: " + routerErrorHint(err)
		}
		h.renderPortal(w, r, http.StatusOK, view)
		return
	}

	// The device authorised the client, so the balance may now be settled.
	if _, err := h.db.Coins().Consume(ctx, credit.Subject, seconds, time.Now()); err != nil {
		// The session is live and the time is spent. Failing the request here
		// would only invite a double spend, so it is logged and the customer is
		// let through.
		h.log.Error("cannot settle the coin balance", "subject", credit.Subject, "error", err)
	} else if _, err := h.db.Coins().Claim(ctx, credit.Subject, router.ID, time.Now()); err != nil {
		h.log.Error("cannot attribute the coin balance to a router", "subject", credit.Subject, "error", err)
	}

	h.registerPortalSession(ctx, router, redemption.Voucher.Code, request, "coin")

	view.Success = true
	view.Voucher = &redemption.Voucher
	view.Notice = defaultValue(redemption.Note,
		"Connected with "+database.FormatCoinSeconds(seconds)+" of coin credit.")
	h.finishPortalLogin(w, r, view, request, redemption.Voucher.Code, redemption.Voucher.Code, redemption.LoginViaAPI)
}

// coinCreditFor finds the balance that belongs to this request.
//
// It looks the client up by address and then by MAC, because the two can
// disagree: a node that credited a hardware address and a browser that only
// knows its DHCP lease are the same person, and either identifier may be the
// one on file. The address-keyed row is preferred when both exist, because
// that is the one the hardware has been feeding.
func (h *Handler) coinCreditFor(ctx context.Context, request portalRequest) (database.CoinCredit, error) {
	store := h.db.Coins()

	if ip := database.NormalizeIP(request.IP); ip != "" {
		if credit, err := store.Get(ctx, database.CoinSubject("", ip)); err == nil && credit.HasBalance() {
			return credit, nil
		}
	}
	if mac := database.NormalizeMAC(request.MAC); len(mac) == 12 {
		if credit, err := store.Get(ctx, database.CoinSubject(mac, "")); err == nil && credit.HasBalance() {
			return credit, nil
		}
		// The node may have posted the MAC under a different key (an address,
		// say), so fall back to a search on the recorded address.
		if credit, err := store.FindByMAC(ctx, mac); err == nil && credit.HasBalance() {
			return credit, nil
		}
	}
	return database.CoinCredit{}, database.ErrNotFound
}

// coinSessionSeconds is how much time the customer gets on this connection.
//
// It is the whole balance, capped at CoinMaxSessionMinutes. The cap exists
// because this is a public kiosk: without it, a jam in the acceptor reporting
// a burst of spurious pulses would hand out a day of access for free. A
// customer with a larger balance simply presses the button again.
func (h *Handler) coinSessionSeconds(credit database.CoinCredit) int {
	remaining := credit.RemainingSeconds()
	if limit := h.cfg.CoinMaxSessionMinutes * 60; limit > 0 && remaining > limit {
		return limit
	}
	// Round down to whole minutes: the portal clock and the session page both
	// speak in minutes, and a 90-second "session" is not worth provisioning.
	return remaining / 60 * 60
}

// coinVoucher materialises a balance as a redeemable voucher.
//
// The code carries the "COIN" prefix rather than the operator's usual "AIR" so
// the voucher list tells at a glance which sessions came from the coin box,
// and the note records the node and the pulse count for the end-of-day
// reconciliation against the acceptor.
func (h *Handler) coinVoucher(router database.Router, credit database.CoinCredit, seconds int) (database.Voucher, error) {
	ctx := context.Background()
	minutes := seconds / 60

	voucher := database.Voucher{
		Batch:           "COIN-" + time.Now().UTC().Format("20060102"),
		Profile:         "default",
		DurationMinutes: minutes,
		DeviceLimit:     1,
		PriceCents:      credit.AmountCents,
		Status:          database.VoucherUnused,
		MaxUses:         1,
		Note: fmt.Sprintf("coin slot %s: %d pulse(s), client %s",
			defaultValue(credit.NodeID, "unregistered"), credit.Pulses,
			defaultValue(database.FormatMAC(credit.MACAddress), credit.Subject)),
	}
	routerID := router.ID
	voucher.RouterID = &routerID

	// A code clash is astronomically unlikely but not impossible, and a coin
	// must not be lost to it, so the insert is retried with a fresh code.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		code, err := database.GenerateVoucherCode(database.VoucherCodeOptions{
			Prefix: "COIN", Groups: 2, GroupLength: 4,
		})
		if err != nil {
			return database.Voucher{}, err
		}
		voucher.Code = code

		if _, err := h.db.Vouchers().CreateBatch(ctx, []database.Voucher{voucher}); err != nil {
			if errors.Is(err, database.ErrDuplicateVoucherCode) {
				lastErr = err
				continue
			}
			return database.Voucher{}, err
		}
		// Read it back so the caller gets the row as stored (status, id) rather
		// than as constructed.
		return h.db.Vouchers().FindByCode(ctx, voucher.Code)
	}
	return database.Voucher{}, fmt.Errorf("could not allocate a unique coin voucher code: %w", lastErr)
}

// coinNodeAuthorized checks the NodeMCU's shared secret.
//
// The comparison is constant time so a wrong token cannot be recovered by
// timing, and an unconfigured controller rejects everything: an endpoint that
// accepts unauthenticated writes would be a free-internet button reachable from
// every guest's phone.
func (h *Handler) coinNodeAuthorized(r *http.Request) bool {
	expected := strings.TrimSpace(h.cfg.CoinNodeToken)
	if expected == "" {
		return false
	}
	presented := r.Header.Get(coinTokenHeader)
	if presented == "" {
		// The token is also accepted as a query parameter, so a node that can
		// only be pointed at a bare URL still works.
		presented = r.URL.Query().Get("token")
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}

// decodeCoinPulse reads the hardware report, accepting JSON or a form body.
//
// A form body is supported because the first thing an operator does with a coin
// box that will not count is curl it by hand, and making them work through JSON
// quoting at that moment wastes a phone call.
func decodeCoinPulse(r *http.Request) (coinPulseBody, error) {
	var body coinPulseBody

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<10)).Decode(&body); err != nil {
			return coinPulseBody{}, fmt.Errorf("the body is not valid JSON: %w", err)
		}
		return body, nil
	}

	if err := r.ParseForm(); err != nil {
		return coinPulseBody{}, fmt.Errorf("the body is not a readable form: %w", err)
	}
	form := r.Form
	body.Subject = form.Get("subject")
	body.MAC = form.Get("mac")
	body.IP = form.Get("ip")
	body.NodeID = form.Get("node_id")
	body.EventID = form.Get("event_id")

	var err error
	if body.Pulses, err = coinFormInt(form.Get("pulses")); err != nil {
		return coinPulseBody{}, fmt.Errorf("pulses: %w", err)
	}
	if body.Seconds, err = coinFormInt(form.Get("seconds")); err != nil {
		return coinPulseBody{}, fmt.Errorf("seconds: %w", err)
	}
	// The money and router-id fields are int64 on the wire but arrive as text
	// from a form, so they are parsed as an int and widened explicitly rather
	// than through a second, subtly different parser.
	var wide int
	if wide, err = coinFormInt(form.Get("amount_cents")); err != nil {
		return coinPulseBody{}, fmt.Errorf("amount_cents: %w", err)
	}
	body.AmountCents = int64(wide)
	if wide, err = coinFormInt(form.Get("router_id")); err != nil {
		return coinPulseBody{}, fmt.Errorf("router_id: %w", err)
	}
	body.RouterID = int64(wide)
	return body, nil
}

// coinFormInt parses an optional form field, naming the field in the error so
// the node's log says which one to fix.
func coinFormInt(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", raw)
	}
	return n, nil
}

// writeCoinJSON writes a machine readable answer.
func writeCoinJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The balance is live money state: a cached answer would show a customer
	// the wrong number of minutes after they paid.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeCoinError answers a machine client. It deliberately does not go through
// the HTML error page: a NodeMCU cannot read that, and a browser polling the
// status endpoint should get JSON it can branch on rather than markup.
func (h *Handler) writeCoinError(w http.ResponseWriter, status int, code, message string) {
	writeCoinJSON(w, status, map[string]any{
		"ok":      false,
		"code":    code,
		"message": message,
	})
}
