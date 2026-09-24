// REST API layer for the Aircoins MikroTik controller. Endpoints live under
// /api/v1, speak JSON, and reuse the MikrotikClient from mikrotik.go so they
// inherit the reconnecting RouterOS client and the classified RouterError
// sentinels. Device queries use the same commands RouterOS v7 exposes.
//
// The API is intended for machine clients (integrations, monitoring, kiosk
// systems) and is therefore exempt from the browser CSRF guard; expose it
// behind a reverse proxy with auth when running on an untrusted network.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// apiError is the body of every non-2xx response.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeAPIJSON writes any value as a JSON response.
func (h *Handler) writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error("write json response", "status", status, "error", err)
	}
}

// writeAPIError writes a standardised JSON error body.
func (h *Handler) writeAPIError(w http.ResponseWriter, status int, code, message string) {
	h.writeAPIJSON(w, status, apiError{Code: code, Message: message})
}

// decodeAPIBody parses a JSON request body into dst.
func decodeAPIBody(r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return false
	}
	return true
}

// apiRouterLookupDB loads the router referenced by {id} without opening a
// device connection. When ok is false a response has already been written.
func (h *Handler) apiRouterLookupDB(w http.ResponseWriter, r *http.Request) (database.Router, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_id",
			"router id must be a positive integer")
		return database.Router{}, false
	}
	router, err := h.db.Routers().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return database.Router{}, false
		}
		h.log.Error("api load router", "id", id, "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		return database.Router{}, false
	}
	return router, true
}

// apiRouterLookup loads the router referenced by {id} and opens an API
// connection. When ok is false a response has already been written.
func (h *Handler) apiRouterLookup(w http.ResponseWriter, r *http.Request) (database.Router, *MikrotikClient, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_id",
			"router id must be a positive integer")
		return database.Router{}, nil, false
	}
	router, err := h.db.Routers().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return database.Router{}, nil, false
		}
		h.log.Error("api load router", "id", id, "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load router")
		return database.Router{}, nil, false
	}
	client, err := h.dialRouter(r.Context(), router)
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return router, nil, false
	}
	return router, client, true
}

// RoutesAPI wires every REST endpoint under /api/v1. It is called from
// Routes() so the API shares the middleware chain of the web UI.
func (h *Handler) RoutesAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/health", h.apiHealth)

	mux.HandleFunc("GET /api/v1/routers", h.apiRoutersList)
	mux.HandleFunc("POST /api/v1/routers", h.apiRouterCreate)
	mux.HandleFunc("GET /api/v1/routers/{id}", h.apiRouterGet)
	mux.HandleFunc("PUT /api/v1/routers/{id}", h.apiRouterUpdate)
	mux.HandleFunc("DELETE /api/v1/routers/{id}", h.apiRouterDelete)
	mux.HandleFunc("POST /api/v1/routers/{id}/test", h.apiRouterTest)
	mux.HandleFunc("GET /api/v1/routers/{id}/device", h.apiRouterDevice)

	mux.HandleFunc("GET /api/v1/routers/{id}/clients", h.apiRouterClients)
	mux.HandleFunc("GET /api/v1/routers/{id}/bindings", h.apiRouterBindings)
	mux.HandleFunc("GET /api/v1/routers/{id}/interfaces", h.apiRouterInterfaces)
	mux.HandleFunc("GET /api/v1/routers/{id}/interfaces/{iface}/traffic", h.apiRouterInterfaceTraffic)

	mux.HandleFunc("POST /api/v1/routers/{id}/clients/{cid}/disconnect", h.apiClientDisconnect)
	mux.HandleFunc("POST /api/v1/routers/{id}/block", h.apiClientBlock)
	mux.HandleFunc("POST /api/v1/routers/{id}/unblock", h.apiClientUnblock)
	mux.HandleFunc("POST /api/v1/routers/{id}/command", h.apiRouterCommand)

	mux.HandleFunc("GET /api/v1/vouchers", h.apiVouchersList)
	mux.HandleFunc("POST /api/v1/vouchers", h.apiVoucherCreate)
	mux.HandleFunc("GET /api/v1/vouchers/{code}", h.apiVoucherGet)
	mux.HandleFunc("POST /api/v1/vouchers/{code}/redeem", h.apiVoucherRedeem)
	mux.HandleFunc("DELETE /api/v1/vouchers/{id}", h.apiVoucherDelete)

	mux.HandleFunc("POST /api/v1/portal/login", h.apiPortalLogin)
}

