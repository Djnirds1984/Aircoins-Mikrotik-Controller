package handlers

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// maxVoucherBatch caps one generation run so a typo cannot create a million
// rows.
const maxVoucherBatch = 500

// vouchersPage backs the voucher engine view.
type vouchersPage struct {
	page
	Vouchers []database.Voucher
	Routers  []database.Router
	Batches  []string
	Stats    database.VoucherStats
	Filter   voucherFilter
	Form     *voucherForm
	Total    int64
	PageNo   int
	Pages    int
	PrevURL  string
	NextURL  string
}

// voucherFilter mirrors the voucher query string.
type voucherFilter struct {
	Status   string
	RouterID int64
	Batch    string
	Query    string
}

// voucherForm carries the generation form values.
type voucherForm struct {
	RouterID    string
	Batch       string
	Quantity    string
	Prefix      string
	Groups      string
	GroupLength string
	Profile     string
	Duration    string
	DataLimit   string
	DeviceLimit string
	Price       string
	MaxUses     string
	Note        string
	Push        bool

	// RouterLockID locks the form onto one device (device page).
	RouterLockID int64

	Errors map[string]string
}

// newVoucherForm returns a generation form with the usual defaults filled in.
func newVoucherForm(routerID int64) *voucherForm {
	form := &voucherForm{
		Batch:       database.VoucherBatchLabel(time.Now()),
		Quantity:    "25",
		Prefix:      "AIR",
		Groups:      "2",
		GroupLength: "4",
		Profile:     "default",
		Duration:    "60",
		DataLimit:   "0",
		DeviceLimit: "1",
		Price:       "0",
		MaxUses:     "1",
		Errors:      map[string]string{},
	}
	if routerID > 0 {
		form.RouterID = strconv.FormatInt(routerID, 10)
		form.RouterLockID = routerID
	}
	return form
}

// voucherFormFromRequest reads the generation form.
func voucherFormFromRequest(r *http.Request) *voucherForm {
	form := newVoucherForm(0)
	form.RouterID = strings.TrimSpace(r.PostFormValue("router_id"))
	form.Batch = strings.TrimSpace(r.PostFormValue("batch"))
	form.Quantity = strings.TrimSpace(defaultValue(r.PostFormValue("quantity"), "25"))
	form.Prefix = strings.TrimSpace(defaultValue(r.PostFormValue("prefix"), "AIR"))
	form.Groups = strings.TrimSpace(defaultValue(r.PostFormValue("groups"), "2"))
	form.GroupLength = strings.TrimSpace(defaultValue(r.PostFormValue("group_length"), "4"))
	form.Profile = strings.TrimSpace(defaultValue(r.PostFormValue("profile"), "default"))
	form.Duration = strings.TrimSpace(defaultValue(r.PostFormValue("duration_minutes"), "0"))
	form.DataLimit = strings.TrimSpace(defaultValue(r.PostFormValue("data_limit_mb"), "0"))
	form.DeviceLimit = strings.TrimSpace(defaultValue(r.PostFormValue("device_limit"), "1"))
	form.Price = strings.TrimSpace(defaultValue(r.PostFormValue("price"), "0"))
	form.MaxUses = strings.TrimSpace(defaultValue(r.PostFormValue("max_uses"), "1"))
	form.Note = strings.TrimSpace(r.PostFormValue("note"))
	form.Push = r.PostFormValue("push") != ""
	return form
}

