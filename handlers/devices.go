package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// deviceRow is one line of the DEVICES page: a single client, merged from three
// sources that each know part of the story.
//
//   - the operator's saved record (name, notes) from the local devices table;
//   - the router's DHCP lease (hostname, current address) matched by MAC;
//   - the router's hotspot active list (remaining session time) matched by MAC.
//
// The live halves are re-read on every page load and are never persisted, so a
// device that has moved to a new address is never shown with a stale one.
type deviceRow struct {
	RouterID   int64
	RouterName string

	// Hostname is the DHCP host-name, falling back to a hotspot comment. It is
	// empty when the router never saw a name for this client.
	Hostname string
	// Address is the client's current IP (live binding preferred).
	Address string
	// MAC is normalised; MACAddress() renders it for display.
	MAC  string
	User string
	// SessionTimeLeft is the RouterOS "session-time-left" of a live hotspot
	// session, empty when the device has no timed session running.
	SessionTimeLeft string
	Uptime          string

	// Connected reports a live hotspot session was matched for this device.
	Connected bool
	// Leased reports a DHCP lease was matched, and Online that the lease is
	// currently bound. A saved device that is powered off has neither.
	Leased bool
	Online bool

	// ID is the stored record id, 0 when the operator has not saved the device.
	ID    int64
	Name  string
	Notes string
}

// MACAddress renders the normalised MAC in canonical colon-separated form.
func (d deviceRow) MACAddress() string {
	if d.MAC == "" {
		return ""
	}
	return database.FormatMAC(d.MAC)
}

// Saved reports whether the operator has stored this device locally.
func (d deviceRow) Saved() bool { return d.ID > 0 }

// DisplayName is what the table shows: the operator's saved name wins, then the
// DHCP hostname, then the hotspot user, then the bare MAC.
func (d deviceRow) DisplayName() string {
	switch {
	case strings.TrimSpace(d.Name) != "":
		return d.Name
	case strings.TrimSpace(d.Hostname) != "":
		return d.Hostname
	case strings.TrimSpace(d.User) != "":
		return d.User
	case d.MAC != "":
		return database.FormatMAC(d.MAC)
	default:
		return noValue
	}
}

// devicesPage backs the DEVICES view.
type devicesPage struct {
	page
	Routers  []database.Router
	Devices  []deviceRow
	Filter   deviceFilter
	Form     *deviceForm
	Warnings []string
	// Total is the number of rows shown, Connected how many hold a live
	// session, and Saved how many the operator has stored.
	Total     int
	Connected int
	Saved     int
}

// deviceFilter mirrors the query string of the DEVICES page.
type deviceFilter struct {
	RouterID int64
	Query    string
}

// deviceForm carries the add/edit form values, including the ones that failed
// validation so the operator does not lose their typing.
type deviceForm struct {
	RouterID int64
	MAC      string
	Name     string
	Notes    string
	Errors   map[string]string
}

func newDeviceForm(routerID int64) *deviceForm {
	return &deviceForm{RouterID: routerID, Errors: map[string]string{}}
}

func deviceFormFromRequest(r *http.Request) *deviceForm {
	form := newDeviceForm(0)
	if raw := strings.TrimSpace(r.PostFormValue("router_id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			form.RouterID = id
		}
	}
	form.MAC = strings.TrimSpace(r.PostFormValue("mac"))
	form.Name = strings.TrimSpace(r.PostFormValue("name"))
	form.Notes = strings.TrimSpace(r.PostFormValue("notes"))
	return form
}

// DevicesList renders the fleet-wide device manager.
func (h *Handler) DevicesList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := deviceFilter{Query: strings.TrimSpace(query.Get("q"))}
	if raw := strings.TrimSpace(query.Get("router")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.RouterID = id
		}
	}

	view, err := h.buildDevicesPage(r.Context(), filter)
	if err != nil {
		h.fail(w, r, "load devices", err)
		return
	}
	h.render(w, r, http.StatusOK, "devices.html", view)
}

// buildDevicesPage assembles the whole view: the router inventory, the merged
// live + saved device rows and the counts. It dials every in-scope router, so
// it is shared by the GET handler and the form-error re-render.
func (h *Handler) buildDevicesPage(ctx context.Context, filter deviceFilter) (*devicesPage, error) {
	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := h.db.Devices().List(ctx)
	if err != nil {
		return nil, err
	}

	// Saved devices grouped by router, so a record always renders even when its
	// router is unreachable or the client is offline.
	savedByRouter := make(map[int64][]database.Device, len(routers))
	for _, device := range stored {
		savedByRouter[device.RouterID] = append(savedByRouter[device.RouterID], device)
	}

	targets := routers
	if filter.RouterID > 0 {
		targets = nil
		for _, router := range routers {
			if router.ID == filter.RouterID {
				targets = append(targets, router)
			}
		}
	}

	var rows []deviceRow
	var warnings []string
	for _, router := range targets {
		rows = append(rows, h.collectRouterDevices(ctx, router, savedByRouter[router.ID], &warnings)...)
	}

	rows = filterDeviceRows(rows, filter.Query)
	sortDeviceRows(rows)

	view := &devicesPage{
		page:     page{Title: "Devices", Nav: "devices"},
		Routers:  routers,
		Devices:  rows,
		Filter:   filter,
		Form:     newDeviceForm(filter.RouterID),
		Warnings: warnings,
		Total:    len(rows),
	}
	for _, row := range rows {
		if row.Connected {
			view.Connected++
		}
		if row.Saved() {
			view.Saved++
		}
	}
	return view, nil
}

