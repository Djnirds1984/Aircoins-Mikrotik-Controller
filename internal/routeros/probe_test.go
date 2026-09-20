// Package routeros_test exercises the probe ladder against the simulated device.
//
// It lives in an external test package because faketos imports routeros.
package routeros_test

import (
	"context"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros/faketos"
)

// proberFor builds a Prober that talks to a simulated device.
func proberFor(device *faketos.Device) *routeros.Prober {
	return &routeros.Prober{
		Dial: func(context.Context, routeros.Options) (routeros.Transport, error) {
			return device, nil
		},
		DialTCP: func(context.Context, string, time.Duration) error { return nil },
	}
}

func testCredentials() domain.RouterCredentials {
	return domain.RouterCredentials{Host: "demo.local", Port: 8728, User: "admin", Password: "secret"}
}

func findCheck(t *testing.T, report *domain.ProbeReport, id string) domain.ProbeCheck {
	t.Helper()
	for _, c := range report.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("probe report has no %q check; checks: %v", id, checkIDs(report))
	return domain.ProbeCheck{}
}

func checkIDs(report *domain.ProbeReport) []string {
	out := make([]string, 0, len(report.Checks))
	for _, c := range report.Checks {
		out = append(out, c.ID)
	}
	return out
}

func TestProbePassesAgainstSimulatedDevice(t *testing.T) {
	device := faketos.New(faketos.Options{})
	prober := proberFor(device)

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{
		Credentials: testCredentials(),
		AllowWrite:  true,
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	if report.Result != domain.StatusPass {
		t.Fatalf("expected PASS, got %s; checks: %+v", report.Result, report.Checks)
	}
	if !report.Caps.HotspotMenuPresent {
		t.Error("expected the hotspot menu to be detected")
	}
	if report.Caps.LoginMethods == "" {
		t.Error("expected login-by to be captured")
	}
	if !report.Caps.APIWritable {
		t.Error("expected the write check to succeed")
	}
	if report.Caps.EstimatedUserCapacity <= 0 {
		t.Error("expected a positive user capacity estimate")
	}
	if got := findCheck(t, report, "write").Status; got != domain.StatusPass {
		t.Fatalf("write check = %s, want PASS", got)
	}

	// The write check must clean up after itself.
	walled, err := routeros.ReadWalledGarden(context.Background(), device)
	if err != nil {
		t.Fatalf("read walled garden: %v", err)
	}
	for _, entry := range walled {
		if entry.DstHost == "aircoins-probe.invalid" {
			t.Fatal("the probe left a scratch walled-garden entry behind")
		}
	}
}

func TestProbeFailsWhenPAPIsUnavailable(t *testing.T) {
	prober := proberFor(faketos.New(faketos.Options{LoginBy: "http-chap,cookie"}))

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{Credentials: testCredentials()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if report.Result != domain.StatusFail {
		t.Fatalf("expected FAIL, got %s", report.Result)
	}

	check := findCheck(t, report, "login_methods")
	if check.Status != domain.StatusFail {
		t.Fatalf("login_methods = %s, want FAIL", check.Status)
	}
	if check.Fix == "" {
		t.Error("a failed check must carry a suggested fix")
	}
	// Other checks must still run, because the device was reachable.
	if got := findCheck(t, report, "hotspot").Status; got != domain.StatusPass {
		t.Fatalf("hotspot inventory = %s, want PASS", got)
	}
}

func TestProbeDetectsReadOnlyAccount(t *testing.T) {
	prober := proberFor(faketos.New(faketos.Options{ReadOnly: true}))

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{
		Credentials: testCredentials(),
		AllowWrite:  true,
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if report.Result != domain.StatusFail {
		t.Fatalf("expected FAIL, got %s", report.Result)
	}
	if got := findCheck(t, report, "write").Status; got != domain.StatusFail {
		t.Fatalf("write = %s, want FAIL", got)
	}
	if report.Caps.APIWritable {
		t.Error("expected APIWritable to be false")
	}
}

func TestProbeSkipsWriteCheckByDefault(t *testing.T) {
	prober := proberFor(faketos.New(faketos.Options{}))

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{Credentials: testCredentials()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got := findCheck(t, report, "write").Status; got != domain.StatusSkip {
		t.Fatalf("write = %s, want SKIP when the write test is not requested", got)
	}
}

func TestProbeDetectsMissingHotspotPackage(t *testing.T) {
	prober := proberFor(faketos.New(faketos.Options{SkipHotspot: true}))

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{Credentials: testCredentials()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if report.Result != domain.StatusFail {
		t.Fatalf("expected FAIL, got %s", report.Result)
	}
	if got := findCheck(t, report, "hotspot").Status; got != domain.StatusFail {
		t.Fatalf("hotspot = %s, want FAIL", got)
	}
	// The dependent check must be skipped rather than reported as a failure.
	if got := findCheck(t, report, "login_methods").Status; got != domain.StatusSkip {
		t.Fatalf("login_methods = %s, want SKIP", got)
	}
}

func TestProbeDetectsDeviceModeRestriction(t *testing.T) {
	prober := proberFor(faketos.New(faketos.Options{BlockHotspotDeviceMode: true}))

	report, err := prober.Probe(context.Background(), routeros.ProbeOptions{Credentials: testCredentials()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got := findCheck(t, report, "device_mode").Status; got != domain.StatusFail {
		t.Fatalf("device_mode = %s, want FAIL", got)
	}
}