// intValue parses a form integer, falling back to the default.
func intValue(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// routerIDValue parses the router selector (0 means "not bound").
func (f *voucherForm) routerIDValue() int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(f.RouterID), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// priceCents converts the decimal price input into cents.
func (f *voucherForm) priceCents() int64 {
	value := strings.ReplaceAll(strings.TrimSpace(f.Price), ",", ".")
	if value == "" {
		return 0
	}
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil || amount < 0 {
		return -1
	}
	return int64(amount*100 + 0.5)
}

// validate checks the generation form.
func (f *voucherForm) validate() bool {
	quantity := intValue(f.Quantity, 0)
	if quantity < 1 || quantity > maxVoucherBatch {
		f.Errors["quantity"] = fmt.Sprintf("Choose between 1 and %d vouchers per batch.", maxVoucherBatch)
	}
	for key, value := range map[string]string{
		"duration_minutes": f.Duration,
		"data_limit_mb":    f.DataLimit,
		"device_limit":     f.DeviceLimit,
		"max_uses":         f.MaxUses,
	} {
		if value == "" {
			continue
		}
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			f.Errors[key] = "Use a whole number, 0 meaning unlimited."
		}
	}
	if length := intValue(f.GroupLength, 4); length < 3 || length > 8 {
		f.Errors["group_length"] = "Random groups must be 3 to 8 characters long."
	}
	if groups := intValue(f.Groups, 2); groups < 1 || groups > 6 {
		f.Errors["groups"] = "Use between 1 and 6 random groups."
	}
	if f.priceCents() < 0 {
		f.Errors["price"] = "Enter a price such as 25 or 25.50."
	}
	return len(f.Errors) == 0
}

// VouchersList renders the voucher engine: filters, statistics and the
// generation form.
func (h *Handler) VouchersList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	filter := voucherFilter{
		Status: strings.TrimSpace(query.Get("status")),
		Batch:  strings.TrimSpace(query.Get("batch")),
		Query:  strings.TrimSpace(query.Get("q")),
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

	h.renderVouchers(w, r, filter, pageNo, nil, http.StatusOK)
}

// renderVouchers is the shared renderer of the voucher page (listing, errors,
// after actions).
func (h *Handler) renderVouchers(w http.ResponseWriter, r *http.Request, filter voucherFilter, pageNo int, form *voucherForm, status int) {
	ctx := r.Context()

	// Keep the status badges honest before listing.
	if _, err := h.db.Vouchers().SyncExpired(ctx, time.Now()); err != nil {
		h.log.Error("cannot expire lapsed vouchers", "error", err)
	}

	dbFilter := database.VoucherFilter{
		Status:   database.VoucherStatus(filter.Status),
		RouterID: filter.RouterID,
		Batch:    filter.Batch,
		Query:    filter.Query,
		Limit:    sessionPageSize,
		Offset:   (pageNo - 1) * sessionPageSize,
	}

	total, err := h.db.Vouchers().Count(ctx, database.VoucherFilter{
		Status: dbFilter.Status, RouterID: filter.RouterID, Batch: filter.Batch, Query: filter.Query,
	})
	if err != nil {
		h.fail(w, r, "count vouchers", err)
		return
	}
	vouchers, err := h.db.Vouchers().List(ctx, dbFilter)
	if err != nil {
		h.fail(w, r, "load vouchers", err)
		return
	}
	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		h.fail(w, r, "load router inventory", err)
		return
	}
	batches, err := h.db.Vouchers().ListBatches(ctx)
	if err != nil {
		h.fail(w, r, "load voucher batches", err)
		return
	}
	stats, err := h.db.Vouchers().Stats(ctx, 0)
	if err != nil {
		h.fail(w, r, "load voucher statistics", err)
		return
	}
	if form == nil {
		form = newVoucherForm(0)
	}

	pages := int((total + sessionPageSize - 1) / sessionPageSize)
	if pages == 0 {
		pages = 1
	}
	h.render(w, r, status, "vouchers.html", &vouchersPage{
		page:     page{Title: "Vouchers", Nav: "vouchers", Back: voucherPageURL(filter, pageNo)},
		Vouchers: vouchers,
		Routers:  routers,
		Batches:  batches,
		Stats:    stats,
		Filter:   filter,
		Form:     form,
		Total:    total,
		PageNo:   pageNo,
		Pages:    pages,
		PrevURL:  voucherPageURL(filter, pageNo-1),
		NextURL:  voucherPageURL(filter, pageNo+1),
	})
}