// apiHealth is the machine readable liveness probe.
func (h *Handler) apiHealth(w http.ResponseWriter, r *http.Request) {
	if err := h.db.Ping(r.Context()); err != nil {
		h.writeAPIError(w, http.StatusServiceUnavailable, "unhealthy",
			"database unreachable")
		return
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// JSON shapes
// ---------------------------------------------------------------------------

type apiRouter struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Host          string     `json:"host"`
	Port          int        `json:"port"`
	Username      string     `json:"username"`
	UseTLS        bool       `json:"use_tls"`
	VerifyTLS     bool       `json:"verify_tls"`
	Location      string     `json:"location"`
	PortalTag     string     `json:"portal_tag"`
	DefaultPortal bool       `json:"default_portal"`
	Notes         string     `json:"notes"`
	Transport     string     `json:"transport"`
	RestPort      int        `json:"rest_port"`
	LastTransport string     `json:"last_transport"`
	LastStatus    string     `json:"last_status"`
	LastError     string     `json:"last_error"`
	LastLatencyMS int64      `json:"last_latency_ms"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func toAPIRouter(r database.Router) apiRouter {
	return apiRouter{
		ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port,
		Username: r.Username, UseTLS: r.UseTLS, VerifyTLS: r.VerifyTLS,
		Location: r.Location, PortalTag: r.PortalTag,
		DefaultPortal: r.DefaultPortal, Notes: r.Notes,
		Transport: r.TransportMode(), RestPort: r.RestPort,
		LastTransport: r.LastTransport,
		LastStatus:    r.LastStatus, LastError: r.LastError,
		LastLatencyMS: r.LastLatencyMS, LastSeenAt: r.LastSeenAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

type apiClient struct {
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
	TotalBytes int64  `json:"total_bytes"`
}

func toAPIClient(c HotspotActive) apiClient {
	return apiClient{
		ID: c.ID, User: c.User, Address: c.Address, MACAddress: c.MACAddress,
		Uptime: c.Uptime, LoginBy: c.LoginBy, Server: c.Server,
		Comment: c.Comment, BytesIn: c.BytesIn, BytesOut: c.BytesOut,
		TotalBytes: c.TotalBytes(),
	}
}

type apiDeviceInfo struct {
	Identity     string `json:"identity"`
	Version      string `json:"version"`
	BoardName    string `json:"board_name"`
	Architecture string `json:"architecture"`
	Uptime       string `json:"uptime"`
	CPULoad      int    `json:"cpu_load"`
	FreeMemory   int64  `json:"free_memory"`
	TotalMemory  int64  `json:"total_memory"`
}

func toAPIDeviceInfo(i DeviceInfo) apiDeviceInfo {
	return apiDeviceInfo{
		Identity: i.Identity, Version: i.Version, BoardName: i.BoardName,
		Architecture: i.Architecture, Uptime: i.Uptime, CPULoad: i.CPULoad,
		FreeMemory: i.FreeMemory, TotalMemory: i.TotalMemory,
	}
}

type apiBinding struct {
	ID         string `json:"id"`
	MACAddress string `json:"mac_address"`
	Address    string `json:"address"`
	ToAddress  string `json:"to_address"`
	Server     string `json:"server"`
	Type       string `json:"type"`
	Blocked    bool   `json:"blocked"`
	Comment    string `json:"comment"`
	Disabled   bool   `json:"disabled"`
}

func toAPIBinding(b IPBinding) apiBinding {
	return apiBinding{
		ID: b.ID, MACAddress: b.MACAddress, Address: b.Address,
		ToAddress: b.ToAddress, Server: b.Server, Type: b.Type,
		Blocked: b.Blocked(), Comment: b.Comment, Disabled: b.Disabled,
	}
}

type apiInterface struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	MTU        int64  `json:"mtu"`
	MACAddress string `json:"mac_address"`
	RxBytes    int64  `json:"rx_bytes"`
	TxBytes    int64  `json:"tx_bytes"`
	RxPackets  int64  `json:"rx_packets"`
	TxPackets  int64  `json:"tx_packets"`
}

func toAPIInterface(i InterfaceStats) apiInterface {
	return apiInterface{
		ID: i.ID, Name: i.Name, Type: i.Type, MTU: i.MTU,
		MACAddress: i.MACAddress,
		RxBytes:    i.RxBytes, TxBytes: i.TxBytes,
		RxPackets: i.RxPackets, TxPackets: i.TxPackets,
	}
}

type apiVoucher struct {
	ID              int64      `json:"id"`
	Code            string     `json:"code"`
	Batch           string     `json:"batch"`
	RouterID        *int64     `json:"router_id"`
	RouterName      string     `json:"router_name"`
	Profile         string     `json:"profile"`
	DurationMinutes int        `json:"duration_minutes"`
	DataLimitMB     int        `json:"data_limit_mb"`
	DeviceLimit     int        `json:"device_limit"`
	PriceCents      int64      `json:"price_cents"`
	Status          string     `json:"status"`
	StatusLabel     string     `json:"status_label"`
	Uses            int        `json:"uses"`
	MaxUses         int        `json:"max_uses"`
	RemainingUses   int        `json:"remaining_uses"`
	Note            string     `json:"note"`
	CreatedAt       time.Time  `json:"created_at"`
	PushedAt        *time.Time `json:"pushed_at"`
	ActivatedAt     *time.Time `json:"activated_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
}

func toAPIVoucher(v database.Voucher) apiVoucher {
	return apiVoucher{
		ID: v.ID, Code: v.Code, Batch: v.Batch, RouterID: v.RouterID,
		RouterName: v.RouterName, Profile: v.Profile,
		DurationMinutes: v.DurationMinutes, DataLimitMB: v.DataLimitMB,
		DeviceLimit: v.DeviceLimit, PriceCents: v.PriceCents,
		Status: string(v.Status), StatusLabel: v.Status.Label(),
		Uses: v.Uses, MaxUses: v.MaxUses, RemainingUses: v.RemainingUses(),
		Note: v.Note, CreatedAt: v.CreatedAt, PushedAt: v.PushedAt,
		ActivatedAt: v.ActivatedAt, ExpiresAt: v.ExpiresAt,
		LastUsedAt: v.LastUsedAt,
	}
}

// ---------------------------------------------------------------------------
// Router inventory
// ---------------------------------------------------------------------------

// apiRoutersList returns every registered router.
func (h *Handler) apiRoutersList(w http.ResponseWriter, r *http.Request) {
	routers, err := h.db.Routers().List(r.Context())
	if err != nil {
		h.log.Error("api routers list", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load routers")
		return
	}
	out := make([]apiRouter, 0, len(routers))
	for _, r := range routers {
		out = append(out, toAPIRouter(r))
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"routers": out,
		"total":   len(out),
	})
}

// apiRouterGet returns one router by id. It reads the inventory only and does
// not require the device to be reachable.
func (h *Handler) apiRouterGet(w http.ResponseWriter, r *http.Request) {
	router, ok := h.apiRouterLookupDB(w, r)
	if !ok {
		return
	}
	h.writeAPIJSON(w, http.StatusOK, toAPIRouter(router))
}

// apiRouterCreate registers a new router.
func (h *Handler) apiRouterCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
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
		Transport     string `json:"transport"`
		RestPort      int    `json:"rest_port"`
	}
	if !decodeAPIBody(r, &req) {
		h.writeAPIError(w, http.StatusBadRequest, "bad_request",
			"invalid JSON body")
		return
	}
	switch {
	case strings.TrimSpace(req.Name) == "":
		h.writeAPIError(w, http.StatusBadRequest, "validation", "name is required")
		return
	case strings.TrimSpace(req.Host) == "":
		h.writeAPIError(w, http.StatusBadRequest, "validation", "host is required")
		return
	case strings.TrimSpace(req.Username) == "":
		h.writeAPIError(w, http.StatusBadRequest, "validation", "username is required")
		return
	case database.NormalizeTransport(req.Transport) == database.TransportREST && req.RestPort <= 0:
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"rest_port is required for REST over HTTP; it is never chosen automatically")
		return
	}
	router := database.Router{
		Name:          strings.TrimSpace(req.Name),
		Host:          strings.TrimSpace(req.Host),
		Port:          req.Port,
		Username:      strings.TrimSpace(req.Username),
		Password:      req.Password,
		UseTLS:        req.UseTLS,
		VerifyTLS:     req.VerifyTLS,
		Location:      strings.TrimSpace(req.Location),
		PortalTag:     strings.TrimSpace(req.PortalTag),
		DefaultPortal: req.DefaultPortal,
		Notes:         strings.TrimSpace(req.Notes),
		Transport:     database.NormalizeTransport(req.Transport),
		RestPort:      req.RestPort,
	}
	created, err := h.db.Routers().Create(r.Context(), router)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			h.writeAPIError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		h.log.Error("api router create", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to create router")
		return
	}
	h.writeAPIJSON(w, http.StatusCreated, toAPIRouter(created))
}

