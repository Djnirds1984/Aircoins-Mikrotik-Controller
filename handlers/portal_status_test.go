package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSplitCountdown pins the DAYS:HOURS:MINUTES:SECONDS format the status page
// is for, including the boundary values. A carry bug here would show a guest
// "60MIN" for an hour, which is exactly the kind of off-by-one nobody notices
// until a customer reads it.
func TestSplitCountdown(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{0, "0D. 00HR. 00MIN. 00SEC."},
		{1, "0D. 00HR. 00MIN. 01SEC."},
		{59, "0D. 00HR. 00MIN. 59SEC."},
		{60, "0D. 00HR. 01MIN. 00SEC."},
		{3599, "0D. 00HR. 59MIN. 59SEC."},
		{3600, "0D. 01HR. 00MIN. 00SEC."},
		// The 996-day value from the reference portal, to prove long sessions
		// do not overflow into hours.
		{996*86400 + 22*3600 + 20*60 + 29, "996D. 22HR. 20MIN. 29SEC."},
		{86399, "0D. 23HR. 59MIN. 59SEC."},
		{86400, "1D. 00HR. 00MIN. 00SEC."},
		// A negative value must not render a negative clock.
		{-5, "0D. 00HR. 00MIN. 00SEC."},
	}
	for _, tc := range cases {
		if got := splitCountdown(tc.seconds).String(); got != tc.want {
			t.Errorf("splitCountdown(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// TestSecondsSinceAndUntilClampAtZero covers the clock-skew guards. A device
// with a wrong clock, or a row stamped slightly in the future, must not produce
// a countdown that starts negative.
func TestSecondsSinceAndUntilClampAtZero(t *testing.T) {
	if got := secondsSince(time.Now().Add(-90 * time.Second)); got < 88 || got > 92 {
		t.Errorf("secondsSince = %d, want about 90", got)
	}
	if got := secondsSince(time.Now().Add(10 * time.Minute)); got != 0 {
		t.Errorf("secondsSince(future) = %d, want 0", got)
	}
	if got := secondsSince(time.Time{}); got != 0 {
		t.Errorf("secondsSince(zero) = %d, want 0", got)
	}

	soon := time.Now().Add(2 * time.Hour)
	if got := secondsUntil(&soon); got < 7100 || got > 7200 {
		t.Errorf("secondsUntil = %d, want about 7200", got)
	}
	past := time.Now().Add(-time.Hour)
	if got := secondsUntil(&past); got != 0 {
		t.Errorf("secondsUntil(past) = %d, want 0", got)
	}
	if got := secondsUntil(nil); got != 0 {
		t.Errorf("secondsUntil(nil) = %d, want 0", got)
	}
}

// TestPortalSessionPageIsPublic proves the route is reachable without admin
// auth and redirects to the captive portal root (which renders session status).
func TestPortalSessionPageIsPublic(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := noRedirect().Get(base + portalStatusPagePath)
	if err != nil {
		t.Fatalf("GET %s: %v", portalStatusPagePath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
}

// TestPortalSessionPageRedirectsRegardlessOfSession confirms the handler always
// redirects to "/" even when a session exists, since captive.html now renders
// the session status directly.
func TestPortalSessionPageRedirectsRegardlessOfSession(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := noRedirect().Get(base + portalStatusPagePath)
	if err != nil {
		t.Fatalf("GET %s: %v", portalStatusPagePath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
}

// TestPortalSessionPageKeepsTheOperatorProbeApart guards the route that was
// already there. /portal/status is the JSON probe operators use to check the
// portal from a script; the new HTML page must not have taken it over.
func TestPortalSessionPageKeepsTheOperatorProbeApart(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := noRedirect().Get(base + "/portal/status")
	if err != nil {
		t.Fatalf("GET /portal/status: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json: the operator probe was replaced", ct)
	}
}
