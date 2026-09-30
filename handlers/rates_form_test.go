package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// TestRatesSavePersistsStructuredTime walks the whole create path through the
// real form and asserts the row that lands in SQLite, which is the only place
// the coin path reads the price from.
func TestRatesSavePersistsStructuredTime(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())
	client := ratesClient(t, base, db)

	page := getBody(t, client, base+"/admin/rates")
	resp, err := client.PostForm(base+"/admin/rates/save", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"label":      {"overnight"},
		"pulses":     {"1"},
		"amount":     {"10.50"},
		"days":       {"1"},
		"hours":      {"2"},
		"minutes":    {"30"},
		"active":     {"1"},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save returned %d, want the pricing page (%s)", resp.StatusCode, truncateForTest(readAll(t, resp)))
	}

	rates, err := db.Rates().List(ctx)
	if err != nil {
		t.Fatalf("list rates: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("got %d rates, want 1", len(rates))
	}
	got := rates[0]
	if got.Days != 1 || got.Hours != 2 || got.Minutes != 30 {
		t.Errorf("stored %dd %dh %dm, want 1d 2h 30m", got.Days, got.Hours, got.Minutes)
	}
	// 10.50 pesos is 1050 cents. A float round trip here would show up as 1049
	// or 1051 and quietly corrupt the reconciliation.
	if got.AmountCents != 1050 {
		t.Errorf("AmountCents = %d, want 1050", got.AmountCents)
	}
	if want := 86400 + 2*3600 + 30*60; got.GrantedSeconds != want {
		t.Errorf("GrantedSeconds = %d, want %d", got.GrantedSeconds, want)
	}
	if !got.Active {
		t.Error("the tier was stored inactive even though the box was ticked")
	}
}

// TestRatesSaveRejectsZeroTimeTier proves the configuration-time guard actually
// fires: a tier that would award no Wi-Fi must not be storable, or a customer's
// coin would be swallowed with no error anywhere.
func TestRatesSaveRejectsZeroTimeTier(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())
	client := ratesClient(t, base, db)

	page := getBody(t, client, base+"/admin/rates")
	resp, err := client.PostForm(base+"/admin/rates/save", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"pulses":     {"1"},
		"amount":     {"5.00"},
		"days":       {"0"},
		"hours":      {"0"},
		"minutes":    {"0"},
		"active":     {"1"},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a zero-time tier got status %d, want 400", resp.StatusCode)
	}
	rates, err := db.Rates().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rates) != 0 {
		t.Fatalf("a zero-time tier was stored anyway: %d rows", len(rates))
	}
}

