// Package handlers contains the HTTP layer of the Aircoins MikroTik
// controller: the admin dashboard, the router inventory CRUD, the live device
// manager, the voucher engine, the captive portal, and the REST API.
//
// The REST API lives under /api/v1 and speaks JSON. It reuses the same
// MikrotikClient from mikrotik.go, so every endpoint inherits the reconnecting
// RouterOS API client and the classified RouterError sentinels.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// writeJSON writes a JSON response with the given status code.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error("write json response", "status", status, "error", err)
	}
}

// writeJSONError writes a standardised JSON error body.
func (h *Handler) writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	h.writeJSON(w, status, jsonError{Code: code, Message: message})
}

// jsonError is the body of every API error response.
type jsonError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// jsonRouter is the public shape of a router row.
type jsonRouter struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	Username      string    `json:"username"`
	UseTLS        bool      `json:"use_tls"`
	VerifyTLS     bool      `json:"verify_tls"`
	Location      string    `json:"location"`
	PortalTag     string    `json:"portal_tag"`
	DefaultPortal bool      `json:"default_portal"`
	Notes         string    `json:"notes"`
	LastStatus    string    `json:"last_status"`
	LastError     string    `json:"last_error"`
	LastLatencyMS int64     `json:"last_latency_ms"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// toJSONRouter converts a database Router into the public JSON shape.
func toJSONRouter(r database.Router) jsonRouter {
	return jsonRouter{
		ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port,
		Username: r.Username, UseTLS: r.UseTLS, VerifyTLS: r.VerifyTLS,
		Location: r.Location, PortalTag: r.PortalTag,
		DefaultPortal: r.DefaultPortal, Notes: r.Notes,
		LastStatus: r.LastStatus, LastError: r.LastError,
		LastLatencyMS: r.LastLatencyMS, LastSeenAt: r.LastSeenAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// jsonRouterList is the envelope for GET /api/v1/routers.
type jsonRouterList struct {
	Routers []jsonRouter `json:"routers"`
	Total   int64        `json:"total"`
}

// jsonClient is the public shape of an active hotspot client.
type jsonClient struct {
	ID         string `json:"id"`
	User       string `json:"user"`
	Address    string `json:"address"`
	MACAddress string `json:"mac_address"`
	Uptime     string `json:"uptime"`
	LoginBy    string `json:"login_by"`
	Server     string `json:"server"`
	Comment    string `json:"comment"`
	BytesIn    int64  `json:"bytes_in"`
	BytesOut   int64  `json:"bytes_out"`
}

// toJSONClient converts a HotspotActive into the public JSON shape.
func toJSONClient(c HotspotActive) jsonClient {
	return jsonClient{
		ID: c.ID, User: c.User, Address: c.Address, MACAddress: c.MACAddress,
		Uptime: c.Uptime, LoginBy: c.LoginBy, Server: c.Server,
		Comment: c.Comment, BytesIn: c.BytesIn, BytesOut: c.BytesOut,
	}
}

// jsonDeviceInfo is the public shape of /system/identity + /system/resource.
type jsonDeviceInfo struct {
	Identity     string `json:"identity"`
	Version      string `json:"version"`
	BoardName    string `json:"board_name"`
	Architecture string `json:"architecture"`
	Uptime       string `json:"uptime"`
	CPULoad      int    `json:"cpu_load"`
	FreeMemory   int64  `json:"free_memory"`
	TotalMemory  int64  `json:"total_memory"`
}

// toJSONDeviceInfo converts a DeviceInfo into the public JSON shape.
func toJSONDeviceInfo(i DeviceInfo) jsonDeviceInfo {
	return jsonDeviceInfo{
		Identity: i.Identity, Version: i.Version, BoardName: i.BoardName,
		Architecture: i.Architecture, Uptime: i.Uptime, CPULoad: i.CPULoad,
		FreeMemory: i.FreeMemory, TotalMemory: i.TotalMemory,
	}
}

// jsonBinding is the public shape of a hotspot IP binding.
type jsonBinding struct {
	ID         string `json:"id"`
	MACAddress string `json:"mac_address"`
	Address    string `json:"address"`
	ToAddress  string `json:"to_address"`
	Server     string `json:"server"`
	Type       string `json:"type"`
	Comment    string `json:"comment"`
	Disabled   bool   `json:"disabled"`
}

// toJSONBinding converts an IPBinding into the public JSON shape.
func toJSONBinding(b IPBinding) jsonBinding {
	return jsonBinding{
		ID: b.ID, MACAddress: b.MACAddress, Address: b.Address,
		ToAddress: b.ToAddress, Server: b.Server, Type: b.Type,
		Comment: b.Comment, Disabled: b.Disabled,
	}
}

// jsonBindingList is the envelope for GET /api/v1/routers/{id}/bindings.
type jsonBindingList struct {
	Bindings []jsonBinding `json:"bindings"`
	Total    int           `json:"total"`
}

// jsonVoucher is the public shape of a voucher row. The password is never
// exposed.
type jsonVoucher struct {
	ID           int64      `json:"id"`
	Code         string     `json:"code"`
	RouterID     int64      `json:"router_id"`
	User         string     `json:"user"`
	Status       string     `json:"status"`
	TimeLimit    string     `json:"time_limit"`
	DataLimitMB  int        `json:"data_limit_mb"`
	ExpiresAt    time.Time  `json:"expires_at"`
	UsedBytes    int64      `json:"used_bytes"`
	UsedTime     string     `json:"used_time"`
	CreatedAt    time.Time  `json:"created_at"`
	RedeemedAt   *time.Time `json:"redeemed_at"`
	MAC          string     `json:"mac,omitempty"`
	IP           string     `json:"ip,omitempty"`
	Serial       string     `json:"serial,omitempty"`
}

// toJSONVoucher converts a database Voucher into the public JSON shape.
func toJSONVoucher(v database.Voucher) jsonVoucher {
	return jsonVoucher{
		ID: v.ID, Code: v.Code, RouterID: v.RouterID, User: v.User,
		Status: v.Status, TimeLimit: v.TimeLimit, DataLimitMB: v.DataLimitMB,
		ExpiresAt: v.ExpiresAt, UsedBytes: v.UsedBytes, UsedTime: v.UsedTime,
		CreatedAt: v.CreatedAt, RedeemedAt: v.RedeemedAt,
		MAC: v.MAC, IP: v.IP, Serial: v.Serial,
	}
}

// jsonCreateRouter is the request body for POST /api/v1/routers.
type jsonCreateRouter struct {
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	UseTLS        bool   `json:"use_tls"`
	VerifyTLS     bool   `json:"verify_tls"`
	Location      string `json:"location"`
	PortalTag     string `json:"portal_tag"`
	DefaultPortal bool   `json:"default_portal"`
	Notes         string `json:"notes"`
}

// validateCreateRouter checks the create request.
func (r jsonCreateRouter) validate() string {
	if strings.TrimSpace(r.Name) == "" {
		return "name is required"
	}
	if len(r.Name) > 60 {
		return "name must be at most 60 characters"
	}
	if strings.TrimSpace(r.Host) == "" {
		return "host is required"
	}
	if r.Port <= 0 || r.Port > 65535 {
		return "port must be between 1 and 65535"
	}
	if strings.TrimSpace(r.Username) == "" {
		return "username is required"
	}
	if r.Password == "" {
		return "password is required"
	}
	if len(r.PortalTag) > 40 {
		return "portal_tag must be at most 40 characters"
	}
	return ""
}

// jsonUpdateRouter is the request body for PUT /api/v1/routers/{id}.
type jsonUpdateRouter struct {
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	UseTLS        bool   `json:"use_tls"`
	VerifyTLS     bool   `json:"verify_tls"`
	Location      string `json:"location"`
	PortalTag     string `json:"portal_tag"`
	DefaultPortal bool   `json:"default_portal"`
	Notes         string `json:"notes"`
}

// validateUpdateRouter checks the update request.
func (r jsonUpdateRouter) validate() string {
	if strings.TrimSpace(r.Name) == "" {
		return "name is required"
	}
	if len(r.Name) > 60 {
		return "name must be at most 60 characters"
	}
	if strings.TrimSpace(r.Host) == "" {
		return "host is required"
	}
	if r.Port <= 0 || r.Port > 65535 {
		return "port must be between 1 and 65535"
	}
	if strings.TrimSpace(r.Username) == "" {
		return "username is required"
	}
	if len(r.PortalTag) > 40 {
		return "portal_tag must be at most 40 characters"
	}
	return ""
}

// toRouter converts the update request into a database Router.
func (r jsonUpdateRouter) toRouter() database.Router {
	return database.Router{
		Name: strings.TrimSpace(r.Name), Host: strings.TrimSpace(r.Host),
		Port: r.Port, Username: strings.TrimSpace(r.Username),
		Password: r.Password, UseTLS: r.UseTLS, VerifyTLS: r.VerifyTLS,
		Location: strings.TrimSpace(r.Location),
		PortalTag: strings.TrimSpace(r.PortalTag),
		DefaultPortal: r.DefaultPortal, Notes: strings.TrimSpace(r.Notes),
	}
}

// jsonCreateVoucher is the request body for POST /api/v1/vouchers.
type jsonCreateVoucher struct {
	RouterID    int64  `json:"router_id"`
	User        string `json:"user"`
	Password     string `json:"password"`
	TimeLimit   string `json:"time_limit"`
	DataLimitMB int    `json:"data_limit_mb"`
	Count       int    `json:"count"`
}

// validateCreateVoucher checks the create request.
func (r jsonCreateVoucher) validate() string {
	if r.RouterID <= 0 {
		return "router_id is required"
	}
	if strings.TrimSpace(r.User) == "" {
		return "user is required"
	}
	if r.Password == "" {
		return "password is required"
	}
	if r.Count <= 0 {
		return "count must be at least 1"
	}
	if r.DataLimitMB < 0 {
		return "data_limit_mb must be zero or positive"
	}
	return ""
}

// toSpec converts the create request into a database VoucherSpec.
func (r jsonCreateVoucher) toSpec() database.VoucherSpec {
	return database.VoucherSpec{
		User: strings.TrimSpace(r.User), Password: r.Password,
		TimeLimit: r.TimeLimit, DataLimitMB: r.DataLimitMB, Count: r.Count,
	}
}

// jsonCommand is the request body for POST /api/v1/routers/{id}/command.
type jsonCommand struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// validateCommand checks the command request.
func (r jsonCommand) validate() string {
	if strings.TrimSpace(r.Command) == "" {
		return "command is required"
	}
	return ""
}

// jsonCommandReply is the response body for a command execution.
type jsonCommandReply struct {
	ID   string               `json:"id"`
	Rows []map[string]string  `json:"rows"`
	Done map[string]string    `json:"done"`
}

// jsonPortalLoginRequest is the request body for POST /api/v1/portal/login.
type jsonPortalLoginRequest struct {
	MAC           string `json:"mac"`
	IP            string `json:"ip"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	LinkLogin     string `json:"link_login"`
	LinkLoginOnly string `json:"link_login_only"`
	LinkOrig      string `json:"link_orig"`
	ServerName    string `json:"server_name"`
	ChapID        string `json:"chap_id"`
	ChapChallenge string `json:"chap_challenge"`
}

