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

// routersPage backs the inventory list.
type routersPage struct {
	page
	Form    *routerForm
	Routers []routerSummary
	Stats   database.Stats
}

// routerPage backs the device manager of a single router.
type routerPage struct {
	page
	Router        database.Router
	Live          bool
	LiveError     string
	LiveHint      string
	Info          DeviceInfo
	Clients       []HotspotActive
	CachedClients []database.Session
	Bindings      []IPBinding
	Profiles      []HotspotProfile
	Warnings      []string
	Sync          database.SyncResult
	Vouchers      []database.Voucher
	VoucherStats  database.VoucherStats
	Form          *routerForm
	VoucherForm   *voucherForm
}

// routerForm carries the inventory form values, including the ones that failed
// validation so the operator does not lose their typing.
type routerForm struct {
	Name          string
	Host          string
	Port          string
	Username      string
	Password      string
	Location      string
	PortalTag     string
	Notes         string
	UseTLS        bool
	VerifyTLS     bool
	DefaultPortal bool
	// Transport selects the protocol: auto, api, api-ssl, rest or rest-ssl.
	Transport string
	// RestPort is the www/www-ssl port used by the REST transports. Empty means
	// 443 for HTTPS; plain HTTP requires an explicit value, and only a port the
	// operator actually typed (80 included) is ever dialled.
	RestPort string

	Errors map[string]string
}

// newRouterForm returns an empty form with sensible defaults.
func newRouterForm() *routerForm {
	return &routerForm{
		Port:      "8728",
		Transport: database.TransportAuto,
		Errors:    map[string]string{},
	}
}

// routerFormFromRequest reads the submitted inventory form.
func routerFormFromRequest(r *http.Request) *routerForm {
	form := newRouterForm()
	form.Name = strings.TrimSpace(r.PostFormValue("name"))
	form.Host = strings.TrimSpace(r.PostFormValue("host"))
	form.Port = strings.TrimSpace(defaultValue(r.PostFormValue("port"), "8728"))
	form.Username = strings.TrimSpace(r.PostFormValue("username"))
	form.Password = r.PostFormValue("password")
	form.Location = strings.TrimSpace(r.PostFormValue("location"))
	form.PortalTag = strings.TrimSpace(r.PostFormValue("portal_tag"))
	form.Notes = strings.TrimSpace(r.PostFormValue("notes"))
	form.UseTLS = r.PostFormValue("use_tls") != ""
	form.VerifyTLS = r.PostFormValue("verify_tls") != ""
	form.DefaultPortal = r.PostFormValue("default_portal") != ""
	form.Transport = database.NormalizeTransport(r.PostFormValue("transport"))
	form.RestPort = strings.TrimSpace(r.PostFormValue("rest_port"))
	return form
}

// restPort parses the optional www/www-ssl port. HTTPS uses 443 when this is
// empty; plain HTTP must have an explicit port and therefore returns 0 as a
// validation error when REST over HTTP is selected.
func (f *routerForm) restPort() int {
	if strings.TrimSpace(f.RestPort) == "" {
		return 0
	}
	port, err := strconv.Atoi(f.RestPort)
	if err != nil || port < 0 || port > 65535 {
		return -1
	}
	return port
}

