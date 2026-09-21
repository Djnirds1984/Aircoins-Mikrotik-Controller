package database

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// testKey is a fixed 32 byte master key so tests never touch the real key file.
var testKey = base64.StdEncoding.EncodeToString([]byte("aircoins-unit-test-master-key-01"))

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), Config{
		Path:         ":memory:",
		SecretKey:    testKey,
		BusyTimeout:  2 * time.Second,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestVoucherGenerateRedeemAndExpire(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	router, err := db.Routers().Create(ctx, Router{Name: "Cafe", Host: "192.168.88.1", Port: 8728, Username: "api", Password: "pw"})
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	codes := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := GenerateVoucherCode(DefaultVoucherCodeOptions())
		if err != nil {
			t.Fatalf("GenerateVoucherCode: %v", err)
		}
		if !strings.HasPrefix(code, "AIR-") || strings.Count(code, "-") != 2 {
			t.Fatalf("unexpected code shape: %q", code)
		}
		if codes[code] {
			t.Fatalf("duplicate code generated: %q", code)
		}
		codes[code] = true
	}

	code, err := GenerateVoucherCode(DefaultVoucherCodeOptions())
	if err != nil {
		t.Fatalf("GenerateVoucherCode: %v", err)
	}
	multiCode, err := GenerateVoucherCode(DefaultVoucherCodeOptions())
	if err != nil {
		t.Fatalf("GenerateVoucherCode: %v", err)
	}
	routerID := router.ID
	created, err := db.Vouchers().CreateBatch(ctx, []Voucher{
		{
			Code: code, Batch: "BATCH-TEST", RouterID: &routerID, Profile: "1hour",
			DurationMinutes: 60, DataLimitMB: 500, DeviceLimit: 2, PriceCents: 500,
			Note: "unit test",
		},
		{
			Code: multiCode, Batch: "BATCH-TEST", RouterID: &routerID, Profile: "1hour",
			DurationMinutes: 30, DataLimitMB: 100, MaxUses: 3, PriceCents: 1000,
		},
	})
	if err != nil || created != 2 {
		t.Fatalf("CreateBatch = %d, err %v", created, err)
	}

	// A duplicate code is reported so the generator can retry.
	if _, err := db.Vouchers().CreateBatch(ctx, []Voucher{{Code: code}}); !errors.Is(err, ErrDuplicateVoucherCode) {
		t.Fatalf("duplicate insert err = %v, want ErrDuplicateVoucherCode", err)
	}

	// Lookup is dash and space insensitive.
	found, err := db.Vouchers().FindByCode(ctx, strings.ToLower(strings.ReplaceAll(code, "-", " ")))
	if err != nil {
		t.Fatalf("FindByCode: %v", err)
	}
	if found.Status != VoucherUnused || found.RouterName != "Cafe" {
		t.Fatalf("unexpected voucher: %+v", found)
	}
	if got := found.DataLimitBytes(); got != 500*1024*1024 {
		t.Fatalf("DataLimitBytes = %d", got)
	}
	if !strings.Contains(found.LimitSummary(), "1h") {
		t.Fatalf("LimitSummary = %q", found.LimitSummary())
	}

	now := time.Now().UTC()
	redeemed, err := db.Vouchers().Redeem(ctx, found.ID, now)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if redeemed.Uses != 1 || redeemed.Status != VoucherUsed {
		t.Fatalf("after redeem = %+v", redeemed)
	}
	if redeemed.ActivatedAt == nil || redeemed.ExpiresAt == nil {
		t.Fatalf("expected activated/expires timestamps: %+v", redeemed)
	}
	if want := now.Add(time.Hour).Truncate(time.Second); redeemed.ExpiresAt.Sub(want) != 0 {
		t.Fatalf("expires_at = %s, want %s", redeemed.ExpiresAt, want)
	}

	// A single use voucher cannot be redeemed twice.
	if _, err := db.Vouchers().Redeem(ctx, found.ID, now); !errors.Is(err, ErrVoucherNotRedeemable) {
		t.Fatalf("second redeem err = %v, want ErrVoucherNotRedeemable", err)
	}

	// A multi-use voucher stays active until its window lapses.
	multi, err := db.Vouchers().FindByCode(ctx, multiCode)
	if err != nil {
		t.Fatalf("FindByCode(multi): %v", err)
	}
	multi, err = db.Vouchers().Redeem(ctx, multi.ID, now)
	if err != nil {
		t.Fatalf("Redeem(multi): %v", err)
	}
	if multi.Status != VoucherActive || multi.RemainingUses() != 2 {
		t.Fatalf("multi voucher after redeem = %+v", multi)
	}

	// The expiry sweeper flags lapsed vouchers that were still active.
	if n, err := db.Vouchers().SyncExpired(ctx, now.Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("SyncExpired = %d, err %v", n, err)
	}
	expired, err := db.Vouchers().Get(ctx, multi.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if expired.Status != VoucherExpired {
		t.Fatalf("status = %s, want expired", expired.Status)
	}

	stats, err := db.Vouchers().Stats(ctx, 0)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 2 || stats.Expired != 1 || stats.Used != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.BilledCents != 1500 || stats.FaceValueCents != 1500 {
		t.Fatalf("billed/face value = %d/%d, want 1500/1500", stats.BilledCents, stats.FaceValueCents)
	}

	if deleted, err := db.Vouchers().DeleteBatch(ctx, "BATCH-TEST", false); err != nil || deleted != 2 {
		t.Fatalf("DeleteBatch = %d, err %v", deleted, err)
	}
}

func TestRouterCRUDEncryptsPassword(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	created, err := db.Routers().Create(ctx, Router{
		Name: "HQ Tower", Host: "10.10.0.1", Port: 8728,
		Username: "aircoins", Password: "s3cret", Location: "Ground floor",
		PortalTag: "hq", DefaultPortal: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected an assigned id")
	}

	// The password must be encrypted at rest and decrypted on read.
	var stored string
	if err := db.SQL().QueryRowContext(ctx, `SELECT password FROM routers WHERE id = ?`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored password: %v", err)
	}
	if stored == "s3cret" || !strings.HasPrefix(stored, secretPrefix) {
		t.Fatalf("password is not encrypted at rest: %q", stored)
	}

	loaded, err := db.Routers().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Password != "s3cret" {
		t.Fatalf("decrypted password = %q, want s3cret", loaded.Password)
	}
	if !loaded.DefaultPortal || loaded.PortalTag != "hq" {
		t.Fatalf("portal settings not persisted: %+v", loaded)
	}

	// Renaming and updating keeps the stored secret when the field is blank.
	loaded.Name = "HQ Tower A"
	loaded.Password = ""
	updated, err := db.Routers().Update(ctx, loaded)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "HQ Tower A" || updated.Password != "s3cret" {
		t.Fatalf("update result = %+v", updated)
	}

	byTag, err := db.Routers().FindByPortalTag(ctx, "HQ")
	if err != nil || len(byTag) != 1 {
		t.Fatalf("FindByPortalTag = %d routers, err %v", len(byTag), err)
	}
	byHost, err := db.Routers().FindByHost(ctx, "10.10.0.1")
	if err != nil || len(byHost) != 1 {
		t.Fatalf("FindByHost = %d routers, err %v", len(byHost), err)
	}
	def, err := db.Routers().Default(ctx)
	if err != nil || def.ID != created.ID {
		t.Fatalf("Default = %+v, err %v", def, err)
	}

	// Duplicate names are rejected with a friendly message.
	if _, err := db.Routers().Create(ctx, Router{Name: "HQ Tower A", Host: "10.10.0.2", Username: "u"}); err == nil {
		t.Fatal("expected duplicate name to fail")
	}

	if err := db.Routers().Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := db.Routers().Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}
