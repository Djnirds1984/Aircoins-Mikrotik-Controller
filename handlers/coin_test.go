package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// coinTestToken is the shared secret the tests present as a coin node.
const coinTestToken = "test-node-secret-1234"

// coinTestConfig is the configuration the coin tests run against: 300 seconds
// per pulse, so two pulses buy ten minutes and the arithmetic in the assertions
// stays obvious.
func coinTestConfig() Config {
	return Config{
		CoinNodeToken:         coinTestToken,
		CoinPulseSeconds:      300,
		CoinPulseCents:        500,
		CoinIdleTTL:           20 * time.Minute,
		CoinMaxSessionMinutes: 240,
	}
}

// postPulse sends a hardware report the way the NodeMCU does.
func postPulse(t *testing.T, base, token string, body map[string]any) (int, coinBalance) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal pulse: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+coinPulsePath, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(coinTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", coinPulsePath, err)
	}
	defer resp.Body.Close()

	var balance coinBalance
	_ = json.NewDecoder(resp.Body).Decode(&balance)
	return resp.StatusCode, balance
}

// getBalance reads a client's balance the way the portal's poller does.
func getBalance(t *testing.T, base, subject string) (int, coinBalance) {
	t.Helper()
	resp, err := http.Get(base + coinStatusPath + "?subject=" + url.QueryEscape(subject))
	if err != nil {
		t.Fatalf("GET %s: %v", coinStatusPath, err)
	}
	defer resp.Body.Close()

	var balance coinBalance
	_ = json.NewDecoder(resp.Body).Decode(&balance)
	return resp.StatusCode, balance
}

// truncateForTest keeps an assertion failure readable.
func truncateForTest(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// TestCoinPulseNeedsTheNodeToken is the most important test in this file: the
// endpoint is reachable from every guest's phone, so an unauthenticated write
// would be a free-internet button.
func TestCoinPulseNeedsTheNodeToken(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	for _, token := range []string{"", "wrong-token", coinTestToken + "x"} {
		status, _ := postPulse(t, base, token, map[string]any{
			"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1, "event_id": "e-" + token,
		})
		if status != http.StatusUnauthorized {
			t.Errorf("pulse with token %q got status %d, want 401", token, status)
		}
	}
}

// TestCoinPulseRefusedWithoutConfiguredToken proves the safe default: a
// controller that was never given a node token rejects hardware reports
// outright rather than accepting them from anybody.
func TestCoinPulseRefusedWithoutConfiguredToken(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{CoinPulseSeconds: 300})

	status, _ := postPulse(t, base, "anything", map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1,
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("unconfigured controller got status %d, want 401", status)
	}
}

// TestCoinPulseAccumulates walks the happy path: two reports for the same client
// add up, the portal's poller sees the total, and both the money and the time
// move.
func TestCoinPulseAccumulates(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	subject := "mac:aabbccddeeff"
	if status, first := postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1, "event_id": "pulse-1", "node_id": "box-1",
	}); status != http.StatusOK {
		t.Fatalf("first pulse got status %d, want 200", status)
	} else if first.RemainingSeconds != 300 {
		t.Errorf("after 1 pulse remaining = %d, want 300", first.RemainingSeconds)
	} else if first.AmountCents != 500 {
		t.Errorf("after 1 pulse amount = %d cents, want 500", first.AmountCents)
	}

	status, second := postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1, "event_id": "pulse-2", "node_id": "box-1",
	})
	if status != http.StatusOK {
		t.Fatalf("second pulse got status %d, want 200", status)
	}
	if second.RemainingSeconds != 600 {
		t.Errorf("after 2 pulses remaining = %d, want 600", second.RemainingSeconds)
	}
	if second.SessionLabel != "10 min" {
		t.Errorf("session label = %q, want %q", second.SessionLabel, "10 min")
	}

	// The portal polls the same row.
	status, polled := getBalance(t, base, subject)
	if status != http.StatusOK {
		t.Fatalf("status poll got %d, want 200", status)
	}
	if polled.RemainingSeconds != 600 {
		t.Errorf("polled remaining = %d, want 600", polled.RemainingSeconds)
	}
	if polled.Pulses != 2 {
		t.Errorf("polled pulses = %d, want 2", polled.Pulses)
	}
}