// ---------------------------------------------------------------------------
// API route registration
// ---------------------------------------------------------------------------

// RoutesAPI wires the REST API endpoints under /api/v1. It is called from
// Routes() so the API shares the same middleware (security headers, CSRF,
// panic recovery) as the web UI.
func (h *Handler) RoutesAPI(mux *http.ServeMux) {
	// Discovery and health.
	mux.HandleFunc("GET /api/v1/health", h.apiHealth)

	// Router inventory.
	mux.HandleFunc("GET /api/v1/routers", h.apiRoutersList)
	mux.HandleFunc("POST /api/v1/routers", h.apiRouterCreate)
	mux.HandleFunc("GET /api/v1/routers/{id}", h.apiRouterGet)
	mux.HandleFunc("PUT /api/v1/routers/{id}", h.apiRouterUpdate)
	mux.HandleFunc("DELETE /api/v1/routers/{id}", h.apiRouterDelete)
	mux.HandleFunc("POST /api/v1/routers/{id}/test", h.apiRouterTest)
	mux.HandleFunc("GET /api/v1/routers/{id}/device", h.apiRouterDevice)
	mux.HandleFunc("GET /api/v1/routers/{id}/clients", h.apiRouterClients)
	mux.HandleFunc("GET /api/v1/routers/{id}/bindings", h.apiRouterBindings)
	mux.HandleFunc("POST /api/v1/routers/{id}/clients/{clientId:(.+)}/disconnect", h.apiClientDisconnect)
	mux.HandleFunc("POST /api/v1/routers/{id}/clients/block", h.apiClientBlock)
	mux.HandleFunc("POST /api/v1/routers/{id}/clients/unblock", h.apiClientUnblock)
	mux.HandleFunc("POST /api/v1/routers/{id}/command", h.apiCommand)

	// Vouchers.
	mux.HandleFunc("GET /api/v1/vouchers", h.apiVouchersList)
	mux.HandleFunc("POST /api/v1/vouchers", h.apiVoucherCreate)
	mux.HandleFunc("GET /api/v1/vouchers/{code}", h.apiVoucherGet)
	mux.HandleFunc("POST /api/v1/vouchers/{code}/redeem", h.apiVoucherRedeem)
	mux.HandleFunc("DELETE /api/v1/vouchers/{id}", h.apiVoucherDelete)

	// Portal login accepts the MikroTik redirect parameters either as JSON or
	// as a standard form, so external systems and the captive portal can both
	// use the same endpoint.
	mux.HandleFunc("POST /api/v1/portal/login", h.apiPortalLogin)
}