// apiRouterUpdate applies a PUT. An omitted password keeps the stored secret.
func (h *Handler) apiRouterUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_id",
			"router id must be a positive integer")
		return
	}
	var req struct {
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
		Transport     string `json:"transport"`
		RestPort      int    `json:"rest_port"`
	}
	if !decodeAPIBody(r, &req) {
		h.writeAPIError(w, http.StatusBadRequest, "bad_request",
			"invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Host) == "" ||
		strings.TrimSpace(req.Username) == "" {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"name, host and username are required")
		return
	}
	if database.NormalizeTransport(req.Transport) == database.TransportREST && req.RestPort <= 0 {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"rest_port is required for REST over HTTP; it is never chosen automatically")
		return
	}
	updated, err := h.db.Routers().Update(r.Context(), database.Router{
		ID:            id,
		Name:          strings.TrimSpace(req.Name),
		Host:          strings.TrimSpace(req.Host),
		Port:          req.Port,
		Username:      strings.TrimSpace(req.Username),
		Password:      req.Password,
		UseTLS:        req.UseTLS,
		VerifyTLS:     req.VerifyTLS,
		Location:      strings.TrimSpace(req.Location),
		PortalTag:     strings.TrimSpace(req.PortalTag),
		DefaultPortal: req.DefaultPortal,
		Notes:         strings.TrimSpace(req.Notes),
		Transport:     database.NormalizeTransport(req.Transport),
		RestPort:      req.RestPort,
	})
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		if strings.Contains(err.Error(), "already exists") {
			h.writeAPIError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		h.log.Error("api router update", "id", id, "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to update router")
		return
	}
	h.writeAPIJSON(w, http.StatusOK, toAPIRouter(updated))
}