func (f *routerForm) port() int {
	port, err := strconv.Atoi(f.Port)
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// validate checks the form and records per field messages.
func (f *routerForm) validate(requirePassword bool) bool {
	if f.Name == "" {
		f.Errors["name"] = "Give the router a name so it can be identified later."
	} else if len(f.Name) > 60 {
		f.Errors["name"] = "Keep the name under 60 characters."
	}
	if f.Host == "" {
		f.Errors["host"] = "Enter the router IP address or hostname reachable on the API port."
	}
	if f.port() == 0 {
		f.Errors["port"] = "Port must be a number between 1 and 65535 (8728 for API, 8729 for API-SSL). REST ignores this field - it is never an IP address."
	}
	if f.Username == "" {
		f.Errors["username"] = "Enter the RouterOS API user (for example \"aircoins\")."
	}
	if requirePassword && f.Password == "" {
		f.Errors["password"] = "Enter the API password so the controller can connect."
	}
	if len(f.PortalTag) > 40 {
		f.Errors["portal_tag"] = "Keep the portal tag under 40 characters."
	}
	if f.restPort() < 0 {
		f.Errors["rest_port"] = "Enter the web port for REST (for example 10775); port 80 is not used automatically."
	} else if f.Transport == database.TransportREST && f.restPort() == 0 {
		f.Errors["rest_port"] = "REST over HTTP needs the www port typed explicitly (for example 10775, or 80 if that is the router's www port); it is never chosen automatically."
	}
	return len(f.Errors) == 0
}

// router builds the domain object from the form.
func (f *routerForm) router() database.Router {
	return database.Router{
		Name:          f.Name,
		Host:          f.Host,
		Port:          f.port(),
		Username:      f.Username,
		Password:      f.Password,
		UseTLS:        f.UseTLS,
		VerifyTLS:     f.VerifyTLS,
		Location:      f.Location,
		PortalTag:     f.PortalTag,
		DefaultPortal: f.DefaultPortal,
		Notes:         f.Notes,
		Transport:     f.Transport,
		RestPort:      f.restPort(),
	}
}

// routerFormFromRouter fills the edit form of an existing router. The password
// stays blank on purpose: leaving it blank keeps the stored secret.
func routerFormFromRouter(router database.Router) *routerForm {
	form := newRouterForm()
	form.Name = router.Name
	form.Host = router.Host
	form.Port = strconv.Itoa(router.Port)
	form.Username = router.Username
	form.Location = router.Location
	form.PortalTag = router.PortalTag
	form.Notes = router.Notes
	form.UseTLS = router.UseTLS
	form.VerifyTLS = router.VerifyTLS
	form.DefaultPortal = router.DefaultPortal
	form.Transport = router.TransportMode()
	form.RestPort = ""
	if router.RestPort > 0 {
		form.RestPort = strconv.Itoa(router.RestPort)
	}
	return form
}

// RoutersList renders the multi-router inventory.
func (h *Handler) RoutersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
	stats, err := h.db.Dashboard(ctx)
	if err != nil {
		h.fail(w, r, "load statistics", err)
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

	h.render(w, r, http.StatusOK, "routers.html", &routersPage{
		page:    page{Title: "Routers", Nav: "routers"},
		Form:    newRouterForm(),
		Routers: summaries,
		Stats:   stats,
	})
}

// RouterCreate registers a new device and immediately tests the credentials so
// the operator knows whether the entry actually works.
func (h *Handler) RouterCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := routerFormFromRequest(r)

	if !form.validate(true) {
		h.renderRouterFormErrors(w, r, form)
		return
	}

	created, err := h.db.Routers().Create(ctx, form.router())
	if err != nil {
		form.Errors["name"] = err.Error()
		h.renderRouterFormErrors(w, r, form)
		return
	}
	if created.DefaultPortal {
		if err := h.db.Routers().ClearDefaultPortal(ctx, created.ID); err != nil {
			h.log.Error("cannot clear previous default portal", "error", err)
		}
	}

	// Verify the credentials right away; the outcome is a helpful flash rather
	// than a reason to reject a valid inventory entry.
	back := "/routers/" + strconv.FormatInt(created.ID, 10)
	client, dialErr := h.dialRouter(ctx, created)
	if dialErr != nil {
		h.flashAndRedirect(w, r, back, "warn",
			"Router saved, but the API connection failed: "+routerErrorHint(dialErr))
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()
	info, infoErr := client.DeviceInfo(callCtx)
	_ = client.Close()
	if infoErr != nil {
		h.flashAndRedirect(w, r, back, "warn",
			"Router saved and authenticated, but reading its identity failed: "+routerErrorHint(infoErr))
		return
	}
	h.flashAndRedirect(w, r, back, "ok",
		"Router saved and connected to "+defaultValue(info.Identity, created.Host)+
			" (RouterOS "+defaultValue(info.Version, "unknown")+")")
}

// RouterUpdate applies the edit form of a device.
func (h *Handler) RouterUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	existing, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
	}

	form := routerFormFromRequest(r)
	if !form.validate(false) {
		h.renderRouterFormErrors(w, r, form)
		return
	}
	updated := form.router()
	updated.ID = id
	if _, err := h.db.Routers().Update(ctx, updated); err != nil {
		form.Errors["name"] = err.Error()
		h.renderRouterFormErrors(w, r, form)
		return
	}
	if updated.DefaultPortal {
		if err := h.db.Routers().ClearDefaultPortal(ctx, id); err != nil {
			h.log.Error("cannot clear previous default portal", "error", err)
		}
	}

	message := "Router " + updated.Name + " updated"
	if form.Password == "" {
		message += " (stored API password kept)"
	} else {
		message += " (API password replaced)"
	}
	if existing.Endpoint() != updated.Endpoint() {
		message += " — endpoint changed to " + updated.Endpoint()
	}
	h.flashAndRedirect(w, r, "/routers/"+strconv.FormatInt(id, 10), "ok", message)
}

