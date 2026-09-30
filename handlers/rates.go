package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// The admin routes of the RATES page.
//
// They are deliberately not under /portal/, so they never inherit the
// guest-facing exemptions in isPublicPath and csrfGuard. Pricing a coin is a
// money decision: it must require a panel session and a CSRF token.
const (
	ratesPath       = "/rates"
	ratesTogglePath = "/rates/{id}/toggle"
	ratesDeletePath = "/rates/{id}/delete"
)

// ratesPage backs /admin/rates: what the coin slot charges and how much Wi-Fi
// time each coin buys.
type ratesPage struct {
	page
	// Rates is every configured tier, active or not.
	Rates []database.Rate
	// Form carries the submitted values back on a validation error, so a
	// mistyped peso amount does not also clear the dropdowns.
	Form rateForm
	// EditingID is the tier the form is bound to; 0 means "create a new one".
	EditingID int64
	// Dropdown bounds, so the template cannot drift from the store's limits.
	Days    []int
	Hours   []int
	Minutes []int
	Pulses  []int
	// FallbackSeconds is the environment-configured rate used when no tier is
	// active. It is shown so the operator understands what is still in effect.
	FallbackSeconds      int
	FallbackSecondsLabel string
	// NodeReady reports whether the coin endpoint will accept a node at all.
	NodeReady bool
}

// rateForm is the submitted pricing form.
//
// The three time fields are separate strings rather than a parsed struct so an
// unparseable dropdown value can be reported against the exact field that
// caused it, instead of collapsing the whole form into one error.
type rateForm struct {
	ID      int64
	Label   string
	Pulses  string
	Amount  string
	Days    string
	Hours   string
	Minutes string
	Active  bool
	Errors  map[string]string
}

// newRateForm returns a form pre-filled with a sensible first tier: one pulse,
// five pesos, fifteen minutes. Those are the numbers a piso Wi-Fi box starts
// with, so the common case is one submit away rather than a blank form.
func newRateForm() rateForm {
	return rateForm{
		Pulses:  "1",
		Amount:  "5.00",
		Days:    "0",
		Hours:   "0",
		Minutes: "15",
		Active:  true,
		Errors:  map[string]string{},
	}
}

// rateFormFromRequest reads the submitted pricing form.
func rateFormFromRequest(r *http.Request) rateForm {
	form := newRateForm()
	if raw := strings.TrimSpace(r.PostFormValue("id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			form.ID = id
		}
	}
	form.Label = strings.TrimSpace(r.PostFormValue("label"))
	form.Pulses = strings.TrimSpace(defaultValue(r.PostFormValue("pulses"), "1"))
	// The price is deliberately NOT defaulted. A blank price field must be
	// reported as an error rather than silently becoming the 5.00 of the
	// default form: an operator who cleared the field to retype it would
	// otherwise save a coin price they never chose.
	form.Amount = strings.TrimSpace(r.PostFormValue("amount"))
	form.Days = strings.TrimSpace(defaultValue(r.PostFormValue("days"), "0"))
	form.Hours = strings.TrimSpace(defaultValue(r.PostFormValue("hours"), "0"))
	form.Minutes = strings.TrimSpace(defaultValue(r.PostFormValue("minutes"), "15"))
	form.Active = r.PostFormValue("active") != ""
	return form
}

// rate builds a store tier from the form, reporting each field problem in its
// own key so the template can show the message under the offending control.
//
// The peso amount is parsed as a decimal here rather than in the store: the
// operator types "5" or "5.00" and the store wants integer cents, and doing that
// conversion in two places is how a rounding drift creeps in.
func (f *rateForm) rate() database.Rate {
	rate := database.Rate{
		ID:     f.ID,
		Label:  f.Label,
		Active: f.Active,
	}

	pulses, err := strconv.Atoi(f.Pulses)
	if err != nil || pulses < 1 {
		f.Errors["pulses"] = "Enter how many pulses one coin produces, as a whole number (normally 1)."
	} else {
		rate.Pulses = pulses
	}

	// Each parser reports its problem through setErr rather than by writing an
	// empty string into the map. Assigning "" would still create the key, and
	// the len(Errors) check below would then reject a perfectly good form.
	var amountErr, daysErr, hoursErr, minutesErr string
	rate.AmountCents, amountErr = parsePesoAmount(f.Amount)
	rate.Days, daysErr = rateFieldInt(f.Days)
	rate.Hours, hoursErr = rateFieldInt(f.Hours)
	rate.Minutes, minutesErr = rateFieldInt(f.Minutes)
	setErr(f.Errors, "amount", amountErr)
	setErr(f.Errors, "days", daysErr)
	setErr(f.Errors, "hours", hoursErr)
	setErr(f.Errors, "minutes", minutesErr)

	if len(f.Errors) > 0 {
		// The zero value is returned so a caller that ignores the error map
		// still cannot accidentally write a half-parsed tier.
		return database.Rate{}
	}
	return rate
}