// apiRouterDelete removes a router and cascades its tracked sessions.
func (h *Handler) apiRouterDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_id",
			"router id must be a positive integer")
		return
	}
	if err := h.db.Routers().Delete(r.Context(), id); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("router %d does not exist", id))
			return
		}
		h.log.Error("api router delete", "id", id, "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to delete router")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiRouterTest probes connectivity and returns the device identity.
func (h *Handler) apiRouterTest(w http.ResponseWriter, r *http.Request) {
	router, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	info, err := client.DeviceInfo(r.Context())
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "device_error",
			routerErrorHint(err))
		return
	}
	identity := info.Identity
	if identity == "" {
		identity = router.Host
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"connected":  true,
		"identity":   identity,
		"version":    info.Version,
		"board":      info.BoardName,
		"latency_ms": router.LastLatencyMS,
	})
}

// apiRouterDevice returns identity and resource usage of one device.
func (h *Handler) apiRouterDevice(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	info, err := client.DeviceInfo(r.Context())
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "device_error",
			routerErrorHint(err))
		return
	}
	h.writeAPIJSON(w, http.StatusOK, toAPIDeviceInfo(info))
}

// apiRouterClients lists the clients currently online on the hotspot.
func (h *Handler) apiRouterClients(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	clients, err := client.ActiveHotspotClients(r.Context())
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "query_failed",
			routerErrorHint(err))
		return
	}
	out := make([]apiClient, 0, len(clients))
	for _, c := range clients {
		out = append(out, toAPIClient(c))
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"clients": out,
		"total":   len(out),
	})
}

// apiRouterBindings lists the hotspot IP bindings of one device.
func (h *Handler) apiRouterBindings(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	bindings, err := client.IPBindings(r.Context())
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "query_failed",
			routerErrorHint(err))
		return
	}
	out := make([]apiBinding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, toAPIBinding(b))
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"bindings": out,
		"total":    len(out),
	})
}

// apiRouterInterfaces lists the interfaces of one device with byte counters.
func (h *Handler) apiRouterInterfaces(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	interfaces, err := client.InterfaceList(r.Context())
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "query_failed",
			routerErrorHint(err))
		return
	}
	out := make([]apiInterface, 0, len(interfaces))
	for _, i := range interfaces {
		out = append(out, toAPIInterface(i))
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"interfaces": out,
		"total":      len(out),
	})
}

// apiTrafficPoint is one sample of the rolling window the traffic graph
// renders. The JSON names are the ones templates/dashboard.html reads.
type apiTrafficPoint struct {
	Timestamp time.Time `json:"timestamp"`
	RxBytes   int64     `json:"rx_bytes"`
	TxBytes   int64     `json:"tx_bytes"`
	RxRate    int64     `json:"rx_rate"`
	TxRate    int64     `json:"tx_rate"`
}

func toAPITrafficPoint(p InterfaceTraffic) apiTrafficPoint {
	return apiTrafficPoint{
		Timestamp: p.Timestamp, RxBytes: p.RxBytes, TxBytes: p.TxBytes,
		RxRate: p.RxRate, TxRate: p.TxRate,
	}
}

