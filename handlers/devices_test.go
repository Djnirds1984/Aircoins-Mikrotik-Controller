package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// devicesTestConfig keeps the router dial cheap: the fixture router points at a
// closed loopback port, so a probe is refused immediately instead of blocking
// the test for the full API timeout.
func devicesTestConfig() Config {
	return Config{APITimeout: 2 * time.Second}
}

// seedDeviceRouter registers a router the DEVICES page can enumerate. It is
// deliberately unreachable (binary API on a closed loopback port) so the page
// exercises its "router offline, still show saved devices" path.
func seedDeviceRouter(t *testing.T, db *database.DB, name string) database.Router {
	t.Helper()
	router, err := db.Routers().Create(context.Background(), database.Router{
		Name: name, Host: "127.0.0.1", Port: 1, Username: "api", Password: "pw",
		Transport: database.TransportAPI,
	})
	if err != nil {
		t.Fatalf("seed router %q: %v", name, err)
	}
	return router
}

// TestDevicesPageRendersSavedDeviceDespiteOfflineRouter is the core promise of
// the page: the operator's inventory is read from local storage, so a device
// that was saved stays on screen (and a dial failure is reported as a warning)
// even when its router cannot be reached right now.
func TestDevicesPageRendersSavedDeviceDespiteOfflineRouter(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, devicesTestConfig())
	client := authedClientFor(t, base, db)
	router := seedDeviceRouter(t, db, "Cafe")

	if _, err := db.Devices().Create(ctx, database.Device{
		RouterID: router.ID, MAC: "AA:BB:CC:DD:EE:FF", Name: "Front desk", Notes: "tablet",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	page := getBody(t, client, base+"/admin/devices")
	for _, want := range []string{
		"Devices",                // the page heading
		"Front desk",             // the saved name renders
		"AA:BB:CC:DD:EE:FF",      // the formatted MAC renders
		"Some routers could not", // the offline warning banner is shown
		`name="csrf_token"`,      // the add form carries a CSRF token
	} {
		if !strings.Contains(page, want) {
			t.Errorf("devices page is missing %q", want)
		}
	}
}

// TestDeviceCreateUpdateDelete walks the whole CRUD lifecycle through the real
// forms and asserts the row that lands in SQLite at each step, which is what
// the page reads back on the next load.
func TestDeviceCreateUpdateDelete(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, devicesTestConfig())
	client := authedClientFor(t, base, db)
	router := seedDeviceRouter(t, db, "Cafe")

	// --- Create -----------------------------------------------------------
	page := getBody(t, client, base+"/admin/devices")
	resp, err := client.PostForm(base+"/admin/devices", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"router_id":  {itoa(router.ID)},
		"mac":        {"aa-bb-cc-dd-ee-ff"},
		"name":       {"Kiosk"},
		"notes":      {"lobby"},
	})
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create ended on status %d, want 200 after the redirect", resp.StatusCode)
	}
	created, err := db.Devices().FindByMAC(ctx, router.ID, "AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("device was not stored: %v", err)
	}
	if created.Name != "Kiosk" || created.Notes != "lobby" {
		t.Errorf("stored name/notes = %q / %q, want Kiosk / lobby", created.Name, created.Notes)
	}

	// --- Update -----------------------------------------------------------
	page = getBody(t, client, base+"/admin/devices")
	resp, err = client.PostForm(base+"/admin/devices/"+itoa(created.ID), url.Values{
		"csrf_token": {csrfOf(t, page)},
		"router_id":  {itoa(router.ID)},
		"mac":        {"AA:BB:CC:DD:EE:FF"},
		"name":       {"Renamed"},
		"notes":      {"moved"},
	})
	if err != nil {
		t.Fatalf("POST update: %v", err)
	}
	resp.Body.Close()
	updated, err := db.Devices().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if updated.Name != "Renamed" || updated.Notes != "moved" {
		t.Errorf("after update name/notes = %q / %q, want Renamed / moved", updated.Name, updated.Notes)
	}

	// --- Delete -----------------------------------------------------------
	page = getBody(t, client, base+"/admin/devices")
	resp, err = client.PostForm(base+"/admin/devices/"+itoa(created.ID)+"/delete", url.Values{
		"csrf_token": {csrfOf(t, page)},
	})
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	resp.Body.Close()
	if _, err := db.Devices().Get(ctx, created.ID); err == nil {
		t.Error("the device survived the delete")
	}
}

// TestDeviceCreateRejectsMissingFields proves the form guard runs server-side:
// a POST with no MAC is refused with a 422 re-render rather than writing a row
// the page could never match to live router data. The MAC check matches the
// controller-wide convention (see ClientBlock): NormalizeMAC is deliberately
// lenient, so the guard rejects a blank address.
func TestDeviceCreateRejectsMissingFields(t *testing.T) {
	base, db := newCaptiveE2E(t, devicesTestConfig())
	client := authedClientFor(t, base, db)
	router := seedDeviceRouter(t, db, "Cafe")

	page := getBody(t, client, base+"/admin/devices")
	resp, err := client.PostForm(base+"/admin/devices", url.Values{
		"csrf_token": {csrfOf(t, page)},
		"router_id":  {itoa(router.ID)},
		"mac":        {""},
		"name":       {"Broken"},
	})
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an invalid MAC got status %d, want 422", resp.StatusCode)
	}
	n, err := db.Devices().Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("an invalid device was stored anyway: %d rows", n)
	}
}

// TestDeviceMutationsRequireCSRF proves the write routes are guarded like the
// rest of the panel: a POST without a valid token is refused and stores nothing.
func TestDeviceMutationsRequireCSRF(t *testing.T) {
	base, db := newCaptiveE2E(t, devicesTestConfig())
	client := authedClientFor(t, base, db)
	router := seedDeviceRouter(t, db, "Cafe")

	resp, err := client.PostForm(base+"/admin/devices", url.Values{
		"csrf_token": {"not-the-real-token"},
		"router_id":  {itoa(router.ID)},
		"mac":        {"AA:BB:CC:DD:EE:FF"},
	})
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a bad CSRF token got %d, want 403", resp.StatusCode)
	}
	n, err := db.Devices().Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("a request with a bad CSRF token still created a device")
	}
}
