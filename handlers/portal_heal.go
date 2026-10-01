package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// Self-healing handoff.
//
// Everything a guest sees starts at one tiny file on the router, hotspot/
// login.html, which hands the browser to this panel. If that file is missing,
// stale, or a broken copy of something else, the guest gets a scrambled page
// or a 404, and no amount of correct controller code can fix what the ROUTER
// answers. Operators used to be told to re-run a /tool fetch whenever that
// happened; this makes the panel notice and repair it on its own, the next
// time any guest proves the path is being used.
//
// The work runs off the request goroutine on a clock: each router is checked
// at most once per portalHealInterval, so a busy hotspot does not hammer the
// device's API and a broken file is fixed within minutes of appearing.

// portalHealInterval is how long a successful-or-unneeded check buys a router
// before the next guest load looks again.
const portalHealInterval = 15 * time.Minute

// portalHealTimeout bounds one whole check-and-repair cycle. It is generous
// because the repair is a round trip the ROUTER makes back to the panel.
const portalHealTimeout = 45 * time.Second

// ensureRouterHandoff schedules the self-heal for the router serving this
// guest, using the address THIS request arrived on.
//
// That address is the right one twice over: it is what guests can already
// reach, and it is the same LAN address the router reaches the panel on, which
// is exactly what the failed fetches of the past got wrong.
func (h *Handler) ensureRouterHandoff(r *http.Request, router database.Router) {
	base := portalBaseURL(r)
	if base == "" {
		return
	}
	if !h.portalHealDue(router.ID) {
		return
	}
	go h.repairRouterHandoff(router, base+portalRouterLoginPath)
}

// portalHealDue claims this router's next check window. Claiming before the
// work starts means a crowd of concurrent guests triggers exactly one check,
// not one per page view.
func (h *Handler) portalHealDue(routerID int64) bool {
	h.portalHealMu.Lock()
	defer h.portalHealMu.Unlock()
	if h.portalHealAt == nil {
		h.portalHealAt = map[int64]time.Time{}
	}
	if last, seen := h.portalHealAt[routerID]; seen && time.Since(last) < portalHealInterval {
		return false
	}
	h.portalHealAt[routerID] = time.Now()
	return true
}

// repairRouterHandoff reads what login page the device is actually serving
// and reinstalls the panel handoff when it is missing or provably broken.
//
// It never blocks or fails a guest: every path out is a log line. The install
// itself removes the old file first (a /tool fetch never overwrites) and
// verifies the content by the handoff marker, so "repaired" here means the
// same thing the one-click installer's button reports.
func (h *Handler) repairRouterHandoff(router database.Router, fetchURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), portalHealTimeout)
	defer cancel()

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.log.Debug("portal self-heal could not reach the router", "router", router.Name, "error", err)
		return
	}
	defer client.Close()

	state, err := client.HotspotPortalState(ctx)
	if err != nil {
		h.log.Debug("portal self-heal could not read the login page state", "router", router.Name, "error", err)
		return
	}
	if state.Verified {
		return
	}

	h.log.Info("router login page is broken, repairing automatically",
		"router", router.Name, "file", state.DestPath, "found", state.Size > 0)
	result, err := client.InstallHotspotPortal(ctx, fetchURL)
	if err != nil {
		h.log.Warn("automatic portal repair failed", "router", router.Name, "error", err)
		return
	}
	if result.Verified {
		h.log.Info("automatic portal repair complete", "router", router.Name, "file", result.DestPath)
	} else {
		h.log.Warn("automatic portal repair did not verify", "router", router.Name, "steps", result.Steps)
	}
}
