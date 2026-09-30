package database

import (
	"context"
	"errors"
	"testing"
)

// TestRateNormalizesStructuredTime pins the Days:Hours:Minutes contract the
// admin form relies on: each dropdown field is bounded on its own, and the
// triple is folded into a single second count the coin path can do arithmetic
// on.
func TestRateNormalizesStructuredTime(t *testing.T) {
	tests := []struct {
		name                     string
		days, hours, minutes     int
		wantSeconds              int
		wantDays, wantH, wantMin int
	}{
		{"zero", 0, 0, 0, 0, 0, 0, 0},
		{"minutes only", 0, 0, 15, 900, 0, 0, 15},
		{"hours carry", 0, 2, 30, 9000, 0, 2, 30},
		{"days carry", 1, 0, 0, 86400, 1, 0, 0},
		{"all three fields", 1, 2, 30, 86400 + 2*3600 + 30*60, 1, 2, 30},
		{"max days", 30, 23, 59, 30*86400 + 23*3600 + 59*60, 30, 23, 59},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			total, d, h, m, err := NormalizeRateTime(tc.days, tc.hours, tc.minutes)
			if err != nil {
				t.Fatalf("NormalizeRateTime(%d,%d,%d): %v", tc.days, tc.hours, tc.minutes, err)
			}
			if total != tc.wantSeconds || d != tc.wantDays || h != tc.wantH || m != tc.wantMin {
				t.Errorf("got %ds = %dd %dh %dm, want %ds = %dd %dh %dm",
					total, d, h, m, tc.wantSeconds, tc.wantDays, tc.wantH, tc.wantMin)
			}
		})
	}
}

// TestRateRejectsOutOfRangeFields proves the dropdown bounds are enforced in the
// store and not only in the browser, so a hand-crafted POST cannot price a
// session longer than the operator is able to express.
func TestRateRejectsOutOfRangeFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		d, h, m int
	}{
		{"days over the cap", MaxRateDays + 1, 0, 0},
		{"hours over the cap", 0, MaxRateHours + 1, 0},
		{"minutes over the cap", 0, 0, MaxRateMinutes + 1},
		{"negative day", -1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, _, err := NormalizeRateTime(tc.d, tc.h, tc.m); !errors.Is(err, ErrRateInvalid) {
				t.Fatalf("got %v, want ErrRateInvalid", err)
			}
		})
	}
}

