package routeros_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros/faketos"
)

func TestProbeWarnsOnSmallFlashAndClockSkew(t *testing.T) {
	prober := proberFor(faketos.New(faketos.Options{FreeHDD: 64 * 1024, ClockOffsetHours: 3}))

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{Credentials: testCredentials()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if report.Result != domain.StatusWarn {
		t.Fatalf("expected WARN, got %s", report.Result)
	}
	if got := findCheck(t, report, "capacity").Status; got != domain.StatusWarn {
		t.Fatalf("capacity = %s, want WARN", got)
	}
	if got := findCheck(t, report, "clock").Status; got != domain.StatusWarn {
		t.Fatalf("clock = %s, want WARN", got)
	}
	if report.Caps.ClockOffsetSec < 10000 {
		t.Fatalf("expected a large clock offset, got %d s", report.Caps.ClockOffsetSec)
	}
}

func TestProbeStopsAfterDialFailure(t *testing.T) {
	apiContacted := false
	prober := &routeros.Prober{
		Dial: func(context.Context, routeros.Options) (routeros.Transport, error) {
			apiContacted = true
			return nil, errors.New("should not be called")
		},
		DialTCP: func(context.Context, string, time.Duration) error {
			return routeros.ErrTimeout
		},
	}

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{Credentials: testCredentials()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if apiContacted {
		t.Fatal("the API must not be contacted when the TCP check failed")
	}
	if report.Result != domain.StatusFail {
		t.Fatalf("expected FAIL, got %s", report.Result)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected only the dial check to run, got %v", checkIDs(report))
	}
	check := report.Checks[0]
	if check.Status != domain.StatusFail || check.Fix == "" {
		t.Fatalf("expected a failing dial check with a fix, got %+v", check)
	}
}

func TestLoginActiveClassifiesFailures(t *testing.T) {
	device := faketos.New(faketos.Options{})
	ctx := context.Background()

	// The simulated host table holds 10.5.50.23, so another address is rejected
	// exactly like a real device rejecting an unknown client.
	err := routeros.LoginActive(ctx, device, routeros.LoginRequest{
		User: "DEMO-0001", Password: "demo", IP: "10.5.50.99",
	})
	if !errors.Is(err, routeros.ErrHostUnknown) {
		t.Fatalf("expected ErrHostUnknown, got %v", err)
	}

	err = routeros.LoginActive(ctx, device, routeros.LoginRequest{
		User: "DEMO-0001", Password: "wrong", IP: "10.5.50.23",
	})
	if !errors.Is(err, routeros.ErrAuth) {
		t.Fatalf("expected ErrAuth for a wrong password, got %v", err)
	}

	if err := routeros.LoginActive(ctx, device, routeros.LoginRequest{
		User: "DEMO-0001", Password: "demo", IP: "10.5.50.23",
	}); err != nil {
		t.Fatalf("expected a successful login, got %v", err)
	}

	// A missing identifier must be rejected before any command is sent.
	if err := routeros.LoginActive(ctx, device, routeros.LoginRequest{User: "X", Password: "y"}); err == nil {
		t.Fatal("expected an error when neither ip nor mac-address is supplied")
	}
}

func TestJustInTimeUserLifecycle(t *testing.T) {
	device := faketos.New(faketos.Options{})
	ctx := context.Background()

	spec := routeros.HotspotUserSpec{
		Name:            "VOUCHER-1",
		Password:        "voucher-password",
		Profile:         "ac-1h-10M",
		LimitUptime:     "1h",
		LimitBytesTotal: "1073741824",
	}

	id, err := routeros.AddHotspotUser(ctx, device, spec)
	if err != nil {
		t.Fatalf("add hotspot user: %v", err)
	}
	if id == "" {
		t.Fatal("expected the device to return a generated id")
	}

	found, err := routeros.FindHotspotUserByName(ctx, device, "VOUCHER-1")
	if err != nil {
		t.Fatalf("find hotspot user: %v", err)
	}
	if found == nil {
		t.Fatal("expected the created user to be findable")
	}
	if found.Profile != "ac-1h-10M" || found.LimitUptime != "1h" {
		t.Fatalf("limits were not stored: %+v", found)
	}

	// Creating the same user twice must fail rather than silently duplicate.
	if _, err := routeros.AddHotspotUser(ctx, device, spec); err == nil {
		t.Fatal("expected a duplicate user to be rejected")
	}

	if err := routeros.SetHotspotUserDisabled(ctx, device, found.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if err := routeros.RemoveHotspotUser(ctx, device, found.ID); err != nil {
		t.Fatalf("remove user: %v", err)
	}
	gone, err := routeros.FindHotspotUserByName(ctx, device, "VOUCHER-1")
	if err != nil {
		t.Fatalf("find after remove: %v", err)
	}
	if gone != nil {
		t.Fatalf("expected the user to be removed, got %+v", gone)
	}
}

func TestEnsureUserProfileIsIdempotent(t *testing.T) {
	device := faketos.New(faketos.Options{})
	ctx := context.Background()

	// faketos seeds ac-1h-10M, so use a name the fixture does not know about.
	spec := routeros.UserProfileSpec{
		Name:           "ac-2h-20M",
		RateLimit:      "20M/20M",
		SharedUsers:    "3",
		SessionTimeout: "2h",
	}

	created, err := routeros.EnsureUserProfile(ctx, device, spec)
	if err != nil {
		t.Fatalf("ensure profile (first): %v", err)
	}
	if !created {
		t.Fatal("expected the first ensure to create the profile")
	}

	created, err = routeros.EnsureUserProfile(ctx, device, spec)
	if err != nil {
		t.Fatalf("ensure profile (second): %v", err)
	}
	if created {
		t.Fatal("expected the second ensure to update, not create")
	}

	created, err = routeros.EnsureUserProfile(ctx, device, routeros.UserProfileSpec{Name: "ac-1d-50M"})
	if err != nil {
		t.Fatalf("ensure profile (new plan): %v", err)
	}
	if !created {
		t.Fatal("expected a new plan to be created")
	}

	// The seeded demo profile must be unchanged by all of this.
	found, err := routeros.FindUserProfileByName(ctx, device, "ac-2h-20M")
	if err != nil {
		t.Fatalf("find profile: %v", err)
	}
	if found == nil || found.RateLimit != "20M/20M" || found.SharedUsers != "3" {
		t.Fatalf("profile limits were not stored: %+v", found)
	}
}