// voucherPageURL builds a paging link preserving the filters.
func voucherPageURL(filter voucherFilter, page int) string {
	if page < 1 {
		page = 1
	}
	values := url.Values{}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.RouterID > 0 {
		values.Set("router", strconv.FormatInt(filter.RouterID, 10))
	}
	if filter.Batch != "" {
		values.Set("batch", filter.Batch)
	}
	if filter.Query != "" {
		values.Set("q", filter.Query)
	}
	values.Set("page", strconv.Itoa(page))
	return "/vouchers?" + values.Encode()
}

// VouchersGenerate creates a batch of prepaid keys and optionally seeds them on
// the target router so they keep working when the controller is offline.
func (h *Handler) VouchersGenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := voucherFormFromRequest(r)

	// The device manager posts a locked router id.
	if raw := r.PostFormValue("lock_router_id"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			form.RouterLockID = id
			form.RouterID = raw
		}
	}

	back := "/vouchers"
	if form.RouterLockID > 0 {
		back = "/routers/" + strconv.FormatInt(form.RouterLockID, 10)
	}

	if !form.validate() {
		if form.RouterLockID > 0 {
			h.renderRouterVoucherErrors(w, r, form)
			return
		}
		h.renderVouchers(w, r, voucherFilter{}, 1, form, http.StatusUnprocessableEntity)
		return
	}

	quantity := intValue(form.Quantity, 25)
	routerID := form.routerIDValue()

	// Generate unique codes, retrying the whole batch on the (very unlikely)
	// chance the database reports a code clash.
	var (
		inserted int
		err      error
	)
	for attempt := 1; attempt <= 3; attempt++ {
		batch, buildErr := h.buildVoucherBatch(form, quantity, routerID)
		if buildErr != nil {
			h.flashErr(w, r, back, "Cannot generate voucher codes", buildErr)
			return
		}
		inserted, err = h.db.Vouchers().CreateBatch(ctx, batch)
		if err == nil {
			break
		}
		if !errors.Is(err, database.ErrDuplicateVoucherCode) {
			h.flashErr(w, r, back, "Cannot store the voucher batch", err)
			return
		}
		h.log.Warn("voucher code clash, regenerating the batch", "attempt", attempt)
	}
	if err != nil {
		h.flashErr(w, r, back, "Cannot store the voucher batch", err)
		return
	}

	message := strconv.Itoa(inserted) + " voucher(s) created in batch " + form.Batch

	// Optionally provision the keys on the device straight away.
	if form.Push && routerID > 0 {
		router, err := h.db.Routers().Get(ctx, routerID)
		if err != nil {
			h.flashAndRedirect(w, r, back, "warn",
				message+", but the router could not be loaded for provisioning: "+err.Error())
			return
		}
		created, failures := h.pushVouchers(ctx, router, database.VoucherFilter{Batch: form.Batch, Limit: maxVoucherBatch})
		message += ". Provisioned " + strconv.Itoa(created) + " key(s) on " + router.Name
		if len(failures) > 0 {
			h.flashAndRedirect(w, r, back, "warn",
				message+"; first problem: "+truncateText(failures[0], 160))
			return
		}
	}
	h.flashAndRedirect(w, r, back, "ok", message)
}

// buildVoucherBatch assembles the voucher rows to insert.
func (h *Handler) buildVoucherBatch(form *voucherForm, quantity int, routerID int64) ([]database.Voucher, error) {
	options := database.VoucherCodeOptions{
		Prefix:      form.Prefix,
		Groups:      intValue(form.Groups, 2),
		GroupLength: intValue(form.GroupLength, 4),
	}
	batch := defaultValue(form.Batch, database.VoucherBatchLabel(time.Now()))

	vouchers := make([]database.Voucher, 0, quantity)
	for i := 0; i < quantity; i++ {
		code, err := database.GenerateVoucherCode(options)
		if err != nil {
			return nil, err
		}
		voucher := database.Voucher{
			Code:            code,
			Batch:           batch,
			Profile:         defaultValue(form.Profile, "default"),
			DurationMinutes: intValue(form.Duration, 0),
			DataLimitMB:     intValue(form.DataLimit, 0),
			DeviceLimit:     intValue(form.DeviceLimit, 1),
			PriceCents:      form.priceCents(),
			MaxUses:         maxIntValue(intValue(form.MaxUses, 1), 1),
			Note:            form.Note,
			Status:          database.VoucherUnused,
		}
		if routerID > 0 {
			id := routerID
			voucher.RouterID = &id
		}
		vouchers = append(vouchers, voucher)
	}
	return vouchers, nil
}

