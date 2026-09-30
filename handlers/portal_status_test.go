package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// statusBody trims a rendered page down to the part a failure message can
// actually show, so the assertion output is readable instead of being a wall of
// inlined CSS.
func statusBody(body string) string {
	idx := strings.Index(body, "<body")
	if idx < 0 {
		return body
	}
	return body[idx:]
}

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

// TestPortalSessionPageIsPublic proves the page reaches a customer who has not
// signed in yet, and does not leak the operator panel.
func TestPortalSessionPageIsPublic(t *testing.T) {
	base, _ := newCaptiveE2E(t, Config{})

	resp, err := noRedirect().Get(base + portalStatusPagePath)
	if err != nil {
		t.Fatalf("GET %s: %v", portalStatusPagePath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (not a redirect to the panel)", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if strings.Contains(body, "Operator name") {
		t.Error("the session page served the panel login form")
	}
	// With no session the clock must still render, in the requested format.
	if !strings.Contains(body, "0D. 00HR. 00MIN. 00SEC.") {
		t.Errorf("the idle page does not show the DAYS:HOURS:MINUTES:SECONDS clock: %s", body)
	}
}

// TestPortalSessionPageShowsTheRunningClock is the main case: a guest with an
// open session sees their own elapsed time in the four-field format, and the
// browser is given the seconds so it can keep ticking.
func TestPortalSessionPageShowsTheRunningClock(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, Config{})

	// A session that started two days, three hours, four minutes and five
	// seconds ago. It needs a real router row: the session table references
	// routers(id), and that constraint is why a session can never be attributed
	// to a device that is not registered.
	router, err := db.Routers().Create(ctx, database.Router{
		Name: "Lab", Host: "192.0.2.1", Port: 8728, Username: "api", Password: "pw",
	})
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	// time.Duration is nanoseconds, so the seconds are converted explicitly.
	// Written as a bare constant expression this would silently become
	// -183845ns and the session would start "now", making the clock assertion
	// pass or fail for the wrong reason.
	started := time.Now().Add(-time.Duration(2*86400+3*3600+4*60+5) * time.Second)
	if err := db.Sessions().Register(ctx, database.Session{
		RouterID: router.ID, SessionKey: "k1", Username: "AIR-2X4Q-9BNM",
		Address: "127.0.0.1", MACAddress: "AA:BB:CC:DD:EE:FF", Server: "hotspot1",
		// StartedAt is explicit: Register only falls back to its "at" argument
		// when this is zero, and a session that silently started now would make
		// the clock assertion pass for the wrong reason.
		StartedAt: started,
	}, time.Now()); err != nil {
		t.Fatalf("register session: %v", err)
	}
	// Confirm the row is what the page will look for, so a failure below is
	// about the handler rather than about the fixture.
	found, err := db.Sessions().List(ctx, database.SessionFilter{Status: "open", Query: "127.0.0.1", Limit: 10})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(found) != 1 || found[0].Address != "127.0.0.1" {
		t.Fatalf("fixture is not visible to the portal: %+v", found)
	}
	if got := found[0].StartedAt; got.After(started.Add(time.Minute)) {
		t.Fatalf("the store did not keep the start time: got %s, want about %s", got, started)
	}

	body := getBody(t, &http.Client{}, base+portalStatusPagePath)
	if strings.Contains(body, "No active session") {
		t.Errorf("the page took the offline branch although the session exists:\n%s", statusBody(body))
	}
	if !strings.Contains(body, "2D. 03HR. 04MIN.") {
		t.Errorf("the page does not show the elapsed clock in D/H/M form:\n%s", statusBody(body))
	}
	// The script needs the seconds to continue from.
	if !strings.Contains(body, "id=\"elapsed\"") {
		t.Error("the elapsed clock element is missing, so nothing can tick it")
	}
	if !strings.Contains(body, "data-seconds=") {
		t.Error("the page does not pass the seconds to the browser")
	}
	// The MAC is stored bare, so the page has to restore the colon form; a guest
	// should not be shown "aabbccddeeff".
	if !strings.Contains(body, "AA:BB:CC:DD:EE:FF") {
		t.Errorf("the page does not show the MAC in its canonical form:\n%s", statusBody(body))
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