// setErr records a field error, and does nothing at all when there is no
// message. A map is the right shape for "which fields are wrong", and the
// template treats a missing key as valid, so an empty message must never become
// a key.
func setErr(errs map[string]string, field, message string) {
	if message != "" {
		errs[field] = message
	}
}

// rateFieldInt parses one dropdown value, returning a message naming the field.
func rateFieldInt(raw string) (int, string) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, "Pick a value from the list."
	}
	return n, ""
}

// parsePesoAmount converts the operator decimal entry into cents.
//
// It accepts a bare "5", "5.00" or "5.50" and rejects anything with more than
// two decimal places rather than silently rounding, because a price that is
// quietly off by half a cent is a price the reconciliation cannot reproduce.
func parsePesoAmount(raw string) (int64, string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSpace(strings.TrimLeft(raw, "PHPphp"))
	raw = strings.TrimSpace(strings.TrimLeft(raw, "$"))
	raw = strings.ReplaceAll(raw, ",", "")
	if raw == "" {
		return 0, "Enter the price the customer pays."
	}
	if strings.HasPrefix(raw, "-") {
		return 0, "The price cannot be negative."
	}
	whole, fraction, _ := strings.Cut(raw, ".")
	whole = strings.TrimSpace(whole)
	fraction = strings.TrimSpace(fraction)
	if whole == "" {
		whole = "0"
	}
	if len(fraction) > 2 {
		return 0, "Use at most two decimal places, e.g. 5.00."
	}
	pesos, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || pesos < 0 {
		return 0, "Enter the price as a number, e.g. 5.00."
	}
	for len(fraction) < 2 {
		fraction += "0"
	}
	var centavos int64
	if fraction != "" {
		if centavos, err = strconv.ParseInt(fraction, 10, 64); err != nil {
			return 0, "Enter the price as a number, e.g. 5.00."
		}
	}
	cents := pesos*100 + centavos
	if cents > database.MaxRateAmountCents {
		return 0, "That price is too large."
	}
	return cents, ""
}

// Rates renders the pricing page.
//
// An ?edit=<id> parameter binds the form to an existing tier so an operator
// corrects a price by editing it in place rather than deleting and retyping it,
// which is how the pulse count gets lost.
func (h *Handler) Rates(w http.ResponseWriter, r *http.Request) {
	form := rateForm{}
	var editingID int64

	if raw := strings.TrimSpace(r.URL.Query().Get("edit")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid rate id", http.StatusBadRequest)
			return
		}
		rate, err := h.db.Rates().Get(r.Context(), id)
		if errors.Is(err, database.ErrNotFound) {
			h.renderRates(w, r, http.StatusNotFound, rateForm{}, 0)
			return
		}
		if err != nil {
			h.fail(w, r, "load rate", err)
			return
		}
		editingID = rate.ID
		form = rateForm{
			ID:      rate.ID,
			Label:   rate.Label,
			Pulses:  strconv.Itoa(rate.Pulses),
			Amount:  formatPesoAmount(rate.AmountCents),
			Days:    strconv.Itoa(rate.Days),
			Hours:   strconv.Itoa(rate.Hours),
			Minutes: strconv.Itoa(rate.Minutes),
			Active:  rate.Active,
			Errors:  map[string]string{},
		}
	}

	h.renderRates(w, r, http.StatusOK, form, editingID)
}