// collectRouterDevices merges one router's saved records with its live DHCP
// leases and hotspot sessions. An unreachable router is not fatal: the saved
// rows still render and the dial failure is reported as a warning, matching the
// controller's rule that one broken device must not blank a whole page.
func (h *Handler) collectRouterDevices(ctx context.Context, router database.Router, saved []database.Device, warnings *[]string) []deviceRow {
	index := make(map[string]*deviceRow, len(saved))
	get := func(key string) *deviceRow {
		if row, ok := index[key]; ok {
			return row
		}
		row := &deviceRow{RouterID: router.ID, RouterName: router.Name}
		index[key] = row
		return row
	}

	// Seed with saved records so they survive an offline client or router.
	for _, device := range saved {
		if key := macKey(device.MAC); key != "" {
			row := get(key)
			row.ID = device.ID
			row.Name = device.Name
			row.Notes = device.Notes
			row.MAC = database.NormalizeMAC(device.MAC)
		}
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		*warnings = append(*warnings, router.Name+": "+routerErrorHint(err))
		return materialiseRows(index)
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	// DHCP leases supply the hostname and current address the hotspot list omits.
	if leases, leaseErr := client.DHCPLeases(callCtx); leaseErr != nil {
		*warnings = append(*warnings, router.Name+": DHCP leases unavailable ("+routerErrorHint(leaseErr)+")")
	} else {
		for _, lease := range leases {
			mac := lease.EffectiveMAC()
			key := macKey(mac)
			if key == "" {
				// A lease with no MAC cannot be matched to a saved device; key
				// it on its address so it still appears in the list.
				if addr := lease.EffectiveAddress(); addr != "" {
					key = "ip:" + addr
				} else {
					continue
				}
			}
			row := get(key)
			row.MAC = mac
			row.Leased = true
			row.Online = lease.Bound || strings.EqualFold(lease.Status, "bound")
			if row.Hostname == "" {
				row.Hostname = strings.TrimSpace(lease.HostName)
			}
			if row.Address == "" {
				row.Address = lease.EffectiveAddress()
			}
		}
	}

	// Hotspot active sessions supply the remaining session time.
	if actives, activeErr := client.ActiveHotspotClients(callCtx); activeErr != nil {
		*warnings = append(*warnings, router.Name+": hotspot active list unavailable ("+routerErrorHint(activeErr)+")")
	} else {
		for _, active := range actives {
			mac := database.NormalizeMAC(active.MACAddress)
			key := macKey(mac)
			if key == "" {
				switch {
				case active.Address != "":
					key = "ip:" + active.Address
				case active.ID != "":
					key = "id:" + active.ID
				default:
					continue
				}
			}
			row := get(key)
			row.MAC = mac
			row.Connected = true
			row.User = active.User
			row.SessionTimeLeft = strings.TrimSpace(active.SessionTimeLeft)
			row.Uptime = active.Uptime
			if row.Address == "" {
				row.Address = active.Address
			}
			if row.Hostname == "" {
				row.Hostname = strings.TrimSpace(active.Comment)
			}
		}
	}

	return materialiseRows(index)
}

// macKey builds the merge identity for a MAC address, or "" when there is none.
func macKey(mac string) string {
	if normalized := database.NormalizeMAC(mac); normalized != "" {
		return "mac:" + normalized
	}
	return ""
}

func materialiseRows(index map[string]*deviceRow) []deviceRow {
	out := make([]deviceRow, 0, len(index))
	for _, row := range index {
		out = append(out, *row)
	}
	return out
}

// filterDeviceRows keeps only the rows matching a free-text query across the
// fields an operator is likely to search by.
func filterDeviceRows(rows []deviceRow, query string) []deviceRow {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return rows
	}
	out := make([]deviceRow, 0, len(rows))
	for _, row := range rows {
		haystack := strings.ToLower(strings.Join([]string{
			row.DisplayName(), row.Address, row.MACAddress(), row.User, row.RouterName, row.Notes,
		}, " "))
		if strings.Contains(haystack, query) {
			out = append(out, row)
		}
	}
	return out
}

// sortDeviceRows puts live sessions first, then groups by router and name so
// the page reads the same way on every load.
func sortDeviceRows(rows []deviceRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Connected != b.Connected {
			return a.Connected
		}
		if !strings.EqualFold(a.RouterName, b.RouterName) {
			return strings.ToLower(a.RouterName) < strings.ToLower(b.RouterName)
		}
		return strings.ToLower(a.DisplayName()) < strings.ToLower(b.DisplayName())
	})
}

