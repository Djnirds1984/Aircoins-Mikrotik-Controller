package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// ratesTestConfig is the coin configuration the RATES page tests run against.
// The fallback is deliberately different from what any test tier prices, so a
// tier that is silently ignored shows up as the wrong number rather than as a
// coincidence.
func ratesTestConfig() Config {
	return Config{
		CoinNodeToken:         coinTestToken,
		CoinPulseSeconds:      300,
		CoinPulseCents:        500,
		CoinIdleTTL:           20 * time.Minute,
		CoinMaxSessionMinutes: 240,
	}
}

// ratesClient returns a client holding a valid panel session, so a test can post
// to an /admin route without walking the login form each time.
func ratesClient(t *testing.T, base string, db *database.DB) *http.Client {
	t.Helper()
	return authedClientFor(t, base, db)
}

// itoa keeps the form posts below readable.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// TestRatesPageRequiresAPanelSession is the first thing to check about a page
// that sets the price of a coin: it must not be reachable by a guest standing
// at the machine, who is by definition on the network the controller serves.
func TestRatesPageRequiresAPanelSession(t *testing.T) {
	base, _ := newUnauthE2E(t, ratesTestConfig())

	resp, err := http.Get(base + "/admin/rates")
	if err != nil {
		t.Fatalf("GET /admin/rates: %v", err)
	}
	defer resp.Body.Close()
	// The guard answers with a 303 to the sign-in form, which http.Get follows,
	// so the final status is the login page's 200. The assertion that matters is
	// therefore the URL it ended on: a 200 here would mean the pricing page was
	// actually served to an anonymous caller.
	if strings.Contains(resp.Request.URL.Path, "/rates") {
		t.Errorf("an anonymous request reached %q: the rates page was served to a guest",
			resp.Request.URL.Path)
	}
	if !strings.Contains(resp.Request.URL.Path, "/login") {
		t.Errorf("an anonymous request landed on %q, want the sign-in form", resp.Request.URL.Path)
	}
}

// TestRatesPageRendersStructuredTimeDropdowns is the requirement the form exists
// for: the operator picks a session length from Days, Hours and Minutes rather
// than computing a minute count by hand.
func TestRatesPageRendersStructuredTimeDropdowns(t *testing.T) {
	base, db := newCaptiveE2E(t, ratesTestConfig())
	body := getBody(t, ratesClient(t, base, db), base+"/admin/rates")

	for _, want := range []string{
		`name="days"`, `name="hours"`, `name="minutes"`,
		`name="amount"`, `name="pulses"`,
		// The option lists have to actually carry the documented bounds, or the
		// dropdown silently offers fewer values than the store accepts.
		`<option value="30"`,
		`<option value="23"`,
		`<option value="59"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rates page is missing %q", want)
		}
	}
	// This install does have a node token, so the unconfigured warning must NOT
	// appear - telling an operator their working coin box is disconnected is
	// its own kind of wrong.
	if strings.Contains(body, "COIN_NODE_TOKEN") {
		t.Error("the page warns about an unconfigured node on a configured install")
	}
}

// TestRatesPageWarnsWhenNoNodeConfigured covers the other half of that warning.
// An operator looking at an empty rate table needs to know whether the coin box
// is broken or simply never configured, and "0.00" answers neither.
func TestRatesPageWarnsWhenNoNodeConfigured(t *testing.T) {
	cfg := ratesTestConfig()
	cfg.CoinNodeToken = ""
	base, db := newCaptiveE2E(t, cfg)

	body := getBody(t, ratesClient(t, base, db), base+"/admin/rates")
	if !strings.Contains(body, "COIN_NODE_TOKEN") {
		t.Error("the page does not warn that no coin node is configured")
	}
}