// withCORS wraps a handler with lightweight CORS headers suitable for a
// controller that may be called from a browser admin UI or an external system.
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Router inventory
// ---------------------------------------------------------------------------

// apiRoutersList returns every registered router.
func (h *Handler) apiRoutersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router inventory")
		h.log.Error("api routers list", "error", err)
		return
	}
	out := make([]jsonRouter, 0, len(routers))
	for _, router := range routers {
		out = append(out, toJSONRouter(router))
	}
	h.writeJSON(w, http.StatusOK, jsonRouterList{
		Routers: out,
		Total:   int64(len(out)),
	})
}

// apiRouterCreate registers a new router from a JSON body.
func (h *Handler) apiRouterCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req jsonCreateRouter
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"request body must be valid JSON")
		return
	}
	if msg := req.validate(); msg != "" {
		h.writeJSONError(w, http.StatusUnprocessableEntity, "validation_error", msg)
		return
	}
	router := req.toRouter()
	created, err := h.db.Routers().Create(ctx, router)
	if err != nil {
		h.log.Error("api router create", "name", router.Name, "error", err)
		if errors.Is(err, database.ErrDuplicateName) {
			h.writeJSONError(w, http.StatusConflict, "duplicate_name",
				fmt.Sprintf("a router named %q already exists", router.Name))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to create router")
		return
	}
	h.writeJSON(w, http.StatusCreated, toJSONRouter(created))
}