// trafficEntry is the rolling window of one router interface.
type trafficEntry struct {
	hist   InterfaceTrafficHistory
	seenAt time.Time
}

// trafficStore keeps the last 60 samples per router and interface so every
// poll returns history, not a single point: the browser replaces its whole
// series from this window on each refresh. The zero value is ready to use and
// windows untouched for 15 minutes are pruned, so a long lived controller
// process does not accumulate stale interfaces forever.
type trafficStore struct {
	mu      sync.Mutex
	entries map[string]*trafficEntry
}

// sample records one reading and returns a copy of the window for key. The
// copy lets the response be built without holding the lock.
func (s *trafficStore) sample(key string, now time.Time, stats InterfaceStats) []InterfaceTraffic {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]*trafficEntry)
	}
	cutoff := now.Add(-15 * time.Minute)
	for k, e := range s.entries {
		if e.seenAt.Before(cutoff) {
			delete(s.entries, k)
		}
	}
	e := s.entries[key]
	if e == nil {
		e = &trafficEntry{}
		s.entries[key] = e
	}
	e.seenAt = now
	e.hist.InterfaceID = stats.ID
	e.hist.InterfaceName = stats.Name
	e.hist.Points = append(e.hist.Points, InterfaceTraffic{
		Timestamp: now,
		RxBytes:   stats.RxBytes,
		TxBytes:   stats.TxBytes,
		RxRate:    stats.RxRate,
		TxRate:    stats.TxRate,
	})
	if len(e.hist.Points) > 60 {
		e.hist.Points = e.hist.Points[len(e.hist.Points)-60:]
	}
	points := make([]InterfaceTraffic, len(e.hist.Points))
	copy(points, e.hist.Points)
	return points
}

// apiRouterInterfaceTraffic samples one interface for the live traffic graph
// and appends the reading to a per router interface window. The whole window
// (up to 60 points; the dashboard polls every 2s) comes back as "points" so
// the graph redraws with history even right after a page load.
func (h *Handler) apiRouterInterfaceTraffic(w http.ResponseWriter, r *http.Request) {
	router, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	iface := r.PathValue("iface")
	stats, err := client.MonitorInterface(r.Context(), iface)
	if err != nil {
		if errors.Is(err, ErrRouterNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("interface %q not found on this device", iface))
			return
		}
		h.writeAPIError(w, http.StatusBadGateway, "query_failed",
			routerErrorHint(err))
		return
	}
	now := time.Now().UTC()
	key := strconv.FormatInt(router.ID, 10) + "/" + iface
	points := h.traffic.sample(key, now, stats)
	out := make([]apiTrafficPoint, 0, len(points))
	for _, p := range points {
		out = append(out, toAPITrafficPoint(p))
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"interface":      toAPIInterface(stats),
		"interface_name": stats.Name,
		"points":         out,
		"rx_rate":        stats.RxRate,
		"tx_rate":        stats.TxRate,
		"timestamp":      now,
	})
}

// apiClientDisconnect ends one hotspot session by client id or username.
func (h *Handler) apiClientDisconnect(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	var req struct {
		ClientID string `json:"client_id"`
		User     string `json:"user"`
	}
	_ = decodeAPIBody(r, &req)
	id := r.PathValue("cid")
	if id == "" {
		id = req.ClientID
	}
	if id == "" && req.User == "" {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"provide the client id in the path or a user in the body")
		return
	}
	if err := client.DisconnectClient(r.Context(), id, req.User); err != nil {
		if errors.Is(err, ErrRouterNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				"no such active client on this device")
			return
		}
		h.writeAPIError(w, http.StatusBadGateway, "disconnect_failed",
			routerErrorHint(err))
		return
	}
	h.db.Sessions().Close(r.Context(), 0, id, "api disconnect", time.Now())
	h.writeAPIJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

// apiClientBlock adds a blocked IP binding for a MAC address.
func (h *Handler) apiClientBlock(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	var req struct {
		MAC     string `json:"mac"`
		Comment string `json:"comment"`
	}
	if !decodeAPIBody(r, &req) || strings.TrimSpace(req.MAC) == "" {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"mac is required")
		return
	}
	bindingID, err := client.BlockMAC(r.Context(), req.MAC, req.Comment)
	if err != nil {
		if errors.Is(err, ErrRouterConflict) {
			h.writeAPIError(w, http.StatusConflict, "conflict",
				"this MAC already has an IP binding on the device")
			return
		}
		h.writeAPIError(w, http.StatusBadGateway, "block_failed",
			routerErrorHint(err))
		return
	}
	h.db.Sessions().CloseByMAC(r.Context(), 0,
		database.NormalizeMAC(req.MAC), "blocked via api", time.Now())
	h.writeAPIJSON(w, http.StatusCreated, map[string]any{
		"status":     "blocked",
		"mac":        database.FormatMAC(req.MAC),
		"binding_id": bindingID,
	})
}

