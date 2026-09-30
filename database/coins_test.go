package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCoinCreditAccumulates covers the basic running total: successive reports
// for the same client add up rather than replacing each other.
func TestCoinCreditAccumulates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	total := 0
	for i, pulses := range []int{1, 2, 3} {
		total += pulses
		credit, err := store.Credit(ctx, CoinPulse{
			Subject: "mac:aabbccddeeff",
			Pulses:  pulses,
			Seconds: pulses * 300,
		}, now)
		if err != nil {
			t.Fatalf("credit %d: %v", i, err)
		}
		if credit.Pulses != total {
			t.Errorf("after report %d pulses = %d, want %d", i, credit.Pulses, total)
		}
	}

	credit, err := store.Get(ctx, "mac:aabbccddeeff")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if credit.GrantedSeconds != 1800 {
		t.Errorf("granted = %d, want 1800", credit.GrantedSeconds)
	}
	if credit.RemainingSeconds() != 1800 {
		t.Errorf("remaining = %d, want 1800", credit.RemainingSeconds())
	}
	if !credit.HasBalance() {
		t.Error("HasBalance = false with 30 minutes on the balance")
	}
}

// TestCoinCreditDuplicateEventIsIgnored is the property that makes a retrying
// NodeMCU safe: the same EventID twice must cost the customer one coin.
func TestCoinCreditDuplicateEventIsIgnored(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	pulse := CoinPulse{
		Subject: "mac:aabbccddeeff",
		Pulses:  1,
		Seconds: 300,
		EventID: "box-1-lu-1",
	}

	first, err := store.Credit(ctx, pulse, now)
	if err != nil {
		t.Fatalf("first credit: %v", err)
	}
	if first.Pulses != 1 {
		t.Fatalf("first credit pulses = %d, want 1", first.Pulses)
	}

	second, err := store.Credit(ctx, pulse, now.Add(time.Second))
	if !errors.Is(err, ErrCoinPulseDuplicate) {
		t.Fatalf("second credit error = %v, want ErrCoinPulseDuplicate", err)
	}
	// The returned credit is the unchanged balance, so the caller can answer
	// the node without a second read.
	if second.Pulses != 1 {
		t.Errorf("after the duplicate pulses = %d, want 1", second.Pulses)
	}
	if second.GrantedSeconds != 300 {
		t.Errorf("after the duplicate granted = %d, want 300", second.GrantedSeconds)
	}

	stored, err := store.Get(ctx, "mac:aabbccddeeff")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Pulses != 1 {
		t.Errorf("stored pulses = %d, want 1: the duplicate was persisted", stored.Pulses)
	}
}

// TestCoinCreditResetsAfterSpend proves a returning customer starts a fresh
// tally rather than having their new coins absorbed into an already-spent one.
func TestCoinCreditResetsAfterSpend(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:aabbccddeeff", Pulses: 1, Seconds: 300,
	}, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.Consume(ctx, "mac:aabbccddeeff", 300, now); err != nil {
		t.Fatalf("consume: %v", err)
	}

	credit, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:aabbccddeeff", Pulses: 1, Seconds: 600,
	}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second credit: %v", err)
	}
	if credit.GrantedSeconds != 600 {
		t.Errorf("granted = %d, want 600 (the spent balance must not carry over)", credit.GrantedSeconds)
	}
	if credit.UsedSeconds != 0 {
		t.Errorf("used = %d, want 0 after a reset", credit.UsedSeconds)
	}
	if credit.Status != CoinActive {
		t.Errorf("status = %q, want %q", credit.Status, CoinActive)
	}
}