// TestRatesSaveValidatesThePesoAmount proves the price field is actually parsed.
// A silent "0.00" on a typo is how an operator ends up giving away Wi-Fi for
// nothing and not noticing for a week.
func TestRatesSaveValidatesThePesoAmount(t *testing.T) {
	for _, amount := range []string{"", "abc", "-5", "5.999"} {
		t.Run("amount="+amount, func(t *testing.T) {
			base, db := newCaptiveE2E(t, ratesTestConfig())
			client := ratesClient(t, base, db)

			page := getBody(t, client, base+"/admin/rates")
			resp, err := client.PostForm(base+"/admin/rates/save", url.Values{
				"csrf_token": {csrfOf(t, page)},
				"pulses":     {"1"},
				"amount":     {amount},
				"days":       {"0"},
				"hours":      {"0"},
				"minutes":    {"15"},
				"active":     {"1"},
			})
			if err != nil {
				t.Fatalf("POST save: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("amount %q got status %d, want 400", amount, resp.StatusCode)
			}
			rates, err := db.Rates().List(context.Background())
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(rates) != 0 {
				t.Errorf("amount %q stored a rate anyway", amount)
			}
		})
	}
}

// TestRatesEditRoundTrip proves an operator correcting a price edits it in
// place. This is the path that stops them deleting and retyping a tier and
// losing its pulse count.
func TestRatesEditRoundTrip(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())
	client := ratesClient(t, base, db)

	created, err := db.Rates().Create(ctx, database.Rate{
		Label: "promo", Pulses: 2, AmountCents: 2000, Minutes: 45, Active: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	page := getBody(t, client, base+"/admin/rates?edit="+itoa(created.ID))
	// The edit form must come back pre-filled with the stored values, including
	// the structured time and the peso amount.
	for _, want := range []string{`value="promo"`, `value="20.00"`, `<option value="2" selected`} {
		if !strings.Contains(page, want) {
			t.Errorf("the edit form is missing %q", want)
		}
	}

	resp, err := client.PostForm(base+"/admin/rates/save", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"id":         {itoa(created.ID)},
		"label":      {"promo"},
		"pulses":     {"2"},
		"amount":     {"25.00"},
		"days":       {"0"},
		"hours":      {"1"},
		"minutes":    {"0"},
		"active":     {"1"},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save returned %d", resp.StatusCode)
	}

	rates, err := db.Rates().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("editing created a second row: %d rows", len(rates))
	}
	if rates[0].ID != created.ID {
		t.Errorf("edit created id %d, want the edited %d", rates[0].ID, created.ID)
	}
	if rates[0].GrantedSeconds != 3600 {
		t.Errorf("GrantedSeconds = %d, want 3600", rates[0].GrantedSeconds)
	}
	if rates[0].AmountCents != 2500 {
		t.Errorf("AmountCents = %d, want 2500", rates[0].AmountCents)
	}
}

// TestRatesToggleKeepsThePrice proves disabling a tier is not the same as
// deleting it, which is the whole reason the store has a soft switch: a credit
// sold yesterday must stay explicable.
func TestRatesToggleKeepsThePrice(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())
	client := ratesClient(t, base, db)

	rate, err := db.Rates().Create(ctx, database.Rate{
		Label: "promo", Pulses: 1, AmountCents: 500, Minutes: 15, Active: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	page := getBody(t, client, base+"/admin/rates")
	resp, err := client.PostForm(base+"/admin/rates/"+itoa(rate.ID)+"/toggle", url.Values{
		"csrf_token": {csrfOf(t, page)},
	})
	if err != nil {
		t.Fatalf("POST toggle: %v", err)
	}
	defer resp.Body.Close()

	stored, err := db.Rates().Get(ctx, rate.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Active {
		t.Error("the tier is still active after an explicit disable")
	}
	if stored.GrantedSeconds != 900 {
		t.Errorf("disabling changed the price: GrantedSeconds = %d, want 900", stored.GrantedSeconds)
	}
}

// TestRatesDeleteRemovesTheRow proves the delete action is wired to the store
// rather than silently doing nothing.
func TestRatesDeleteRemovesTheRow(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())
	client := ratesClient(t, base, db)

	rate, err := db.Rates().Create(ctx, database.Rate{Pulses: 1, AmountCents: 500, Minutes: 15, Active: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	page := getBody(t, client, base+"/admin/rates")
	resp, err := client.PostForm(base+"/admin/rates/"+itoa(rate.ID)+"/delete", url.Values{
		"csrf_token": {csrfOf(t, page)},
	})
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	defer resp.Body.Close()

	if _, err := db.Rates().Get(ctx, rate.ID); err == nil {
		t.Error("the tier survived the delete")
	}
}

// TestRatesActionsRequireAPanelSession proves the mutating routes are not open
// to the guest network. Pricing a coin is a money decision, and an open one would
// let anyone standing at the machine set a coin to buy a month of Wi-Fi.
//
// The expected answer is 403 rather than a redirect, and that is not an
// accident: the CSRF guard is the outermost middleware, so an anonymous POST with
// no token is refused before the session guard even looks at it. A 200 or a
// served form here would mean the route ran.
func TestRatesActionsRequireAPanelSession(t *testing.T) {
	base, _ := newUnauthE2E(t, ratesTestConfig())

	for _, path := range []string{"/admin/rates/save", "/admin/rates/1/toggle", "/admin/rates/1/delete"} {
		resp, err := http.PostForm(base+path, url.Values{
			"pulses": {"1"}, "amount": {"5"}, "minutes": {"15"},
		})
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s accepted an unauthenticated POST", path)
		}
	}
}

// TestRatesActionsRefuseAStolenSessionWithABadToken proves the guard is not
// satisfied by a session cookie alone. Without this, a page on the guest network
// that can read no cookie but can still make the browser send one would be able
// to reprice a coin.
func TestRatesActionsRefuseAStolenSessionWithABadToken(t *testing.T) {
	base, db := newCaptiveE2E(t, ratesTestConfig())
	client := ratesClient(t, base, db)

	resp, err := client.PostForm(base+"/admin/rates/save", url.Values{
		"csrf_token": {"not-the-real-token"},
		"pulses":     {"1"},
		"amount":     {"5.00"},
		"minutes":    {"15"},
		"active":     {"1"},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a bad CSRF token got %d, want 403", resp.StatusCode)
	}

	rates, err := db.Rates().List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rates) != 0 {
		t.Error("a request with a bad CSRF token still created a rate")
	}
}

// TestCoinPulseUsesTheConfiguredRate is the integration point that matters
// most: the price an operator typed into the RATES form must be the price a
// customer's coin is actually bought at.
//
// The tier prices a pulse at an hour while the environment fallback would price
// it at five minutes, so a report that ignores the rates table cannot pass.
func TestCoinPulseUsesTheConfiguredRate(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())

	if _, err := db.Rates().Create(ctx, database.Rate{
		Label: "hourly", Pulses: 1, AmountCents: 2000, Hours: 1, Active: true,
	}); err != nil {
		t.Fatalf("create rate: %v", err)
	}

	status, balance := postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1, "event_id": "rate-1", "node_id": "box-1",
	})
	if status != http.StatusOK {
		t.Fatalf("pulse got status %d, want 200", status)
	}
	if balance.RemainingSeconds != 3600 {
		t.Errorf("remaining = %d seconds, want 3600 (the configured tier)", balance.RemainingSeconds)
	}
	if balance.AmountCents != 2000 {
		t.Errorf("amount = %d cents, want 2000 (the configured tier)", balance.AmountCents)
	}
	// The node's own view of the rate, echoed on every answer, must agree, or a
	// node with a display would tell the customer a different number.
	if balance.SecondsPerPulse != 3600 {
		t.Errorf("SecondsPerPulse = %d, want 3600", balance.SecondsPerPulse)
	}
}

