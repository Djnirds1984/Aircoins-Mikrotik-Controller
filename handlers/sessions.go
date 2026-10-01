package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

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