// TestCoinConsumeNeverGoesNegative covers the race where two "Connect" taps
// arrive together: the deduction is capped at what was actually granted.
func TestCoinConsumeNeverGoesNegative(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:aabbccddeeff", Pulses: 1, Seconds: 300,
	}, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Ten concurrent over-requests against a 300 second balance.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Consume(ctx, "mac:aabbccddeeff", 10000, time.Now()); err != nil {
				t.Errorf("consume: %v", err)
			}
		}()
	}
	wg.Wait()

	credit, err := store.Get(ctx, "mac:aabbccddeeff")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if credit.UsedSeconds != 300 {
		t.Errorf("used = %d, want exactly 300", credit.UsedSeconds)
	}
	if credit.GrantedSeconds != 300 {
		t.Errorf("granted = %d, want 300", credit.GrantedSeconds)
	}
	if credit.RemainingSeconds() != 0 {
		t.Errorf("remaining = %d, want 0", credit.RemainingSeconds())
	}
	if credit.Status != CoinConnected {
		t.Errorf("status = %q, want %q", credit.Status, CoinConnected)
	}
}

// TestCoinExpireIdleReleasesAbandonedBalances is the anti-theft case: money a
// customer walked away from must not become a free session for the next person
// on the same address.
func TestCoinExpireIdleReleasesAbandonedBalances(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	// Abandoned half an hour ago.
	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:aaaaaaaaaaaa", Pulses: 2, Seconds: 600, IdleTTL: 20 * time.Minute,
	}, now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	// Fresh.
	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:bbbbbbbbbbbb", Pulses: 1, Seconds: 300, IdleTTL: 20 * time.Minute,
	}, now); err != nil {
		t.Fatalf("seed fresh: %v", err)
	}

	released, err := store.ExpireIdle(ctx, 20*time.Minute, now)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if released != 1 {
		t.Errorf("released %d, want 1", released)
	}

	stale, err := store.Get(ctx, "mac:aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if stale.Status != CoinExpired {
		t.Errorf("stale status = %q, want %q", stale.Status, CoinExpired)
	}
	fresh, err := store.Get(ctx, "mac:bbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if fresh.Status != CoinActive {
		t.Errorf("a fresh balance was expired: status = %q", fresh.Status)
	}

	// A zero window is a no-op rather than a mass expiry.
	if n, err := store.ExpireIdle(ctx, 0, now); err != nil || n != 0 {
		t.Errorf("ExpireIdle(0) = %d, %v; want 0, nil", n, err)
	}
}

// TestCoinSubjectPrefersMAC proves the key choice: a MAC survives a DHCP lease
// change, an address does not, so the MAC is preferred when both are known.
func TestCoinSubjectPrefersMAC(t *testing.T) {
	cases := []struct {
		name, mac, ip, want string
	}{
		{"mac wins", "AA:BB:CC:DD:EE:FF", "10.0.0.5", "mac:aabbccddeeff"},
		{"dashed mac", "aa-bb-cc-dd-ee-ff", "", "mac:aabbccddeeff"},
		{"bare mac", "AABBCCDDEEFF", "", "mac:aabbccddeeff"},
		{"address fallback", "", "10.0.0.5", "ip:10.0.0.5"},
		{"junk mac falls back", "not-a-mac", "10.0.0.5", "ip:10.0.0.5"},
		{"nothing", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CoinSubject(tc.mac, tc.ip); got != tc.want {
				t.Errorf("CoinSubject(%q, %q) = %q, want %q", tc.mac, tc.ip, got, tc.want)
			}
		})
	}
}

// TestCoinFindByMAC proves a client credited by address can still find its
// balance once RouterOS starts reporting the hardware address.
func TestCoinFindByMAC(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()

	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "ip:10.0.0.5", MACAddress: "AA:BB:CC:DD:EE:FF", Pulses: 1, Seconds: 300,
	}, time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The MAC must match in any of the shapes RouterOS, an HTML form and a URL
	// parameter use.
	for _, lookup := range []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff", "aabbccddeeff"} {
		credit, err := store.FindByMAC(ctx, lookup)
		if err != nil {
			t.Fatalf("FindByMAC(%q): %v", lookup, err)
		}
		if credit.RemainingSeconds() != 300 {
			t.Errorf("FindByMAC(%q) remaining = %d, want 300", lookup, credit.RemainingSeconds())
		}
	}

	if _, err := store.FindByMAC(ctx, "11:22:33:44:55:66"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByMAC for an unknown client = %v, want ErrNotFound", err)
	}
	if _, err := store.FindByMAC(ctx, "nonsense"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByMAC for a malformed address = %v, want ErrNotFound", err)
	}
}