// TestCoinPulsePricesMultiCoinBursts proves a rapid multi-coin burst is priced
// by the tiers rather than by a flat per-pulse number: six pulses with a 4-pulse
// bulk tier and a 1-pulse single tier is one bulk coin plus two singles.
func TestCoinPulsePricesMultiCoinBursts(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, ratesTestConfig())

	if _, err := db.Rates().Create(ctx, database.Rate{
		Label: "single", Pulses: 1, AmountCents: 500, Minutes: 15, Active: true,
	}); err != nil {
		t.Fatalf("create single: %v", err)
	}
	if _, err := db.Rates().Create(ctx, database.Rate{
		Label: "bulk", Pulses: 4, AmountCents: 2000, Hours: 1, Active: true,
	}); err != nil {
		t.Fatalf("create bulk: %v", err)
	}

	status, balance := postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 6, "event_id": "burst-1", "node_id": "box-1",
	})
	if status != http.StatusOK {
		t.Fatalf("pulse got status %d, want 200", status)
	}
	if want := 3600 + 2*900; balance.RemainingSeconds != want {
		t.Errorf("remaining = %d seconds, want %d (one bulk + two singles)",
			balance.RemainingSeconds, want)
	}
	if want := int64(2000 + 2*500); balance.AmountCents != want {
		t.Errorf("amount = %d cents, want %d", balance.AmountCents, want)
	}
}

// TestCoinPulseFallsBackWhenNoRateConfigured proves the upgrade path: an install
// that has never opened the RATES page keeps pricing coins from the environment,
// so adding this feature does not change the price of a coin on a machine that
// was working yesterday.
func TestCoinPulseFallsBackWhenNoRateConfigured(t *testing.T) {
	base, _ := newCaptiveE2E(t, ratesTestConfig())

	status, balance := postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1, "event_id": "fallback-1", "node_id": "box-1",
	})
	if status != http.StatusOK {
		t.Fatalf("pulse got status %d, want 200", status)
	}
	if balance.RemainingSeconds != 300 {
		t.Errorf("remaining = %d, want the 300s environment fallback", balance.RemainingSeconds)
	}
	if balance.AmountCents != 500 {
		t.Errorf("amount = %d cents, want the 500 environment fallback", balance.AmountCents)
	}
}