// apiRouterGet returns a single router by id.
func (h *Handler) apiRouterGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api router get", "id", id, "error", err)
		return
	}
	h.writeJSON(w, http.StatusOK, toJSONRouter(router))
}

// apiRouterUpdate updates a router from a JSON body.
func (h *Handler) apiRouterUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	var req jsonUpdateRouter
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"request body must be valid JSON")
		return
	}
	if msg := req.validate(); msg != "" {
		h.writeJSONError(w, http.StatusUnprocessableEntity, "validation_error", msg)
		return
	}
	existing, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api router update", "id", id, "error", err)
		return
	}
	router := req.toRouter()
	router.ID = id
	if router.Password == "" {
		router.Password = existing.Password
	}
	updated, err := h.db.Routers().Update(ctx, router)
	if err != nil {
		h.log.Error("api router update", "id", id, "error", err)
		if errors.Is(err, database.ErrDuplicateName) {
			h.writeJSONError(w, http.StatusConflict, "duplicate_name",
				fmt.Sprintf("a router named %q already exists", router.Name))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to update router")
		return
	}
	h.writeJSON(w, http.StatusOK, toJSONRouter(updated))
}

// apiRouterTest tests the API connectivity of a router and returns the device
// info together with the connection latency.
func (h *Handler) apiRouterTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api router test", "id", id, "error", err)
		return
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	info, err := client.DeviceInfo(ctx)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"device":    toJSONDeviceInfo(info),
		"connected": true,
	})
}

// apiRouterDevice returns the identity and resource snapshot of a router.
func (h *Handler) apiRouterDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api router device", "id", id, "error", err)
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	info, err := client.DeviceInfo(ctx)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	h.writeJSON(w, http.StatusOK, toJSONDeviceInfo(info))
}

// apiRouterClients returns the active hotspot clients on a router.
func (h *Handler) apiRouterClients(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api router clients", "id", id, "error", err)
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	clients, err := client.ActiveHotspotClients(ctx)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	out := make([]jsonClient, 0, len(clients))
	for _, c := range clients {
		out = append(out, toJSONClient(c))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"clients":  out,
		"total":    len(out),
		"router_id": id,
	})
}

