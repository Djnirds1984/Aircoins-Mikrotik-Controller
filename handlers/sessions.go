package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// sessionPageSize is how many rows the history page shows per page.
const sessionPageSize = 50

// sessionsPage backs the session history view.
type sessionsPage struct {
	page
	Sessions []database.Session
	Routers  []database.Router
	Filter   sessionFilter
	Total    int64
	PageNo   int
	Pages    int
	PrevURL  string
	NextURL  string
}

// sessionFilter mirrors the query string of the history page.
type sessionFilter struct {
	RouterID int64
	Status   string
	Query    string
	MAC      string
}

// SessionsList renders the session history with filters and paging.
func (h *Handler) SessionsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	filter := sessionFilter{
		Status: strings.TrimSpace(query.Get("status")),
		Query:  strings.TrimSpace(query.Get("q")),
		MAC:    strings.TrimSpace(query.Get("mac")),
	}
	if raw := query.Get("router"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.RouterID = id
		}
	}
	pageNo := 1
	if raw := query.Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			pageNo = n
		}
	}

	dbFilter := database.SessionFilter{
		RouterID: filter.RouterID,
		Status:   filter.Status,
		Query:    filter.Query,
		MAC:      filter.MAC,
		Limit:    sessionPageSize,
		Offset:   (pageNo - 1) * sessionPageSize,
	}

	total, err := h.db.Sessions().Count(ctx, database.SessionFilter{
		RouterID: filter.RouterID, Status: filter.Status, Query: filter.Query, MAC: filter.MAC,
	})
	if err != nil {
		h.fail(w, r, "count sessions", err)
		return
	}
	sessions, err := h.db.Sessions().List(ctx, dbFilter)
	if err != nil {
		h.fail(w, r, "load sessions", err)
		return
	}
	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		h.fail(w, r, "load router inventory", err)
		return
	}

	pages := int((total + sessionPageSize - 1) / sessionPageSize)
	if pages == 0 {
		pages = 1
	}
	h.render(w, r, http.StatusOK, "sessions.html", &sessionsPage{
		page:     page{Title: "Sessions", Nav: "sessions"},
		Sessions: sessions,
		Routers:  routers,
		Filter:   filter,
		Total:    total,
		PageNo:   pageNo,
		Pages:    pages,
		PrevURL:  sessionPageURL(filter, pageNo-1),
		NextURL:  sessionPageURL(filter, pageNo+1),
	})
}

// sessionPageURL builds a history link preserving the current filters.
func sessionPageURL(filter sessionFilter, page int) string {
	if page < 1 {
		page = 1
	}
	values := url.Values{}
	if filter.RouterID > 0 {
		values.Set("router", strconv.FormatInt(filter.RouterID, 10))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Query != "" {
		values.Set("q", filter.Query)
	}
	if filter.MAC != "" {
		values.Set("mac", filter.MAC)
	}
	values.Set("page", strconv.Itoa(page))
	return "/sessions?" + values.Encode()
}

// SessionDisconnect ends a live hotspot session on the device and marks it
// closed locally. The optional "block" checkbox also creates a blocked IP
// binding so the client cannot come straight back.
func (h *Handler) SessionDisconnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
	}
	back := "/routers/" + strconv.FormatInt(id, 10)

	sessionKey := strings.TrimSpace(r.PostFormValue("session_key"))
	username := strings.TrimSpace(r.PostFormValue("username"))
	mac := strings.TrimSpace(r.PostFormValue("mac"))
	block := r.PostFormValue("block") != ""
	if sessionKey == "" && username == "" {
		h.flashAndRedirect(w, r, back, "err", "The client could not be identified; reload the device page and try again.")
		return
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashErr(w, r, back, "Cannot reach the router to disconnect the client", err)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	disconnectErr := client.DisconnectClient(callCtx, sessionKey, username)
	if disconnectErr != nil && !errors.Is(disconnectErr, ErrRouterNotFound) {
		h.flashErr(w, r, back, "Disconnect failed", disconnectErr)
		return
	}

	// Mirror the change locally so the controller history is consistent even if
	// the device was already gone.
	if _, err := h.db.Sessions().Close(ctx, router.ID, sessionKey, "disconnected by operator", time.Now()); err != nil {
		h.log.Error("cannot close local session", "router", router.Name, "session", sessionKey, "error", err)
	}
	if username != "" {
		if _, err := h.db.Sessions().CloseByUser(ctx, router.ID, username, "disconnected by operator", time.Now()); err != nil {
			h.log.Error("cannot close local sessions by user", "username", username, "error", err)
		}
	}

	message := "Disconnected " + defaultValue(username, sessionKey) + " from " + router.Name
	if disconnectErr != nil {
		message += " (the device no longer had that session)"
	}

	if block && mac != "" {
		if _, err := client.BlockMAC(callCtx, mac, "blocked by aircoins controller"); err != nil {
			h.flashAndRedirect(w, r, back, "warn",
				message+", but blocking "+mac+" failed: "+routerErrorHint(err))
			return
		}
		if _, err := h.db.Sessions().CloseByMAC(ctx, router.ID, mac, "blocked by operator", time.Now()); err != nil {
			h.log.Error("cannot close local sessions by mac", "mac", mac, "error", err)
		}
		message += " and blocked " + database.FormatMAC(mac) + " with an IP binding"
	}
	h.flashAndRedirect(w, r, back, "ok", message)
}

// ClientBlock blocks a client MAC address with a hotspot IP binding.
func (h *Handler) ClientBlock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
	}
	back := "/routers/" + strconv.FormatInt(id, 10)

	mac := strings.TrimSpace(r.PostFormValue("mac"))
	if database.NormalizeMAC(mac) == "" {
		h.flashAndRedirect(w, r, back, "err", "Enter a MAC address to block (for example AA:BB:CC:DD:EE:FF).")
		return
	}
	comment := strings.TrimSpace(r.PostFormValue("comment"))
	if comment == "" {
		comment = "blocked by operator"
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashErr(w, r, back, "Cannot reach the router to block the client", err)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	bindingID, err := client.BlockMAC(callCtx, mac, comment)
	if err != nil {
		h.flashErr(w, r, back, "Block failed", err)
		return
	}
	if _, err := h.db.Sessions().CloseByMAC(ctx, router.ID, mac, "blocked by operator", time.Now()); err != nil {
		h.log.Error("cannot close local sessions by mac", "mac", mac, "error", err)
	}

	message := database.FormatMAC(mac) + " is now blocked on " + router.Name
	if bindingID != "" {
		message += " (IP binding " + bindingID + ")"
	}
	h.flashAndRedirect(w, r, back, "ok", message)
}

// ClientUnblock removes a hotspot IP binding, restoring access.
func (h *Handler) ClientUnblock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
	}
	back := "/routers/" + strconv.FormatInt(id, 10)

	bindingID := strings.TrimSpace(r.PostFormValue("binding_id"))
	if bindingID == "" {
		h.flashAndRedirect(w, r, back, "err", "No IP binding was selected.")
		return
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashErr(w, r, back, "Cannot reach the router to remove the binding", err)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	if err := client.UnblockBinding(callCtx, bindingID); err != nil {
		h.flashErr(w, r, back, "Removing the IP binding failed", err)
		return
	}
	h.flashAndRedirect(w, r, back, "ok", "IP binding "+bindingID+" removed from "+router.Name)
}