// formatPesoAmount renders stored cents back into the decimal the form edits,
// so a round trip through the panel does not change the price it was given.
func formatPesoAmount(cents int64) string {
	if cents < 0 {
		cents = 0
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// RateSave creates or updates one tier.
//
// Both actions share this handler because they share the form: the operator
// edits the same fields either way, and splitting them would mean validating
// the same four dropdowns twice.
func (h *Handler) RateSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the form", http.StatusBadRequest)
		return
	}

	form := rateFormFromRequest(r)
	rate := form.rate()
	if len(form.Errors) == 0 {
		var err error
		if form.ID > 0 {
			// Editing a row that was deleted between the page render and this
			// submit must not silently create a new tier, so the id is carried
			// into the error rather than retried as an insert.
			if _, err = h.db.Rates().Update(r.Context(), rate); errors.Is(err, database.ErrNotFound) {
				form.Errors["form"] = "That rate no longer exists. Add it again as a new rate."
				err = nil
			}
		} else {
			_, err = h.db.Rates().Create(r.Context(), rate)
		}
		if err != nil && !errors.Is(err, database.ErrNotFound) {
			form.Errors["form"] = strings.TrimPrefix(err.Error(), "database: ")
			err = nil
		}
		if err == nil && len(form.Errors) == 0 {
			message := "Rate saved. Coins inserted from now on are priced with it."
			if form.ID > 0 {
				message = "Rate updated."
			}
			h.flashAndRedirect(w, r, h.cfg.AdminPath+ratesPath, "ok", message)
			return
		}
	}

	h.renderRates(w, r, http.StatusBadRequest, form, form.ID)
}

// RateToggle enables or disables a tier without retyping its price.
func (h *Handler) RateToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := h.rateIDPath(w, r)
	if !ok {
		return
	}
	// An unchecked checkbox simply does not appear in the body, so the only
	// honest reading of "the operator pressed Disable" is the absence of the
	// field. The intent is stated explicitly in the form rather than inferred
	// from the current state, which would make a double submit flip it back.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the form", http.StatusBadRequest)
		return
	}
	enable := r.PostFormValue("active") != ""
	if _, err := h.db.Rates().SetActive(r.Context(), id, enable); err != nil {
		h.fail(w, r, "set rate active", err)
		return
	}
	kind, message := "ok", "Rate enabled."
	if !enable {
		kind, message = "warn", "Rate disabled. Coins inserted from now on are not priced with it."
	}
	h.flashAndRedirect(w, r, h.cfg.AdminPath+ratesPath, kind, message)
}

// RateDelete removes a tier outright.
func (h *Handler) RateDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.rateIDPath(w, r)
	if !ok {
		return
	}
	if err := h.db.Rates().Delete(r.Context(), id); err != nil {
		h.fail(w, r, "delete rate", err)
		return
	}
	h.flashAndRedirect(w, r, h.cfg.AdminPath+ratesPath, "ok", "Rate deleted.")
}

// renderRates loads the tier list and draws the page.
func (h *Handler) renderRates(w http.ResponseWriter, r *http.Request, status int, form rateForm, editingID int64) {
	ctx := r.Context()

	rates, err := h.db.Rates().List(ctx)
	if err != nil {
		h.fail(w, r, "load rates", err)
		return
	}

	// A validation failure must show what the operator typed, not an empty
	// form, so the defaults are only restored when nothing was submitted.
	if form.Pulses == "" && form.Amount == "" && form.Label == "" && len(form.Errors) == 0 {
		form = newRateForm()
	}
	if form.Errors == nil {
		form.Errors = map[string]string{}
	}

	h.render(w, r, status, "rates.html", &ratesPage{
		page:                 page{Title: "Rates", Nav: "rates", Back: h.cfg.AdminPath + ratesPath},
		Rates:                rates,
		Form:                 form,
		EditingID:            editingID,
		Days:                 rateDropdown(database.MaxRateDays),
		Hours:                rateDropdown(database.MaxRateHours),
		Minutes:              rateDropdown(database.MaxRateMinutes),
		Pulses:               rateDropdown(database.MaxRatePulses),
		FallbackSeconds:      h.cfg.CoinPulseSeconds,
		FallbackSecondsLabel: database.FormatCoinSeconds(h.cfg.CoinPulseSeconds),
		NodeReady:            strings.TrimSpace(h.cfg.CoinNodeToken) != "",
	})
}

// rateDropdown builds the inclusive 0..max option list the time dropdowns use.
//
// The bounds come from the store constants rather than being written into the
// template, so raising MaxRateDays widens the dropdown and the validation in one
// edit instead of leaving the two disagreeing.
func rateDropdown(max int) []int {
	out := make([]int, 0, max+1)
	for i := 0; i <= max; i++ {
		out = append(out, i)
	}
	return out
}

// rateIDPath extracts and validates the {id} segment of a rate action route.
func (h *Handler) rateIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid rate id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