// apiClientDisconnect ends a hotspot session by its RouterOS .id or by user
// name.
func (h *Handler) apiClientDisconnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	clientID := r.PathValue("clientId")
	if clientID == "" {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"client id is required")
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api client disconnect", "id", id, "error", err)
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	err = client.DisconnectClient(ctx, clientID, "")
	if err != nil && (errors.Is(err, ErrRouterNotFound) || errors.Is(err, ErrRouterUnknownHost)) {
		if _, userErr := client.Run(ctx, "/ip/hotspot/user/print", "?name="+clientID); userErr == nil {
			if dErr := client.DisconnectClient(ctx, "", clientID); dErr == nil {
				err = nil
			} else {
				err = dErr
			}
		}
	}
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{
		"status":    "disconnected",
		"client_id": clientID,
	})
}

// apiClientBlock blocks a client by MAC address using a hotspot IP binding.
func (h *Handler) apiClientBlock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	var req struct {
		MAC    string `json:"mac"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"request body must be valid JSON")
		return
	}
	if req.MAC == "" {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"mac is required")
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api client block", "id", id, "error", err)
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	bindingID, err := client.BlockMAC(ctx, req.MAC, req.Comment)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":     "blocked",
		"mac":        database.FormatMAC(req.MAC),
		"binding_id": bindingID,
	})
}

// apiClientUnblock removes a hotspot IP binding by id or by MAC address.
func (h *Handler) apiClientUnblock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	var req struct {
		BindingID string `json:"binding_id"`
		MAC       string `json:"mac"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"request body must be valid JSON")
		return
	}
	if req.BindingID == "" && req.MAC == "" {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"binding_id or mac is required")
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		h.log.Error("api client unblock", "id", id, "error", err)
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	if req.BindingID != "" {
		if err := client.UnblockBinding(ctx, req.BindingID); err != nil {
			h.writeJSONError(w, http.StatusBadGateway, "router_error",
				routerErrorHint(err))
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]string{
			"status":     "unblocked",
			"binding_id": req.BindingID,
		})
		return
	}
	bindingID, err := client.BlockMAC(ctx, req.MAC, "")
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	if err := client.UnblockBinding(ctx, bindingID); err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_error",
			routerErrorHint(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{
		"status": "unblocked",
		"mac":    database.FormatMAC(req.MAC),
	})
}

// jsonCommandReplyFromReply converts a RouterOS Reply into the public JSON
// shape.
func jsonCommandReplyFromReply(r Reply) jsonCommandReply {
	out := jsonCommandReply{
		ID:   r.ID(),
		Rows: r.Re,
		Done: r.Done,
	}
	if out.Rows == nil {
		out.Rows = []map[string]string{}
	}
	if out.Done == nil {
		out.Done = map[string]string{}
	}
	return out
}

// apiVouchersList returns vouchers filtered by router_id when the query string
// includes it.
func (h *Handler) apiVouchersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	var routerID int64
	if raw := query.Get("router_id"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			routerID = n
		}
	}
	vouchers, err := h.db.Vouchers().List(ctx, database.VoucherFilter{
		RouterID: routerID,
		Limit:    1000,
	})
	if err != nil {
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load vouchers")
		h.log.Error("api vouchers list", "error", err)
		return
	}
	out := make([]jsonVoucher, 0, len(vouchers))
	for _, v := range vouchers {
		out = append(out, toJSONVoucher(v))
	}
	h.writeJSON(w, http.StatusOK, jsonVoucherList{
		Vouchers: out,
		Total:    int64(len(out)),
	})
}

// apiVoucherCreate generates one or more vouchers for a router.
func (h *Handler) apiVoucherCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req jsonCreateVoucher
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"request body must be valid JSON")
		return
	}
	if msg := req.validate(); msg != "" {
		h.writeJSONError(w, http.StatusUnprocessableEntity, "validation_error", msg)
		return
	}
	spec := req.toSpec()
	vouchers, err := h.db.Vouchers().Generate(ctx, req.RouterID, spec)
	if err != nil {
		h.log.Error("api voucher create", "router_id", req.RouterID, "error", err)
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", req.RouterID))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to create vouchers")
		return
	}
	out := make([]jsonVoucher, 0, len(vouchers))
	for _, v := range vouchers {
		out = append(out, toJSONVoucher(v))
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{
		"vouchers": out,
		"count":    len(out),
	})
}