// RouterDelete removes a device from the inventory.
func (h *Handler) RouterDelete(w http.ResponseWriter, r *http.Request) {
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
	if err := h.db.Routers().Delete(ctx, id); err != nil {
		h.flashErr(w, r, "/routers/"+strconv.FormatInt(id, 10), "Delete router", err)
		return
	}
	h.flashAndRedirect(w, r, "/routers", "ok",
		"Router "+router.Name+" removed from the inventory. Its hotspot users were left untouched on the device.")
}

// RouterTest dials a device and reports what it found. It is the quickest way
// to prove that IP, port, user and password are right.
func (h *Handler) RouterTest(w http.ResponseWriter, r *http.Request) {
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

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashErr(w, r, back, "Connection test failed", err)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	info, err := client.DeviceInfo(callCtx)
	if err != nil {
		h.flashErr(w, r, back, "Connected, but reading the device identity failed", err)
		return
	}
	message := "Connected to " + defaultValue(info.Identity, router.Host) +
		" (RouterOS " + defaultValue(info.Version, "unknown") +
		", " + defaultValue(info.BoardName, "unknown board") + ")"

	if clients, err := client.ActiveHotspotClients(callCtx); err != nil {
		message += " — hotspot active list unavailable: " + routerErrorHint(err)
	} else {
		message += " — " + strconv.Itoa(len(clients)) + " hotspot client(s) online"
	}
	if profiles, err := client.HotspotProfiles(callCtx); err == nil {
		message += ", " + strconv.Itoa(len(profiles)) + " user profile(s)"
	}
	h.flashAndRedirect(w, r, back, "ok", message)
}

// RouterRefresh synchronises the local session table with the device and
// reports what changed.
func (h *Handler) RouterRefresh(w http.ResponseWriter, r *http.Request) {
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

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashErr(w, r, back, "Cannot refresh the session list", err)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	clients, err := client.ActiveHotspotClients(callCtx)
	if err != nil {
		h.flashErr(w, r, back, "Cannot read the active hotspot list", err)
		return
	}
	sync, err := h.db.Sessions().SyncDevice(ctx, router.ID, snapshotsFromClients(clients), time.Now())
	if err != nil {
		h.flashErr(w, r, back, "Cannot update the local session table", err)
		return
	}

	message := strconv.Itoa(sync.Tracked) + " client(s) online on " + router.Name +
		", " + strconv.Itoa(sync.Open) + " tracked locally"
	if sync.Closed > 0 {
		message += ", " + strconv.Itoa(sync.Closed) + " stale session(s) closed"
	}
	h.flashAndRedirect(w, r, back, "ok", message)
}

// renderRouterFormErrors re-renders the inventory page with the form errors.
func (h *Handler) renderRouterFormErrors(w http.ResponseWriter, r *http.Request, form *routerForm) {
	ctx := r.Context()

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
	stats, err := h.db.Dashboard(ctx)
	if err != nil {
		h.fail(w, r, "load statistics", err)
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

	h.render(w, r, http.StatusUnprocessableEntity, "routers.html", &routersPage{
		page:    page{Title: "Routers", Nav: "routers"},
		Form:    form,
		Routers: summaries,
		Stats:   stats,
	})
}

// notFoundRouter turns a missing row into a friendly 404.
func (h *Handler) notFoundRouter(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, database.ErrNotFound) {
		h.render(w, r, http.StatusNotFound, "error.html", &errorPage{
			page:    page{Title: "Router not found"},
			Message: "That router is not in the inventory any more.",
		})
		return
	}
	h.fail(w, r, "load router", err)
}