// DeviceCreate saves a device to the local inventory.
func (h *Handler) DeviceCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := deviceFormFromRequest(r)
	back := devicesBackURL(form.RouterID)

	if !h.validateDeviceForm(ctx, form) {
		h.renderDeviceFormErrors(w, r, form)
		return
	}
	created, err := h.db.Devices().Create(ctx, database.Device{
		RouterID: form.RouterID,
		MAC:      form.MAC,
		Name:     form.Name,
		Notes:    form.Notes,
	})
	if err != nil {
		h.flashAndRedirect(w, r, back, "err", err.Error())
		return
	}
	h.flashAndRedirect(w, r, back, "ok", "Saved "+deviceLabel(created)+" to the inventory.")
}

// DeviceUpdate edits a saved device's name, notes and identity.
func (h *Handler) DeviceUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.deviceIDPath(w, r)
	if !ok {
		return
	}
	existing, err := h.db.Devices().Get(ctx, id)
	if err != nil {
		h.deviceNotFound(w, r, err)
		return
	}

	form := deviceFormFromRequest(r)
	// The inline edit form may leave the router and MAC untouched; fall back to
	// the stored values so a rename never orphans the record.
	if form.RouterID <= 0 {
		form.RouterID = existing.RouterID
	}
	if form.MAC == "" {
		form.MAC = existing.MAC
	}
	back := devicesBackURL(form.RouterID)

	if !h.validateDeviceForm(ctx, form) {
		h.renderDeviceFormErrors(w, r, form)
		return
	}
	updated, err := h.db.Devices().Update(ctx, database.Device{
		ID:       id,
		RouterID: form.RouterID,
		MAC:      form.MAC,
		Name:     form.Name,
		Notes:    form.Notes,
	})
	if err != nil {
		h.flashAndRedirect(w, r, back, "err", err.Error())
		return
	}
	h.flashAndRedirect(w, r, back, "ok", "Updated "+deviceLabel(updated)+".")
}

// DeviceDelete removes a saved device. It touches only the controller's
// inventory; the client is left alone on the router.
func (h *Handler) DeviceDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.deviceIDPath(w, r)
	if !ok {
		return
	}
	existing, err := h.db.Devices().Get(ctx, id)
	if err != nil {
		h.deviceNotFound(w, r, err)
		return
	}
	back := devicesBackURL(existing.RouterID)
	if err := h.db.Devices().Delete(ctx, id); err != nil {
		h.flashErr(w, r, back, "Delete device", err)
		return
	}
	h.flashAndRedirect(w, r, back, "ok",
		"Removed "+deviceLabel(existing)+" from the inventory. The device itself was left untouched on the router.")
}

// validateDeviceForm checks the add/edit form and records per-field messages.
func (h *Handler) validateDeviceForm(ctx context.Context, form *deviceForm) bool {
	if form.RouterID <= 0 {
		form.Errors["router_id"] = "Choose the router this device belongs to."
	} else if _, err := h.db.Routers().Get(ctx, form.RouterID); err != nil {
		form.Errors["router_id"] = "That router is not in the inventory."
	}
	if database.NormalizeMAC(form.MAC) == "" {
		form.Errors["mac"] = "Enter a MAC address (for example AA:BB:CC:DD:EE:FF)."
	}
	return len(form.Errors) == 0
}

// renderDeviceFormErrors re-renders the DEVICES page with the failed form so
// the operator keeps their typing and sees what to fix.
func (h *Handler) renderDeviceFormErrors(w http.ResponseWriter, r *http.Request, form *deviceForm) {
	view, err := h.buildDevicesPage(r.Context(), deviceFilter{RouterID: form.RouterID})
	if err != nil {
		h.fail(w, r, "load devices", err)
		return
	}
	view.Form = form
	h.render(w, r, http.StatusUnprocessableEntity, "devices.html", view)
}

// deviceIDPath reads the {id} path value of a device route.
func (h *Handler) deviceIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid device id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// deviceNotFound turns a missing record into a friendly 404.
func (h *Handler) deviceNotFound(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, database.ErrNotFound) {
		h.render(w, r, http.StatusNotFound, "error.html", &errorPage{
			page:    page{Title: "Device not found"},
			Message: "That device is not in the inventory any more.",
		})
		return
	}
	h.fail(w, r, "load device", err)
}

// devicesBackURL returns to the DEVICES page, preserving the router filter so an
// operator working inside one router is not thrown back to the whole fleet.
func devicesBackURL(routerID int64) string {
	if routerID > 0 {
		return "/devices?router=" + strconv.FormatInt(routerID, 10)
	}
	return "/devices"
}

// deviceLabel is the flash-message name of a stored device.
func deviceLabel(device database.Device) string {
	if name := strings.TrimSpace(device.Name); name != "" {
		return name
	}
	return device.MACAddress()
}