func maxIntValue(value, min int) int {
	if value < min {
		return min
	}
	return value
}

// renderRouterVoucherErrors re-renders the device page when its inline voucher
// form fails validation.
func (h *Handler) renderRouterVoucherErrors(w http.ResponseWriter, r *http.Request, form *voucherForm) {
	ctx := r.Context()
	router, err := h.db.Routers().Get(ctx, form.RouterLockID)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
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
	cached, err := h.db.Sessions().OpenForRouter(ctx, router.ID)
	if err != nil {
		h.fail(w, r, "load cached sessions", err)
		return
	}
	h.render(w, r, http.StatusUnprocessableEntity, "router.html", &routerPage{
		page:          page{Title: router.Name, Nav: "routers", Back: "/routers/" + strconv.FormatInt(router.ID, 10)},
		Router:        router,
		Form:          routerFormFromRouter(router),
		VoucherForm:   form,
		Vouchers:      vouchers,
		VoucherStats:  stats,
		CachedClients: cached,
		LiveError:     "The page was reloaded while fixing the voucher form; press Refresh to read the device again.",
		LiveHint:      "Voucher form validation failed",
	})
}

// hotspotUserSpec maps a voucher onto the RouterOS hotspot user that enforces
// its time and data limits on the device itself.
func (h *Handler) hotspotUserSpec(voucher database.Voucher) HotspotUserSpec {
	profile := defaultValue(voucher.Profile, "default")
	comment := "aircoins " + voucher.Batch
	if voucher.Note != "" {
		comment += " - " + truncateText(voucher.Note, 40)
	}
	return HotspotUserSpec{
		Name:               voucher.Code,
		Password:           voucher.Code,
		Profile:            profile,
		Comment:            comment,
		LimitUptimeMinutes: voucher.DurationMinutes,
		LimitBytesTotal:    voucher.DataLimitBytes(),
		DeviceLimit:        voucher.DeviceLimit,
	}
}

// pushVouchers provisions every voucher matching the filter on the device and
// remembers which keys are ready to work while the controller is offline.
func (h *Handler) pushVouchers(ctx context.Context, router database.Router, filter database.VoucherFilter) (int, []string) {
	vouchers, err := h.db.Vouchers().List(ctx, filter)
	if err != nil {
		return 0, []string{err.Error()}
	}
	if len(vouchers) == 0 {
		return 0, nil
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		return 0, []string{routerErrorHint(err)}
	}
	defer client.Close()

	// A batch can take a while; give it room but stay bounded.
	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout*4)
	defer cancel()

	var (
		pushed    int
		pushedIDs []int64
		failures  []string
	)
	for _, voucher := range vouchers {
		if voucher.Status == database.VoucherDisabled {
			continue
		}
		if _, err := client.EnsureHotspotUser(callCtx, h.hotspotUserSpec(voucher)); err != nil {
			failures = append(failures, voucher.Code+": "+routerErrorHint(err))
			if len(failures) >= 5 {
				failures = append(failures, "stopped after 5 errors")
				break
			}
			continue
		}
		pushedIDs = append(pushedIDs, voucher.ID)
		pushed++
	}
	if err := h.db.Vouchers().MarkPushed(ctx, pushedIDs, time.Now()); err != nil {
		failures = append(failures, "keys were provisioned but the local record failed: "+err.Error())
	}
	return pushed, failures
}