// apiClientUnblock removes an IP binding, matched by id or MAC.
func (h *Handler) apiClientUnblock(w http.ResponseWriter, r *http.Request) {
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	var req struct {
		BindingID string `json:"binding_id"`
		MAC       string `json:"mac"`
	}
	if !decodeAPIBody(r, &req) {
		req.BindingID = strings.TrimSpace(r.PostFormValue("binding_id"))
		req.MAC = strings.TrimSpace(r.PostFormValue("mac"))
	}
	bindingID := req.BindingID
	if bindingID == "" && req.MAC != "" {
		bindings, err := client.IPBindings(r.Context())
		if err != nil {
			h.writeAPIError(w, http.StatusBadGateway, "query_failed",
				routerErrorHint(err))
			return
		}
		want := database.FormatMAC(req.MAC)
		for _, b := range bindings {
			if strings.EqualFold(b.MACAddress, want) {
				bindingID = b.ID
				break
			}
		}
		if bindingID == "" {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("no IP binding for %s on this device", want))
			return
		}
	}
	if bindingID == "" {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"provide binding_id or mac")
		return
	}
	if err := client.UnblockBinding(r.Context(), bindingID); err != nil {
		if errors.Is(err, ErrRouterNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				"binding no longer exists on the device")
			return
		}
		h.writeAPIError(w, http.StatusBadGateway, "unblock_failed",
			routerErrorHint(err))
		return
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]string{"status": "unblocked"})
}

// apiRouterCommand runs an arbitrary RouterOS command. This is the escape
// hatch for RouterOS v7 features the controller does not model explicitly.
func (h *Handler) apiRouterCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if !decodeAPIBody(r, &req) || strings.TrimSpace(req.Command) == "" {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"command is required, e.g. \"/interface/print\"")
		return
	}
	command := strings.TrimSpace(req.Command)
	if !strings.HasPrefix(command, "/") {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"command must start with /, e.g. \"/interface/print\"")
		return
	}
	_, client, ok := h.apiRouterLookup(w, r)
	if !ok {
		return
	}
	defer client.Close()
	reply, err := client.Run(r.Context(), command, req.Args...)
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "command_failed",
			routerErrorHint(err))
		return
	}
	rows := reply.Re
	if rows == nil {
		rows = []map[string]string{}
	}
	done := reply.Done
	if done == nil {
		done = map[string]string{}
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"command": command,
		"rows":    rows,
		"done":    done,
	})
}

// ---------------------------------------------------------------------------
// Vouchers
// ---------------------------------------------------------------------------

// apiVouchersList returns vouchers, optionally filtered.
func (h *Handler) apiVouchersList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := database.VoucherFilter{Query: strings.TrimSpace(q.Get("q"))}
	if raw := strings.TrimSpace(q.Get("router_id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.RouterID = id
		}
	}
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		status := database.VoucherStatus(strings.ToLower(raw))
		if status.Valid() {
			filter.Status = status
		}
	}
	filter.Batch = strings.TrimSpace(q.Get("batch"))
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Limit = n
		}
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Offset = n
		}
	}
	vouchers, err := h.db.Vouchers().List(r.Context(), filter)
	if err != nil {
		h.log.Error("api vouchers list", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load vouchers")
		return
	}
	out := make([]apiVoucher, 0, len(vouchers))
	for _, v := range vouchers {
		out = append(out, toAPIVoucher(v))
	}
	total, err := h.db.Vouchers().Count(r.Context(), filter)
	if err != nil {
		total = int64(len(out))
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"vouchers": out,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
	})
}

