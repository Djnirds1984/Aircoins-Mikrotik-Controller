package store

import (
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

func TestRouterProbePersistence(t *testing.T) {
	env := newTestEnv(t)

	id, err := env.routers.Create(env.ctx, sampleRouter("Lobby", "192.168.88.1"))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	report := &domain.ProbeReport{
		Address:   "192.168.88.1:8728",
		ProbedAt:  time.Now().UTC(),
		LatencyMS: 12,
		// Result is intentionally left empty: SaveProbe derives it from the
		// checks, exactly as the probe ladder does.
		Checks: []domain.ProbeCheck{
			{ID: "dial", Title: "TCP reachability", Status: domain.StatusPass, Message: "ok"},
			{ID: "write", Title: "Write permission", Status: domain.StatusWarn, Message: "skipped"},
		},
		Caps: domain.Capabilities{
			Identity:       "Lobby-AP",
			ROSVersion:     "7.16.2",
			BoardName:      "hAP ac^2",
			Arch:           "arm",
			FreeHDDSpace:   1048576,
			HotspotServers: []string{"hotspot1"},
		},
	}

	if err := env.routers.SaveProbe(env.ctx, id, report); err != nil {
		t.Fatalf("save probe: %v", err)
	}

	// A non-failing probe marks the router verified and caches device facts.
	router, err := env.routers.Get(env.ctx, id)
	if err != nil {
		t.Fatalf("get router: %v", err)
	}
	if !router.Verified {
		t.Fatal("expected a passing probe to mark the router verified")
	}
	if router.ProbeState != domain.ProbeWarn {
		t.Fatalf("expected probe state warn, got %q", router.ProbeState)
	}
	if router.BoardName != "hAP ac^2" || router.ROSVersion != "7.16.2" {
		t.Fatalf("device facts were not cached: %+v", router)
	}
	if router.LastSeenAt == nil {
		t.Fatal("expected last seen to be set by a probe")
	}

	stored, err := env.routers.LatestProbe(env.ctx, id)
	if err != nil {
		t.Fatalf("read latest probe: %v", err)
	}
	if len(stored.Checks) != 2 {
		t.Fatalf("expected 2 checks to round trip, got %d", len(stored.Checks))
	}
	if len(stored.Caps.HotspotServers) != 1 || stored.Caps.HotspotServers[0] != "hotspot1" {
		t.Fatalf("capabilities did not round trip: %+v", stored.Caps)
	}

	// A failing probe must clear the verified flag.
	report.Result = domain.StatusFail
	report.ProbedAt = time.Now().UTC().Add(time.Second)
	report.Checks = []domain.ProbeCheck{
		{ID: "dial", Title: "TCP reachability", Status: domain.StatusFail, Message: "no route"},
	}
	if err := env.routers.SaveProbe(env.ctx, id, report); err != nil {
		t.Fatalf("save failing probe: %v", err)
	}

	router, err = env.routers.Get(env.ctx, id)
	if err != nil {
		t.Fatalf("get router after failing probe: %v", err)
	}
	if router.Verified {
		t.Fatal("expected a failing probe to clear the verified flag")
	}
	if router.ProbeState != domain.ProbeFail {
		t.Fatalf("expected probe state fail, got %q", router.ProbeState)
	}
}

func TestHotspotInventoryCache(t *testing.T) {
	env := newTestEnv(t)

	id, err := env.routers.Create(env.ctx, sampleRouter("Lobby", "192.168.88.1"))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	servers := []domain.HotspotServerCache{
		{Name: "hotspot1", Interface: "ether3", AddressPool: "hs-pool-1", Profile: "hsprof1"},
		{Name: "hotspot2", Interface: "wlan1", Disabled: true},
	}
	if err := env.routers.SaveHotspotServers(env.ctx, id, servers); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	got, err := env.routers.ListHotspotServers(env.ctx, id)
	if err != nil {
		t.Fatalf("list inventory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 cached servers, got %d", len(got))
	}
	if got[1].Name != "hotspot2" || !got[1].Disabled {
		t.Fatalf("expected hotspot2 to be cached as disabled: %+v", got[1])
	}

	// A second sync must replace, not duplicate.
	if err := env.routers.SaveHotspotServers(env.ctx, id, servers[:1]); err != nil {
		t.Fatalf("re-save inventory: %v", err)
	}
	got, err = env.routers.ListHotspotServers(env.ctx, id)
	if err != nil {
		t.Fatalf("list inventory after re-sync: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the inventory to be replaced, got %d rows", len(got))
	}
}