// TestCoinPulseRetryIsNotDoubleCounted is the property that makes a flaky Wi-Fi
// link survivable. A node that posts, loses the response and retries must not
// charge the customer for the same coin twice.
func TestCoinPulseRetryIsNotDoubleCounted(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	report := map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1, "event_id": "retry-me", "node_id": "box-1",
	}
	if status, _ := postPulse(t, base, coinTestToken, report); status != http.StatusOK {
		t.Fatalf("first attempt got %d, want 200", status)
	}

	// The retry must be answered as a SUCCESS (so the sketch stops retrying) but
	// must not change the balance.
	status, retry := postPulse(t, base, coinTestToken, report)
	if status != http.StatusOK {
		t.Fatalf("retry got status %d, want 200 (the node did nothing wrong)", status)
	}
	if !retry.Duplicate {
		t.Error("retry was not flagged as a duplicate, so the sketch would keep retrying")
	}
	if retry.RemainingSeconds != 300 {
		t.Errorf("after retry remaining = %d, want 300 (the coin was counted once)", retry.RemainingSeconds)
	}
	if retry.Pulses != 1 {
		t.Errorf("after retry pulses = %d, want 1", retry.Pulses)
	}
}

// TestCoinStatusUnknownClientIsZero proves the "never inserted anything" case is
// a normal answer rather than a 404, because that is the state the tab renders
// before the customer's first coin.
func TestCoinStatusUnknownClientIsZero(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	status, balance := getBalance(t, base, "mac:001122334455")
	if status != http.StatusOK {
		t.Fatalf("unknown client got %d, want 200", status)
	}
	if balance.RemainingSeconds != 0 {
		t.Errorf("unknown client remaining = %d, want 0", balance.RemainingSeconds)
	}
	if balance.SessionLabel != "0m" {
		t.Errorf("unknown client label = %q, want %q", balance.SessionLabel, "0m")
	}
}

// TestCoinStatusAcceptsAMAC proves the poller can look a client up the way the
// page does, with a plain MAC rather than a storage key.
func TestCoinStatusAcceptsAMAC(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 2, "event_id": "mac-lookup",
	})

	resp, err := http.Get(base + coinStatusPath + "?mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer resp.Body.Close()

	var balance coinBalance
	if err := json.NewDecoder(resp.Body).Decode(&balance); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if balance.RemainingSeconds != 600 {
		t.Errorf("remaining = %d, want 600", balance.RemainingSeconds)
	}
	if balance.MAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("mac = %q, want the canonical AA:BB:CC:DD:EE:FF form", balance.MAC)
	}
}

// TestCoinPulseRejectsGarbage proves a misconfigured acceptor produces a 400
// naming the problem, which is what makes it diagnosable from the node's serial
// log without a laptop on site.
func TestCoinPulseRejectsGarbage(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	// A body with no client to attribute the money to.
	status, _ := postPulse(t, base, coinTestToken, map[string]any{
		"pulses": 1, "event_id": "orphan",
	})
	if status != http.StatusBadRequest {
		t.Errorf("orphan pulse got %d, want 400", status)
	}

	// Not JSON at all.
	req, err := http.NewRequest(http.MethodPost, base+coinPulsePath, strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(coinTokenHeader, coinTestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body got %d, want 400", resp.StatusCode)
	}
}

// TestCoinConnectRefusesAnEmptyBalance is the anti-fraud case: pressing the
// button without paying must not produce a session, and must say why.
func TestCoinConnectRefusesAnEmptyBalance(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	resp, err := http.PostForm(base+coinConnectPath, url.Values{
		"mac": {"AA:BB:CC:DD:EE:FF"},
		"ip":  {"10.0.0.9"},
	})
	if err != nil {
		t.Fatalf("POST %s: %v", coinConnectPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty connect got %d, want 200 with an explanation", resp.StatusCode)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "No coins have been credited") {
		t.Errorf("empty connect did not explain itself; body was:\n%s", truncateForTest(body))
	}
}

// TestCoinConnectKeepsTheBalanceWhenTheRouterIsDown proves the ordering that
// makes the flow safe: the money is only deducted after the device authorises
// the client, so a failed attempt leaves the customer able to retry.
func TestCoinConnectKeepsTheBalanceWhenTheRouterIsDown(t *testing.T) {
	base, db := newCaptiveE2E(t, coinTestConfig())
	ctx := context.Background()

	postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 2, "event_id": "offline-router",
	})

	// No router is registered, so the redemption cannot happen.
	resp, err := http.PostForm(base+coinConnectPath, url.Values{
		"mac": {"AA:BB:CC:DD:EE:FF"},
		"ip":  {"10.0.0.9"},
	})
	if err != nil {
		t.Fatalf("POST %s: %v", coinConnectPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("connect redirected as if it had succeeded, but no router is registered")
	}

	// The whole balance must still be there.
	credit, err := db.Coins().Get(ctx, "mac:aabbccddeeff")
	if err != nil {
		t.Fatalf("read credit: %v", err)
	}
	if credit.UsedSeconds != 0 {
		t.Errorf("used_seconds = %d, want 0: a failed connection must not take the money", credit.UsedSeconds)
	}
	if credit.RemainingSeconds() != 600 {
		t.Errorf("remaining = %d, want the full 600 to still be there", credit.RemainingSeconds())
	}
}

// TestCoinTabRendersOnThePortal proves the tab is actually on the page, carries
// the poller's configuration, and offers the action button.
func TestCoinTabRendersOnThePortal(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	resp, err := http.Get(base + "/?mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF&ip=10.0.0.9")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	for _, want := range []string{
		`id="coin-tab"`,
		`data-subject="mac:aabbccddeeff"`,
		`data-status-url="` + coinStatusPath + `"`,
		"Insert coin",
		"Done &mdash; Connect now",
		// The action must keep the hotspot parameters, or the session would be
		// created against the wrong host.
		coinConnectPath + "?",
		"mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("portal page is missing %q", want)
		}
	}
	// The poller must be wired to the status endpoint, otherwise the counter
	// would sit on its server-rendered value forever.
	if !strings.Contains(body, "'?subject='") {
		t.Error("portal page is missing the poller script")
	}
}