// RouterDetail renders the device manager: live clients, IP bindings, profiles
// and the vouchers bound to this router.
//
// When the API connection cannot be established the page still renders, showing
// the locally tracked sessions and an explanation, so an operator is never
// locked out of the controller by a broken router.
func (h *Handler) RouterDetail(w http.ResponseWriter, r *http.Request) {
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

	view := &routerPage{
		page:        page{Title: router.Name, Nav: "routers", Back: "/routers/" + strconv.FormatInt(id, 10)},
		Router:      router,
		Form:        routerFormFromRouter(router),
		VoucherForm: newVoucherForm(router.ID),
	}

	client, dialErr := h.dialRouter(ctx, router)
	if dialErr != nil {
		view.LiveError = dialErr.Error()
		view.LiveHint = routerErrorHint(dialErr)
		cached, cacheErr := h.db.Sessions().OpenForRouter(ctx, router.ID)
		if cacheErr != nil {
			h.log.Error("cannot load cached sessions", "router", router.Name, "error", cacheErr)
		}
		view.CachedClients = cached
	} else {
		callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
		snapshot := h.loadDevice(callCtx, client)
		cancel()
		_ = client.Close()

		view.Live = true
		view.Info = snapshot.Info
		view.Clients = snapshot.Clients
		view.Bindings = snapshot.Bindings
		view.Profiles = snapshot.Profiles
		view.Warnings = snapshot.Warnings

		if snapshot.ClientsErr == nil {
			sync, syncErr := h.db.Sessions().SyncDevice(ctx, router.ID, snapshotsFromClients(snapshot.Clients), time.Now())
			if syncErr != nil {
				h.log.Error("cannot sync sessions", "router", router.Name, "error", syncErr)
				view.Warnings = append(view.Warnings, "Local session table could not be updated: "+syncErr.Error())
			} else {
				view.Sync = sync
			}
		}
	}

	vouchers, err := h.db.Vouchers().List(ctx, database.VoucherFilter{RouterID: router.ID, Limit: 25})
	if err != nil {
		h.fail(w, r, "load vouchers", err)
		return
	}
	stats, err := h.db.Vouchers().Stats(ctx, router.ID)
	if err != nil {
		h.fail(w, r, "load voucher statistics", err)
		return
	}
	view.Vouchers = vouchers
	view.VoucherStats = stats

	h.render(w, r, http.StatusOK, "router.html", view)
}

// deviceSnapshot is a best effort read of one device: individual failures are
// collected as warnings so partial data still reaches the operator.
type deviceSnapshot struct {
	Info       DeviceInfo
	Clients    []HotspotActive
	ClientsErr error
	Bindings   []IPBinding
	Profiles   []HotspotProfile
	Warnings   []string
}

func (h *Handler) loadDevice(ctx context.Context, client *MikrotikClient) deviceSnapshot {
	var snapshot deviceSnapshot

	if info, err := client.DeviceInfo(ctx); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "System information unavailable: "+routerErrorHint(err))
	} else {
		snapshot.Info = info
	}

	if clients, err := client.ActiveHotspotClients(ctx); err != nil {
		snapshot.ClientsErr = err
		snapshot.Warnings = append(snapshot.Warnings, "Active hotspot clients unavailable: "+routerErrorHint(err))
	} else {
		snapshot.Clients = clients
	}

	if bindings, err := client.IPBindings(ctx); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "IP bindings unavailable: "+routerErrorHint(err))
	} else {
		snapshot.Bindings = bindings
	}

	if profiles, err := client.HotspotProfiles(ctx); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "Hotspot user profiles unavailable: "+routerErrorHint(err))
	} else {
		snapshot.Profiles = profiles
	}
	return snapshot
}

// snapshotsFromClients converts live API rows into the rows persisted by the
// session store, deriving a start time from the RouterOS uptime counter.
func snapshotsFromClients(clients []HotspotActive) []database.SessionSnapshot {
	at := time.Now()
	out := make([]database.SessionSnapshot, 0, len(clients))
	for _, client := range clients {
		var started time.Time
		if uptime, ok := parseUptime(client.Uptime); ok {
			started = at.Add(-uptime)
		}
		out = append(out, database.SessionSnapshot{
			Key:        client.ID,
			Username:   client.User,
			Address:    client.Address,
			MACAddress: client.MACAddress,
			LoginBy:    client.LoginBy,
			Server:     client.Server,
			Uptime:     client.Uptime,
			BytesIn:    client.BytesIn,
			BytesOut:   client.BytesOut,
			StartedAt:  started,
		})
	}
	return out
}

// uptimeUnits are the tokens RouterOS uses in uptime strings ("1w2d3h4m5s").
var uptimeUnits = map[rune]time.Duration{
	'w': 7 * 24 * time.Hour,
	'd': 24 * time.Hour,
	'h': time.Hour,
	'm': time.Minute,
	's': time.Second,
}

// parseUptime converts a RouterOS uptime string into a duration.
func parseUptime(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	var (
		total  time.Duration
		number int
		found  bool
	)
	for _, r := range value {
		if r >= '0' && r <= '9' {
			number = number*10 + int(r-'0')
			continue
		}
		unit, ok := uptimeUnits[r]
		if !ok {
			return 0, false
		}
		total += time.Duration(number) * unit
		number = 0
		found = true
	}
	if !found {
		return 0, false
	}
	return total, true
}