// apiVoucherGet returns a single voucher by its code (case insensitive).
func (h *Handler) apiVoucherGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := strings.TrimSpace(r.PathValue("code"))
	if code == "" {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"code is required")
		return
	}
	voucher, err := h.db.Vouchers().GetByCode(ctx, code)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("voucher %q does not exist", code))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load voucher")
		h.log.Error("api voucher get", "code", code, "error", err)
		return
	}
	h.writeJSON(w, http.StatusOK, toJSONVoucher(voucher))
}

// apiVoucherRedeem marks a voucher as used.
func (h *Handler) apiVoucherRedeem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := strings.TrimSpace(r.PathValue("code"))
	if code == "" {
		h.writeJSONError(w, http.StatusBadRequest, "bad_request",
			"code is required")
		return
	}
	voucher, err := h.db.Vouchers().GetByCode(ctx, code)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("voucher %q does not exist", code))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to load voucher")
		h.log.Error("api voucher redeem", "code", code, "error", err)
		return
	}
	redeemed, err := h.db.Vouchers().Redeem(ctx, voucher.ID)
	if err != nil {
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to redeem voucher")
		h.log.Error("api voucher redeem", "code", code, "error", err)
		return
	}
	if redeemed {
		h.writeJSON(w, http.StatusOK, map[string]any{
			"status":  "redeemed",
			"voucher": toJSONVoucher(voucher),
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "already_redeemed",
		"voucher": toJSONVoucher(voucher),
	})
}

// apiVoucherDelete removes a voucher by id.
func (h *Handler) apiVoucherDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.voucherIDPath(w, r)
	if !ok {
		return
	}
	if err := h.db.Vouchers().Delete(ctx, id); err != nil {
		h.log.Error("api voucher delete", "id", id, "error", err)
		if errors.Is(err, database.ErrNotFound) {
			h.writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("voucher %d does not exist", id))
			return
		}
		h.writeJSONError(w, http.StatusInternalServerError, "db_error",
			"failed to delete voucher")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiPortalLogin authenticates a hotspot client. It accepts the MikroTik
// redirect parameters either as JSON or as a standard form POST, so external
// systems and the captive portal share the same endpoint.
func (h *Handler) apiPortalLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Prefer a JSON body when present; fall back to form values so the captive
	// portal can also POST here.
	var req jsonPortalLoginRequest
	if r.Header.Get("Content-Type") != "" &&
		strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.writeJSONError(w, http.StatusBadRequest, "bad_request",
				"request body must be valid JSON")
			return
		}
	} else {
		req = jsonPortalLoginRequest{
			MAC:           r.FormValue("mac"),
			IP:            r.FormValue("ip"),
			Username:      r.FormValue("username"),
			Password:      r.FormValue("password"),
			LinkLogin:     r.FormValue("link_login"),
			LinkLoginOnly: r.FormValue("link_login_only"),
			LinkOrig:      r.FormValue("link_orig"),
			ServerName:    r.FormValue("server_name"),
			ChapID:        r.FormValue("chap_id"),
			ChapChallenge: r.FormValue("chap_challenge"),
		}
	}
	if msg := req.validate(); msg != "" {
		h.writeJSONError(w, http.StatusUnprocessableEntity, "validation_error", msg)
		return
	}

	router, err := h.findPortalRouter(ctx, req.ServerName)
	if err != nil {
		h.log.Error("api portal login", "server_name", req.ServerName, "error", err)
		h.writeJSONError(w, http.StatusBadGateway, "no_router",
			"no hotspot router is configured for this portal")
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeJSONError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return
	}
	defer client.Close()

	if err := client.HotspotLogin(ctx, req.Username, req.Password,
		req.MAC, req.IP); err != nil {
		h.log.Warn("api portal login failed", "username", req.Username,
			"mac", req.MAC, "ip", req.IP, "error", err)
		h.writeJSONError(w, http.StatusForbidden, "login_failed",
			routerErrorHint(err))
		return
	}
	redirect := req.LinkOrig
	if redirect == "" {
		redirect = h.cfg.DefaultRedirect
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"redirect":        redirect,
		"link_login":      req.LinkLogin,
		"link_login_only": req.LinkLoginOnly,
	})
}