// VoucherPush provisions one voucher on its router.
func (h *Handler) VoucherPush(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.voucherIDPath(w, r)
	if !ok {
		return
	}
	back := voucherBackURL(r)
	voucher, err := h.db.Vouchers().Get(ctx, id)
	if err != nil {
		h.notFoundVoucher(w, r, err)
		return
	}
	if voucher.RouterID == nil {
		h.flashAndRedirect(w, r, back, "err",
			"Voucher "+voucher.Code+" is not bound to a router. Edit the batch or assign it to a device first.")
		return
	}
	router, err := h.db.Routers().Get(ctx, *voucher.RouterID)
	if err != nil {
		h.flashErr(w, r, back, "Load the voucher router", err)
		return
	}

	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashErr(w, r, back, "Cannot reach "+router.Name+" to provision the voucher", err)
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	created, err := client.EnsureHotspotUser(callCtx, h.hotspotUserSpec(voucher))
	if err != nil {
		h.flashErr(w, r, back, "Provisioning voucher "+voucher.Code+" failed", err)
		return
	}
	if err := h.db.Vouchers().MarkPushed(ctx, []int64{voucher.ID}, time.Now()); err != nil {
		h.log.Error("cannot mark voucher pushed", "code", voucher.Code, "error", err)
	}

	action := "updated on"
	if created {
		action = "created on"
	}
	h.flashAndRedirect(w, r, back, "ok",
		"Voucher "+voucher.Code+" "+action+" "+router.Name+" with profile "+
			defaultValue(voucher.Profile, "default")+" and limits: "+voucher.LimitSummary())
}

// VoucherValidate cross-checks a voucher against its router: local ledger plus
// the hotspot user state on the device.
func (h *Handler) VoucherValidate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.voucherIDPath(w, r)
	if !ok {
		return
	}
	back := voucherBackURL(r)
	voucher, err := h.db.Vouchers().Get(ctx, id)
	if err != nil {
		h.notFoundVoucher(w, r, err)
		return
	}

	findings := make([]string, 0, 4)
	if err := voucher.Redeemable(time.Now()); err != nil {
		findings = append(findings, "ledger: "+strings.TrimPrefix(err.Error(), database.ErrVoucherNotRedeemable.Error()+": "))
	} else {
		findings = append(findings, "ledger: ready to redeem ("+strconv.Itoa(voucher.RemainingUses())+" use(s) left)")
	}

	if voucher.RouterID == nil {
		findings = append(findings, "device: not bound to a router, so the portal cannot log this key in")
		h.flashAndRedirect(w, r, back, "warn", "Voucher "+voucher.Code+" — "+strings.Join(findings, "; "))
		return
	}
	router, err := h.db.Routers().Get(ctx, *voucher.RouterID)
	if err != nil {
		h.flashErr(w, r, back, "Load the voucher router", err)
		return
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		findings = append(findings, "device: unreachable — "+routerErrorHint(err))
		h.flashAndRedirect(w, r, back, "warn", "Voucher "+voucher.Code+" — "+strings.Join(findings, "; "))
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	user, err := client.FindHotspotUser(callCtx, voucher.Code)
	switch {
	case errors.Is(err, ErrRouterNotFound):
		findings = append(findings, "device: no hotspot user yet, it will be created on first login or by pressing Provision")
	case err != nil:
		findings = append(findings, "device: cannot read the hotspot user — "+routerErrorHint(err))
	default:
		state := "enabled"
		if user.Disabled {
			state = "disabled"
		}
		findings = append(findings, fmt.Sprintf("device: user exists (%s, profile %s, used %s of %s, limit %s / %s)",
			state, defaultValue(user.Profile, "default"), humanBytes(user.BytesIn+user.BytesOut),
			defaultValue(user.LimitBytes, "unlimited"),
			defaultValue(user.LimitUptime, "unlimited"), defaultValue(user.Uptime, "0s")))
	}
	h.flashAndRedirect(w, r, back, "ok", "Voucher "+voucher.Code+" — "+strings.Join(findings, "; "))
}