// TestCoinRejectsAbsurdReports proves a stuck input line cannot mint an
// unbounded balance, which is the failure mode a public kiosk has to survive.
func TestCoinRejectsAbsurdReports(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	cases := []struct {
		name  string
		pulse CoinPulse
	}{
		{"too many pulses", CoinPulse{Subject: "mac:aabbccddeeff", Pulses: MaxCoinPulsesPerReport + 1}},
		{"negative pulses", CoinPulse{Subject: "mac:aabbccddeeff", Pulses: -1}},
		{"too much money", CoinPulse{Subject: "mac:aabbccddeeff", AmountCents: MaxCoinAmountCentsPerReport + 1}},
		{"too much time", CoinPulse{Subject: "mac:aabbccddeeff", Seconds: MaxCoinGrantedSecondsPerReport + 1}},
		{"no subject", CoinPulse{Pulses: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Credit(ctx, tc.pulse, now); !errors.Is(err, ErrCoinPulseInvalid) {
				t.Errorf("error = %v, want ErrCoinPulseInvalid", err)
			}
		})
	}

	// Nothing above may have been written.
	if _, err := store.Get(ctx, "mac:aabbccddeeff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a rejected report created a row: %v", err)
	}
}

// TestCoinLongIdentifiersAreTruncated proves a node with an over-long name or
// event id cannot hit the CHECK-constrained columns and fail the write, which
// would strand a customer's coin on the machine until someone noticed.
func TestCoinLongIdentifiersAreTruncated(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	longNode := strings.Repeat("n", 200)
	longEvent := strings.Repeat("e", 200)

	credit, err := db.Coins().Credit(ctx, CoinPulse{
		Subject: "mac:aabbccddeeff",
		Pulses:  1,
		Seconds: 300,
		NodeID:  longNode,
		EventID: longEvent,
	}, time.Now())
	if err != nil {
		t.Fatalf("a long identifier was rejected: %v", err)
	}
	if len(credit.NodeID) > MaxCoinNodeIDLen {
		t.Errorf("node id is %d characters, want at most %d", len(credit.NodeID), MaxCoinNodeIDLen)
	}
	if len(credit.LastEvent) > MaxCoinEventIDLen {
		t.Errorf("event id is %d characters, want at most %d", len(credit.LastEvent), MaxCoinEventIDLen)
	}
}

// TestCoinListActive proves the operator-facing ledger lists only balances that
// still hold unspent time, most recently fed first.
func TestCoinListActive(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Coins()
	now := time.Now()

	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:111111111111", Pulses: 1, Seconds: 300,
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed older: %v", err)
	}
	if _, err := store.Credit(ctx, CoinPulse{
		Subject: "mac:222222222222", Pulses: 2, Seconds: 600,
	}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed newer: %v", err)
	}

	entries, err := store.ListActive(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("listed %d entries, want 2", len(entries))
	}
	// Most recently fed first.
	if entries[0].Credit.Subject != "mac:222222222222" {
		t.Errorf("first entry = %q, want the most recently fed client", entries[0].Credit.Subject)
	}

	// Spending one balance removes it from the list.
	if _, err := store.Consume(ctx, "mac:222222222222", 600, now); err != nil {
		t.Fatalf("consume: %v", err)
	}
	entries, err = store.ListActive(ctx, 10)
	if err != nil {
		t.Fatalf("list after spend: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("listed %d entries after spending one, want 1", len(entries))
	}
}
