package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// seedDeviceRouter inserts a router the device rows can point at, since the
// devices table references routers(id) with ON DELETE CASCADE.
func seedDeviceRouter(t *testing.T, db *DB, name string) Router {
	t.Helper()
	router, err := db.Routers().Create(context.Background(), Router{
		Name: name, Host: "192.168.88.1", Port: 8728, Username: "api", Password: "pw",
	})
	if err != nil {
		t.Fatalf("seed router %q: %v", name, err)
	}
	return router
}

// TestDeviceCreateNormalizesMAC pins the storage contract: whatever spelling an
// operator pastes, the MAC is stored normalised so FindByMAC and the unique
// (router_id, mac) index behave, while MACAddress renders it back for humans.
func TestDeviceCreateNormalizesMAC(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")

	created, err := db.Devices().Create(ctx, Device{
		RouterID: router.ID, MAC: "AA-BB-CC-DD-EE-FF", Name: "  Front desk  ", Notes: " tablet ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create returned a device with no id")
	}
	if created.MAC != "aabbccddeeff" {
		t.Errorf("MAC = %q, want normalised aabbccddeeff", created.MAC)
	}
	if created.MACAddress() != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("MACAddress() = %q, want AA:BB:CC:DD:EE:FF", created.MACAddress())
	}
	if created.Name != "Front desk" || created.Notes != "tablet" {
		t.Errorf("name/notes were not trimmed: %q / %q", created.Name, created.Notes)
	}
	if created.RouterName != "Cafe" {
		t.Errorf("RouterName = %q, want the joined router name", created.RouterName)
	}

	// The same MAC in a different spelling must collide, not duplicate.
	if _, err := db.Devices().Create(ctx, Device{RouterID: router.ID, MAC: "aa:bb:cc:dd:ee:ff"}); err == nil {
		t.Error("duplicate MAC on the same router was accepted, want a rejection")
	} else if !strings.Contains(err.Error(), "already saved") {
		t.Errorf("duplicate error = %q, want a friendly 'already saved' message", err)
	}
}

// TestDeviceCreateValidation proves the store rejects an incomplete identity
// rather than writing a row the DEVICES page could never match to live data.
func TestDeviceCreateValidation(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")

	if _, err := db.Devices().Create(ctx, Device{RouterID: router.ID}); !errors.Is(err, ErrDeviceInvalid) {
		t.Errorf("Create without MAC err = %v, want ErrDeviceInvalid", err)
	}
	if _, err := db.Devices().Create(ctx, Device{MAC: "AA:BB:CC:DD:EE:FF"}); !errors.Is(err, ErrDeviceInvalid) {
		t.Errorf("Create without router err = %v, want ErrDeviceInvalid", err)
	}
}

// TestDeviceFindByMAC covers the lookup the handler uses to decide whether a
// discovered client is already in the inventory.
func TestDeviceFindByMAC(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")
	store := db.Devices()

	if _, err := store.Create(ctx, Device{RouterID: router.ID, MAC: "AA:BB:CC:DD:EE:FF", Name: "Kiosk"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.FindByMAC(ctx, router.ID, "aa-bb-cc-dd-ee-ff")
	if err != nil {
		t.Fatalf("FindByMAC: %v", err)
	}
	if got.Name != "Kiosk" {
		t.Errorf("FindByMAC name = %q, want Kiosk", got.Name)
	}
	if _, err := store.FindByMAC(ctx, router.ID, "00:00:00:00:00:00"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByMAC unknown err = %v, want ErrNotFound", err)
	}
	if _, err := store.FindByMAC(ctx, router.ID, "not-a-mac"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByMAC garbage err = %v, want ErrNotFound", err)
	}
}

// TestDeviceUpdateAndDelete walks the full CRUD lifecycle the admin page
// drives: edit the mutable fields, then remove the record and confirm a second
// delete reports ErrNotFound instead of silently succeeding.
func TestDeviceUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")
	store := db.Devices()

	created, err := store.Create(ctx, Device{RouterID: router.ID, MAC: "AA:BB:CC:DD:EE:FF", Name: "Old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := store.Update(ctx, Device{
		ID: created.ID, RouterID: router.ID, MAC: "AA:BB:CC:DD:EE:FF", Name: "New", Notes: "renamed",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "New" || updated.Notes != "renamed" {
		t.Errorf("Update did not persist name/notes: %q / %q", updated.Name, updated.Notes)
	}

	if err := store.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
}

// TestDeviceUpdateMissingRecord proves editing an id that is not there is an
// error, so a stale form POST cannot pretend to succeed.
func TestDeviceUpdateMissingRecord(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")

	_, err := db.Devices().Update(ctx, Device{ID: 9999, RouterID: router.ID, MAC: "AA:BB:CC:DD:EE:FF"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing err = %v, want ErrNotFound", err)
	}
}

// TestDeviceListAndCount checks the inventory listing resolves router names and
// the count the DEVICES page header shows.
func TestDeviceListAndCount(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")
	store := db.Devices()

	for _, mac := range []string{"AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"} {
		if _, err := store.Create(ctx, Device{RouterID: router.ID, MAC: mac}); err != nil {
			t.Fatalf("Create %s: %v", mac, err)
		}
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d devices, want 2", len(list))
	}
	for _, d := range list {
		if d.RouterName != "Cafe" {
			t.Errorf("device %d RouterName = %q, want Cafe", d.ID, d.RouterName)
		}
	}
	n, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
}

// TestDeviceCascadeOnRouterDelete proves the ON DELETE CASCADE wiring: removing
// a router must not leave orphan device rows pointing at a missing id.
func TestDeviceCascadeOnRouterDelete(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	router := seedDeviceRouter(t, db, "Cafe")
	store := db.Devices()

	if _, err := store.Create(ctx, Device{RouterID: router.ID, MAC: "AA:BB:CC:DD:EE:FF"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Routers().Delete(ctx, router.ID); err != nil {
		t.Fatalf("delete router: %v", err)
	}
	n, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Errorf("devices left after router delete = %d, want 0 (cascade)", n)
	}
}