// TestCoinTabExplainsAnUnconfiguredSlot proves the tab degrades honestly instead
// of sitting at zero forever with no explanation.
func TestCoinTabExplainsAnUnconfiguredSlot(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := http.Get(base + "/?mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if !strings.Contains(body, "not set up on this network yet") {
		t.Error("an unconfigured coin slot should tell the guest so")
	}
	// The action button must not be offered when it cannot work.
	if strings.Contains(body, "Done &mdash; Connect now") {
		t.Error("the connect button is offered even though no node token is configured")
	}
}

// TestCoinTabShowsAServerRenderedBalance proves the counter is right on first
// paint, before the first poll ever runs. A customer who reloads after paying
// must not watch the number sit on zero.
func TestCoinTabShowsAServerRenderedBalance(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())
	postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 3, "event_id": "server-render",
	})

	resp, err := http.Get(base + "/?mac=AA%3ABB%3ACC%3ADD%3AEE%3AFF&ip=10.0.0.9")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if !strings.Contains(body, `data-seconds="900"`) {
		t.Error("the coin counter was not rendered with the stored balance")
	}
	if !strings.Contains(body, ">15 min<") {
		t.Error(`the coin counter should show "15 min" for three 5-minute pulses`)
	}
}

// TestCoinSessionSecondsIsCapped proves a jammed acceptor cannot hand out an
// unbounded session. The public kiosk is the whole reason the cap exists.
func TestCoinSessionSecondsIsCapped(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())
	// A report claiming far more access time than any acceptor produces, which
	// is what a jammed input line looks like. The amount is stated explicitly so
	// the report is not rejected earlier by the money cap for the wrong reason.
	postPulse(t, base, coinTestToken, map[string]any{
		"mac": "AA:BB:CC:DD:EE:FF", "pulses": 1000,
		"seconds": 300000, "amount_cents": 1000, "event_id": "jam",
	})

	_, balance := getBalance(t, base, "mac:aabbccddeeff")
	if balance.RemainingSeconds <= 240*60 {
		t.Fatalf("test needs a balance above the cap, got %d seconds", balance.RemainingSeconds)
	}

	h := &Handler{cfg: coinTestConfig()}
	credit := database.CoinCredit{GrantedSeconds: balance.RemainingSeconds}
	if got := h.coinSessionSeconds(credit); got != 240*60 {
		t.Errorf("session seconds = %d, want the %d second cap", got, 240*60)
	}
	// A small balance is handed over in full, not rounded up to the cap.
	if got := h.coinSessionSeconds(database.CoinCredit{GrantedSeconds: 600}); got != 600 {
		t.Errorf("session seconds for a 10 minute balance = %d, want 600", got)
	}
	// A partial minute is dropped rather than provisioned as a 1 minute session.
	if got := h.coinSessionSeconds(database.CoinCredit{GrantedSeconds: 90}); got != 60 {
		t.Errorf("session seconds for 90s = %d, want 60 (whole minutes only)", got)
	}
}