// VoucherSetStatus disables, re-enables or marks a voucher as used.
func (h *Handler) VoucherSetStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.voucherIDPath(w, r)
	if !ok {
		return
	}
	back := voucherBackURL(r)

	var status database.VoucherStatus
	switch strings.TrimSpace(r.PostFormValue("status")) {
	case "disabled":
		status = database.VoucherDisabled
	case "unused":
		status = database.VoucherUnused
	case "used":
		status = database.VoucherUsed
	case "expired":
		status = database.VoucherExpired
	default:
		h.flashAndRedirect(w, r, back, "err", "Unknown voucher action.")
		return
	}

	voucher, err := h.db.Vouchers().Get(ctx, id)
	if err != nil {
		h.notFoundVoucher(w, r, err)
		return
	}
	if err := h.db.Vouchers().SetStatus(ctx, id, status); err != nil {
		h.flashErr(w, r, back, "Cannot update the voucher status", err)
		return
	}
	h.flashAndRedirect(w, r, back, "ok", "Voucher "+voucher.Code+" is now "+status.Label())
}

// VoucherDelete removes one voucher and, when its hotspot user exists on the
// device, revokes it there too.
func (h *Handler) VoucherDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.voucherIDPath(w, r)
	if !ok {
		return
	}
	back := voucherBackURL(r)
	voucher, err := h.db.Vouchers().Get(ctx, id)
	if err != nil {
		h.notFoundVoucher(w, r, err)
		return
	}

	revoked := false
	if r.PostFormValue("revoke") != "" && voucher.RouterID != nil {
		if router, err := h.db.Routers().Get(ctx, *voucher.RouterID); err == nil {
			if client, err := h.dialRouter(ctx, router); err == nil {
				callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
				if err := client.RemoveHotspotUser(callCtx, voucher.Code); err == nil {
					revoked = true
				} else {
					h.log.Warn("cannot revoke hotspot user", "code", voucher.Code, "error", err)
				}
				cancel()
				_ = client.Close()
			}
		}
	}
	if err := h.db.Vouchers().Delete(ctx, id); err != nil {
		h.flashErr(w, r, back, "Cannot delete the voucher", err)
		return
	}

	message := "Voucher " + voucher.Code + " deleted"
	if revoked {
		message += " and its hotspot user was removed from the router"
	}
	h.flashAndRedirect(w, r, back, "ok", message)
}

// VoucherBatchDelete removes a whole batch, optionally keeping redeemed keys.
func (h *Handler) VoucherBatchDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batch := strings.TrimSpace(r.PostFormValue("batch"))
	if batch == "" {
		h.flashAndRedirect(w, r, "/vouchers", "err", "No batch was selected.")
		return
	}
	onlyUnused := r.PostFormValue("only_unused") != ""

	deleted, err := h.db.Vouchers().DeleteBatch(ctx, batch, onlyUnused)
	if err != nil {
		h.flashErr(w, r, "/vouchers", "Cannot delete the voucher batch", err)
		return
	}
	message := strconv.FormatInt(deleted, 10) + " voucher(s) deleted from " + batch
	if onlyUnused {
		message += " (redeemed keys were kept)"
	}
	h.flashAndRedirect(w, r, "/vouchers", "ok", message)
}

