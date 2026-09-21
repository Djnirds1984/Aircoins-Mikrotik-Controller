package handlers

import (
	"io"
	"net/http"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// dashboardPage backs the controller overview.
type dashboardPage struct {
	page
	Stats    database.Stats
	Routers  []routerSummary
	Sessions []database.Session
	Vouchers []database.Voucher
	Batches  []string
}

// routerSummary combines a router with its locally tracked counters.
type routerSummary struct {
	Router       database.Router
	OpenSessions int64
	Vouchers     int64
}

// Dashboard renders the landing page: fleet health, live clients and the
// voucher ledger at a glance.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stats, err := h.db.Dashboard(ctx)
	if err != nil {
		h.fail(w, r, "load dashboard statistics", err)
		return
	}
	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		h.fail(w, r, "load router inventory", err)
		return
	}
	openCounts, err := h.db.Sessions().OpenCountsByRouter(ctx)
	if err != nil {
		h.fail(w, r, "count active sessions", err)
		return
	}
	voucherCounts, err := h.db.Vouchers().CountsByRouter(ctx)
	if err != nil {
		h.fail(w, r, "count vouchers", err)
		return
	}
	sessions, err := h.db.Sessions().List(ctx, database.SessionFilter{Status: "open", Limit: 12})
	if err != nil {
		h.fail(w, r, "load active sessions", err)
		return
	}
	vouchers, err := h.db.Vouchers().List(ctx, database.VoucherFilter{Limit: 8})
	if err != nil {
		h.fail(w, r, "load recent vouchers", err)
		return
	}
	batches, err := h.db.Vouchers().ListBatches(ctx)
	if err != nil {
		h.fail(w, r, "load voucher batches", err)
		return
	}

	summaries := make([]routerSummary, 0, len(routers))
	for _, router := range routers {
		summaries = append(summaries, routerSummary{
			Router:       router,
			OpenSessions: openCounts[router.ID],
			Vouchers:     voucherCounts[router.ID],
		})
	}

	h.render(w, r, http.StatusOK, "dashboard.html", &dashboardPage{
		page:     page{Title: "Dashboard", Nav: "dashboard"},
		Stats:    stats,
		Routers:  summaries,
		Sessions: sessions,
		Vouchers: vouchers,
		Batches:  batches,
	})
}

// Health is the liveness probe used by systemd and load balancers.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := h.db.Check(r.Context()); err != nil {
		h.log.Error("health check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "unhealthy\n")
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

// errorPage is rendered when a page level load fails.
type errorPage struct {
	page
	Message string
	Detail  string
}

// fail logs a page level error and renders the error template.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("request failed", "what", what, "path", r.URL.Path, "error", err)
	h.render(w, r, http.StatusInternalServerError, "error.html", &errorPage{
		page:    page{Title: "Something went wrong", Nav: ""},
		Message: what,
		Detail:  err.Error(),
	})
}

// flashErr is the common "action failed" path: tell the user what went wrong
// and send them back to the page they came from.
func (h *Handler) flashErr(w http.ResponseWriter, r *http.Request, back, what string, err error) {
	h.log.Error("action failed", "what", what, "path", r.URL.Path, "error", err)
	h.flashAndRedirect(w, r, back, "err", strings.TrimSpace(what+": "+err.Error()))
}