// TestRateCreateDerivesSeconds proves the stored granted seconds always matches
// the structured triple. granted_seconds is derived, so the two can never be
// allowed to drift - that drift would show up as a customer being charged one
// price and given another.
func TestRateCreateDerivesSeconds(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	rate, err := db.Rates().Create(ctx, Rate{
		Label:       "5-peso coin",
		Pulses:      1,
		AmountCents: 500,
		Days:        1, Hours: 2, Minutes: 30,
		Active: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := 86400 + 2*3600 + 30*60
	if rate.GrantedSeconds != want {
		t.Errorf("GrantedSeconds = %d, want %d", rate.GrantedSeconds, want)
	}
	if got := rate.TimeLabel(); got != "1d 2h 30m" {
		t.Errorf("TimeLabel = %q, want %q", got, "1d 2h 30m")
	}
	if rate.SecondsPerPulse() != want {
		t.Errorf("SecondsPerPulse = %d, want %d", rate.SecondsPerPulse(), want)
	}
}

// TestRateRejectsZeroTimeTier proves a tier that would award nothing is refused.
// It is the configuration-time version of "a coin buys the customer no Wi-Fi".
func TestRateRejectsZeroTimeTier(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	_, err := db.Rates().Create(ctx, Rate{Pulses: 1, AmountCents: 500})
	if !errors.Is(err, ErrRateInvalid) {
		t.Fatalf("a zero-time tier returned %v, want ErrRateInvalid", err)
	}
}

// TestRatePriceUsesLargestFittingTier is the pricing rule the whole feature
// rests on: a burst of pulses is matched against the biggest tier it fits, so a
// higher-value coin is never sold at the single-coin price.
func TestRatePriceUsesLargestFittingTier(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Rates()

	if _, err := store.Create(ctx, Rate{Label: "small", Pulses: 1, AmountCents: 500, Minutes: 15, Active: true}); err != nil {
		t.Fatalf("create small: %v", err)
	}
	if _, err := store.Create(ctx, Rate{Label: "large", Pulses: 4, AmountCents: 2000, Hours: 1, Active: true}); err != nil {
		t.Fatalf("create large: %v", err)
	}

	tests := []struct {
		name        string
		pulses      int
		wantSeconds int
		wantCents   int64
	}{
		// One pulse cannot use the four-pulse tier, so it falls to the small one.
		{"one pulse", 1, 900, 500},
		// Four pulses exactly fill the large tier. The cents are what
		// distinguishes this from the wrong answer: 4x900 also lands on 1h, so
		// only the face value reveals which tier was actually used.
		{"four pulses", 4, 3600, 2000},
		// Six pulses is one large tier plus two small ones.
		{"six pulses", 6, 3600 + 2*900, 2000 + 2*500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Price(ctx, tc.pulses)
			if err != nil {
				t.Fatalf("Price(%d): %v", tc.pulses, err)
			}
			if got.Seconds != tc.wantSeconds {
				t.Errorf("Seconds = %d, want %d", got.Seconds, tc.wantSeconds)
			}
			if got.Cents != tc.wantCents {
				t.Errorf("Cents = %d, want %d", got.Cents, tc.wantCents)
			}
			if got.PulsesConsumed != tc.pulses {
				t.Errorf("PulsesConsumed = %d, want %d (no pulse may be dropped)",
					got.PulsesConsumed, tc.pulses)
			}
		})
	}
}

// TestRatePriceIgnoresDisabledTier proves a disabled tier stops pricing coins
// while its row survives, which is what lets an operator retire a promo rate and
// still be able to explain a credit sold at it yesterday.
func TestRatePriceIgnoresDisabledTier(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Rates()

	small, err := store.Create(ctx, Rate{Label: "small", Pulses: 1, AmountCents: 500, Minutes: 15, Active: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Create(ctx, Rate{Label: "large", Pulses: 4, AmountCents: 2000, Hours: 1, Active: true}); err != nil {
		t.Fatalf("create large: %v", err)
	}
	if _, err := store.SetActive(ctx, small.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("a disabled tier was deleted: got %d rows, want 2", len(all))
	}

	// With only the four-pulse tier active, a single pulse is priced by the
	// remainder path rather than by the retired tier.
	got, err := store.Price(ctx, 1)
	if err != nil {
		t.Fatalf("Price(1): %v", err)
	}
	if got.Seconds != 900 { // 3600/4 per pulse
		t.Errorf("Seconds = %d, want 900", got.Seconds)
	}
}

// TestRatePriceWithNoActiveRates proves the "never configured" case is
// distinguishable, which is what lets the HTTP layer fall back to the
// environment rate instead of selling coins for nothing.
func TestRatePriceWithNoActiveRates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if _, err := db.Rates().Price(ctx, 3); !errors.Is(err, ErrNoActiveRates) {
		t.Fatalf("got %v, want ErrNoActiveRates", err)
	}
	// A zero-pulse report is not an error: it is a de-duplicated retry.
	got, err := db.Rates().Price(ctx, 0)
	if err != nil {
		t.Fatalf("Price(0): %v", err)
	}
	if got.Seconds != 0 {
		t.Errorf("Seconds = %d, want 0", got.Seconds)
	}
}

// TestRateFallbackSecondsPerPulse proves the "each coin buys X" line agrees with
// the price that will actually be applied, and falls back to the environment
// value when nothing is configured.
func TestRateFallbackSecondsPerPulse(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.Rates()

	if got := store.FallbackSecondsPerPulse(ctx, 300); got != 300 {
		t.Errorf("with no tiers FallbackSecondsPerPulse = %d, want 300", got)
	}
	if _, err := store.Create(ctx, Rate{Pulses: 1, AmountCents: 500, Minutes: 15, Active: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := store.FallbackSecondsPerPulse(ctx, 300); got != 900 {
		t.Errorf("with a tier FallbackSecondsPerPulse = %d, want 900", got)
	}
}

// TestRateUpdateRecomputesSeconds proves editing a tier through Update cannot
// leave a stale granted_seconds behind, which is the failure that would sell a
// coin at yesterday's price.
func TestRateUpdateRecomputesSeconds(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	rate, err := db.Rates().Create(ctx, Rate{Pulses: 1, AmountCents: 500, Minutes: 15, Active: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := db.Rates().Update(ctx, Rate{
		ID: rate.ID, Pulses: 1, AmountCents: 500,
		Days: 2, Hours: 3, Minutes: 4, Active: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	want := 2*86400 + 3*3600 + 4*60
	if updated.GrantedSeconds != want {
		t.Fatalf("GrantedSeconds = %d, want %d", updated.GrantedSeconds, want)
	}
	if updated.UpdatedAt.Before(rate.UpdatedAt) {
		t.Error("UpdatedAt did not advance on edit")
	}
}

// TestRateDeleteMissingIsNotFound proves the delete path distinguishes "gone"
// from "never there", so the panel can say so rather than reporting a success.
func TestRateDeleteMissingIsNotFound(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if err := db.Rates().Delete(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