// VouchersExport streams the filtered vouchers as CSV so they can be printed or
// imported into a POS system.
func (h *Handler) VouchersExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	filter := database.VoucherFilter{
		Status: database.VoucherStatus(strings.TrimSpace(query.Get("status"))),
		Batch:  strings.TrimSpace(query.Get("batch")),
		Query:  strings.TrimSpace(query.Get("q")),
		Limit:  2000,
	}
	if raw := query.Get("router"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.RouterID = id
		}
	}

	vouchers, err := h.db.Vouchers().List(ctx, filter)
	if err != nil {
		h.fail(w, r, "load vouchers for export", err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="aircoins-vouchers.csv"`)
	writer := csv.NewWriter(w)
	defer writer.Flush()

	header := []string{
		"code", "batch", "router", "profile", "duration_minutes", "data_limit_mb",
		"device_limit", "price", "status", "uses", "max_uses", "created_at",
		"activated_at", "expires_at", "pushed_to_router", "note",
	}
	if err := writer.Write(header); err != nil {
		h.log.Error("csv header failed", "error", err)
		return
	}
	for _, voucher := range vouchers {
		row := []string{
			voucher.Code,
			voucher.Batch,
			voucher.RouterName,
			voucher.Profile,
			strconv.Itoa(voucher.DurationMinutes),
			strconv.Itoa(voucher.DataLimitMB),
			strconv.Itoa(voucher.DeviceLimit),
			money(voucher.PriceCents),
			voucher.Status.Label(),
			strconv.Itoa(voucher.Uses),
			strconv.Itoa(voucher.MaxUses),
			voucher.CreatedAt.Format(time.RFC3339),
			timeLabel(voucher.ActivatedAt),
			timeLabel(voucher.ExpiresAt),
			yesNo(voucher.PushedAt != nil),
			voucher.Note,
		}
		if err := writer.Write(row); err != nil {
			h.log.Error("csv row failed", "code", voucher.Code, "error", err)
			return
		}
	}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// voucherBackURL returns the page a voucher action should return to.
func voucherBackURL(r *http.Request) string {
	back := strings.TrimSpace(r.PostFormValue("back"))
	if strings.HasPrefix(back, "/") && !strings.HasPrefix(back, "//") {
		return back
	}
	return "/vouchers"
}

// notFoundVoucher renders the 404 page for a missing voucher.
func (h *Handler) notFoundVoucher(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, database.ErrNotFound) {
		h.render(w, r, http.StatusNotFound, "error.html", &errorPage{
			page:    page{Title: "Voucher not found"},
			Message: "That voucher key does not exist.",
		})
		return
	}
	h.fail(w, r, "load voucher", err)
}

// voucherRedemption describes the outcome of using a voucher key.
type voucherRedemption struct {
	Voucher database.Voucher
	// CreatedUser reports whether the hotspot user had to be created.
	CreatedUser bool
	// LoginViaAPI reports whether the controller logged the client in through
	// the RouterOS API instead of leaving it to the hotspot login page.
	LoginViaAPI bool
	// Note carries a caveat worth showing to the customer.
	Note string
}

// redeemVoucher is the shared redemption path of the captive portal: it
// provisions the key on the device, authenticates the client and only then
// consumes one redemption in the local ledger.
//
// Ordering matters: a failure to reach the device must never burn a voucher.
func (h *Handler) redeemVoucher(ctx context.Context, client *MikrotikClient, voucher database.Voucher, mac, ip string) (voucherRedemption, error) {
	result := voucherRedemption{Voucher: voucher}

	if voucher.RouterID == nil {
		return result, errors.New("this key is not assigned to a hotspot yet, please contact the front desk")
	}
	if err := voucher.Redeemable(time.Now()); err != nil {
		return result, err
	}

	created, err := client.EnsureHotspotUser(ctx, h.hotspotUserSpec(voucher))
	if err != nil {
		return result, fmt.Errorf("preparing the key on the hotspot failed: %w", err)
	}
	result.CreatedUser = created

	loginErr := client.HotspotLogin(ctx, voucher.Code, voucher.Code, mac, ip)
	switch {
	case loginErr == nil:
		result.LoginViaAPI = true
	case errors.Is(loginErr, ErrRouterUnknownHost), errors.Is(loginErr, ErrRouterNoCommand):
		// This RouterOS build cannot log the client in through the API (or the
		// client is not in the host table yet). The key now exists on the
		// device, so the customer can finish the login on the hotspot page.
		result.Note = "the hotspot asked your device to finish the login"
	default:
		return result, fmt.Errorf("the hotspot refused the login: %w", loginErr)
	}

	updated, err := h.db.Vouchers().Redeem(ctx, voucher.ID, time.Now())
	if err != nil {
		// The client is authenticated but the ledger refused the redemption
		// (another device consumed the last use). Drop the session again so the
		// customer is not online for free, then report the problem.
		callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
		if disconnectErr := client.DisconnectClient(callCtx, "", voucher.Code); disconnectErr != nil {
			h.log.Warn("cannot roll back hotspot login", "code", voucher.Code, "error", disconnectErr)
		}
		cancel()
		return result, err
	}
	result.Voucher = updated
	return result, nil
}