// apiVoucherCreate generates a batch of voucher keys.
func (h *Handler) apiVoucherCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RouterID        int64  `json:"router_id"`
		Count           int    `json:"count"`
		Batch           string `json:"batch"`
		Profile         string `json:"profile"`
		DurationMinutes int    `json:"duration_minutes"`
		DataLimitMB     int    `json:"data_limit_mb"`
		DeviceLimit     int    `json:"device_limit"`
		PriceCents      int64  `json:"price_cents"`
		MaxUses         int    `json:"max_uses"`
		Note            string `json:"note"`
		Prefix          string `json:"prefix"`
	}
	if !decodeAPIBody(r, &req) {
		h.writeAPIError(w, http.StatusBadRequest, "bad_request",
			"invalid JSON body")
		return
	}
	if req.Count <= 0 {
		req.Count = 10
	}
	if req.Count > maxVoucherBatch {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			fmt.Sprintf("count must be at most %d", maxVoucherBatch))
		return
	}
	if req.RouterID > 0 {
		if _, err := h.db.Routers().Get(r.Context(), req.RouterID); err != nil {
			h.writeAPIError(w, http.StatusBadRequest, "validation",
				fmt.Sprintf("router %d does not exist", req.RouterID))
			return
		}
	}
	if req.DeviceLimit <= 0 {
		req.DeviceLimit = 1
	}
	if req.MaxUses <= 0 {
		req.MaxUses = 1
	}
	batch := strings.TrimSpace(req.Batch)
	if batch == "" {
		batch = database.VoucherBatchLabel(time.Now())
	}

	opts := database.DefaultVoucherCodeOptions()
	opts.Prefix = req.Prefix
	vouchers := make([]database.Voucher, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		var routerID *int64
		if req.RouterID > 0 {
			id := req.RouterID
			routerID = &id
		}
		code, err := database.GenerateVoucherCode(opts)
		if err != nil {
			h.log.Error("api voucher code", "error", err)
			h.writeAPIError(w, http.StatusInternalServerError, "code_error",
				"could not generate a voucher code")
			return
		}
		vouchers = append(vouchers, database.Voucher{
			Code:            code,
			Batch:           batch,
			RouterID:        routerID,
			Profile:         strings.TrimSpace(req.Profile),
			DurationMinutes: req.DurationMinutes,
			DataLimitMB:     req.DataLimitMB,
			DeviceLimit:     req.DeviceLimit,
			PriceCents:      req.PriceCents,
			Status:          database.VoucherUnused,
			MaxUses:         req.MaxUses,
			Note:            strings.TrimSpace(req.Note),
		})
	}
	inserted, err := h.db.Vouchers().CreateBatch(r.Context(), vouchers)
	if err != nil {
		if errors.Is(err, database.ErrDuplicateVoucherCode) {
			h.writeAPIError(w, http.StatusConflict, "conflict",
				"a generated code already exists, retry the request")
			return
		}
		h.log.Error("api voucher create", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to create vouchers")
		return
	}
	stored, err := h.db.Vouchers().List(r.Context(), database.VoucherFilter{
		Batch: batch, Limit: req.Count,
	})
	if err != nil {
		h.writeAPIJSON(w, http.StatusCreated, map[string]any{
			"count": inserted, "batch": batch,
		})
		return
	}
	out := make([]apiVoucher, 0, len(stored))
	for _, v := range stored {
		out = append(out, toAPIVoucher(v))
	}
	h.writeAPIJSON(w, http.StatusCreated, map[string]any{
		"count":    inserted,
		"batch":    batch,
		"vouchers": out,
	})
}

// apiVoucherGet looks one voucher up by code.
func (h *Handler) apiVoucherGet(w http.ResponseWriter, r *http.Request) {
	voucher, err := h.db.Vouchers().FindByCode(r.Context(), r.PathValue("code"))
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				"no voucher with that code")
			return
		}
		h.log.Error("api voucher get", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load voucher")
		return
	}
	h.writeAPIJSON(w, http.StatusOK, toAPIVoucher(voucher))
}

// apiVoucherDelete removes one voucher from the ledger.
func (h *Handler) apiVoucherDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_id",
			"voucher id must be a positive integer")
		return
	}
	if err := h.db.Vouchers().Delete(r.Context(), id); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				"no voucher with that id")
			return
		}
		h.log.Error("api voucher delete", "id", id, "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to delete voucher")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiResolveVoucherRouter picks the device a voucher login should hit:
// the hotspot server-name tag first, then the voucher's bound router, then
// the default portal device.
func (h *Handler) apiResolveVoucherRouter(w http.ResponseWriter, r *http.Request, voucher database.Voucher, serverName string) (database.Router, *MikrotikClient, bool) {
	ctx := r.Context()
	if tag := strings.TrimSpace(serverName); tag != "" {
		if routers, err := h.db.Routers().FindByPortalTag(ctx, tag); err == nil && len(routers) > 0 {
			client, err := h.dialRouter(ctx, routers[0])
			if err != nil {
				h.writeAPIError(w, http.StatusBadGateway, "router_unreachable",
					routerErrorHint(err))
				return routers[0], nil, false
			}
			return routers[0], client, true
		}
	}
	if voucher.RouterID != nil {
		router, err := h.db.Routers().Get(ctx, *voucher.RouterID)
		if err != nil {
			h.writeAPIError(w, http.StatusBadGateway, "router_missing",
				"the voucher points to a router that no longer exists")
			return database.Router{}, nil, false
		}
		client, err := h.dialRouter(ctx, router)
		if err != nil {
			h.writeAPIError(w, http.StatusBadGateway, "router_unreachable",
				routerErrorHint(err))
			return router, nil, false
		}
		return router, client, true
	}
	router, err := h.db.Routers().Default(ctx)
	if err != nil {
		h.writeAPIError(w, http.StatusUnprocessableEntity, "no_router",
			"the voucher is not bound to a hotspot and no default device is set")
		return database.Router{}, nil, false
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.writeAPIError(w, http.StatusBadGateway, "router_unreachable",
			routerErrorHint(err))
		return router, nil, false
	}
	return router, client, true
}