// TestCoinIdleBalanceIsReleased proves the money a customer walks away from does
// not become a free session for the next person on the same address.
func TestCoinIdleBalanceIsReleased(t *testing.T) {
	_, db := newCaptiveE2E(t, coinTestConfig())
	ctx := context.Background()

	past := time.Now().Add(-2 * time.Hour)
	if _, err := db.Coins().Credit(ctx, database.CoinPulse{
		Subject: "mac:aabbccddeeff",
		Pulses:  2,
		Seconds: 600,
		IdleTTL: 20 * time.Minute,
	}, past); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	released, err := db.Coins().ExpireIdle(ctx, 20*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("expire idle: %v", err)
	}
	if released != 1 {
		t.Errorf("released %d balances, want 1", released)
	}

	credit, err := db.Coins().Get(ctx, "mac:aabbccddeeff")
	if err != nil {
		t.Fatalf("read expired credit: %v", err)
	}
	if credit.Status != database.CoinExpired {
		t.Errorf("status = %q, want %q", credit.Status, database.CoinExpired)
	}
}

// TestCoinConsumeDeductsOnlyWhatWasGranted proves the deduction can never push
// the balance negative, however the two "Connect" taps race.
func TestCoinConsumeDeductsOnlyWhatWasGranted(t *testing.T) {
	_, db := newCaptiveE2E(t, coinTestConfig())
	ctx := context.Background()

	if _, err := db.Coins().Credit(ctx, database.CoinPulse{
		Subject: "mac:aabbccddeeff", Pulses: 1, Seconds: 300,
	}, time.Now()); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	// Ask for far more than exists, twice.
	var credit database.CoinCredit
	for i := 0; i < 2; i++ {
		settled, err := db.Coins().Consume(ctx, "mac:aabbccddeeff", 10000, time.Now())
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		credit = settled
		if credit.RemainingSeconds() != 0 {
			t.Fatalf("remaining = %d after over-consuming, want 0", credit.RemainingSeconds())
		}
		if credit.UsedSeconds > credit.GrantedSeconds {
			t.Fatalf("used %d exceeds granted %d", credit.UsedSeconds, credit.GrantedSeconds)
		}
	}
	if credit.Status != database.CoinConnected {
		t.Errorf("status = %q, want %q once the balance is spent", credit.Status, database.CoinConnected)
	}
}

// TestSketchContract feeds the exact request the Arduino sketch builds through
// the real handler, proving the two sides agree on the field names, the auth
// header and the duplicate-detection contract. If someone edits the sketch's
// JSON without editing the Go struct (or the reverse), this is what catches it.
func TestSketchContract(t *testing.T) {
	base, _ := newCaptiveE2E(t, coinTestConfig())

	// Verbatim from reportPulses() in nodemcu_coin_slot.ino.
	const sketchBody = `{"mac":"AA:BB:CC:DD:EE:FF","pulses":2,"node_id":"box-1","event_id":"box-1-lu-7"}`

	post := func() (int, coinBalance) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+coinPulsePath, strings.NewReader(sketchBody))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(coinTokenHeader, coinTestToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		var balance coinBalance
		_ = json.NewDecoder(resp.Body).Decode(&balance)
		return resp.StatusCode, balance
	}

	status, first := post()
	if status != http.StatusOK {
		t.Fatalf("the sketch's request body was rejected with %d", status)
	}
	if first.RemainingSeconds != 600 {
		t.Errorf("remaining = %d, want 600 (2 pulses x 300s)", first.RemainingSeconds)
	}

	// The same event_id again: this is the "response was lost, retry" case the
	// sketch is built around, and it must not cost the customer a second coin.
	status, retry := post()
	if status != http.StatusOK {
		t.Fatalf("the retry was rejected with %d; the sketch would retry forever", status)
	}
	if !retry.Duplicate {
		t.Error("the retry was not reported as a duplicate, so the sketch would keep retrying")
	}
	if retry.RemainingSeconds != 600 {
		t.Errorf("after the retry remaining = %d, want 600 (the coin was charged once)", retry.RemainingSeconds)
	}
}