// apiVoucherRedeem provisions a key on its device, logs the client in and
// consumes one redemption. A device failure never burns the voucher.
func (h *Handler) apiVoucherRedeem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAC        string `json:"mac"`
		IP         string `json:"ip"`
		ServerName string `json:"server_name"`
	}
	_ = decodeAPIBody(r, &req)
	voucher, err := h.db.Vouchers().FindByCode(r.Context(), r.PathValue("code"))
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusNotFound, "not_found",
				"no voucher with that code")
			return
		}
		h.log.Error("api voucher redeem", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load voucher")
		return
	}
	_, client, ok := h.apiResolveVoucherRouter(w, r, voucher, req.ServerName)
	if !ok {
		return
	}
	defer client.Close()
	result, err := h.redeemVoucher(r.Context(), client, voucher, req.MAC, req.IP)
	if err != nil {
		if errors.Is(err, database.ErrVoucherNotRedeemable) {
			h.writeAPIError(w, http.StatusConflict, "not_redeemable", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not assigned to a hotspot") {
			h.writeAPIError(w, http.StatusUnprocessableEntity, "no_router",
				err.Error())
			return
		}
		h.writeAPIError(w, http.StatusBadGateway, "redeem_failed", err.Error())
		return
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"status":        "redeemed",
		"login_via_api": result.LoginViaAPI,
		"created_user":  result.CreatedUser,
		"note":          result.Note,
		"voucher":       toAPIVoucher(result.Voucher),
	})
}

// apiPortalLogin is the machine endpoint of the captive portal. It accepts the
// MikroTik redirect parameters plus the voucher code and returns where the
// client should be sent next.
func (h *Handler) apiPortalLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code       string `json:"code"`
		MAC        string `json:"mac"`
		IP         string `json:"ip"`
		LinkLogin  string `json:"link_login"`
		LinkOrig   string `json:"link_orig"`
		ServerName string `json:"server_name"`
	}
	if !decodeAPIBody(r, &req) {
		h.writeAPIError(w, http.StatusBadRequest, "bad_request",
			"invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		h.writeAPIError(w, http.StatusBadRequest, "validation",
			"code is required")
		return
	}
	voucher, err := h.db.Vouchers().FindByCode(r.Context(), req.Code)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.writeAPIError(w, http.StatusUnauthorized, "invalid_code",
				"unknown voucher code")
			return
		}
		h.log.Error("api portal login", "error", err)
		h.writeAPIError(w, http.StatusInternalServerError, "db_error",
			"failed to load voucher")
		return
	}
	_, client, ok := h.apiResolveVoucherRouter(w, r, voucher, req.ServerName)
	if !ok {
		return
	}
	defer client.Close()
	result, err := h.redeemVoucher(r.Context(), client, voucher, req.MAC, req.IP)
	if err != nil {
		if errors.Is(err, database.ErrVoucherNotRedeemable) {
			h.writeAPIError(w, http.StatusConflict, "not_redeemable", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not assigned to a hotspot") {
			h.writeAPIError(w, http.StatusUnprocessableEntity, "no_router",
				err.Error())
			return
		}
		h.writeAPIError(w, http.StatusBadGateway, "redeem_failed", err.Error())
		return
	}
	redirect := strings.TrimSpace(req.LinkOrig)
	if redirect == "" || !isSafeRedirect(redirect) {
		redirect = h.cfg.DefaultRedirect
	}
	h.writeAPIJSON(w, http.StatusOK, map[string]any{
		"status":        "authenticated",
		"login_via_api": result.LoginViaAPI,
		"note":          result.Note,
		"voucher":       toAPIVoucher(result.Voucher),
		"redirect":      redirect,
	})
}

// isSafeRedirect only follows http(s) targets back to the client.
func isSafeRedirect(target string) bool {
	return strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
}
