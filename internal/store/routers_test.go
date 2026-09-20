package store

import (
	"errors"
	"testing"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

func sampleRouter(name, host string) *domain.Router {
	return &domain.Router{
		Name:       name,
		Host:       host,
		APIPort:    8728,
		APIUser:    "apiuser",
		APISecret:  "super-secret-password",
		FTPHost:    host,
		FTPPort:    21,
		FTPUser:    "ftpuser",
		FTPSecret:  "ftp-secret",
		Enabled:    true,
		ProbeState: domain.ProbeUnknown,
	}
}

func TestRouterCRUDAndSecretEncryption(t *testing.T) {
	env := newTestEnv(t)

	id, err := env.routers.Create(env.ctx, sampleRouter("Lobby", "192.168.88.1"))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	// The secret must come back intact through decryption.
	got, err := env.routers.Get(env.ctx, id)
	if err != nil {
		t.Fatalf("get router: %v", err)
	}
	if got.APISecret != "super-secret-password" {
		t.Fatalf("expected the API secret to round trip, got %q", got.APISecret)
	}
	if !got.Enabled {
		t.Fatal("expected the router to be enabled")
	}

	// The plaintext must not appear in the stored column.
	var encAPI string
	if err := env.db.QueryRowContext(env.ctx,
		`SELECT api_password_enc FROM routers WHERE id = ?`, id,
	).Scan(&encAPI); err != nil {
		t.Fatalf("read encrypted column: %v", err)
	}
	if encAPI == "" || encAPI == "super-secret-password" {
		t.Fatalf("API password was not encrypted at rest: %q", encAPI)
	}

	// Updating with an empty secret must keep the stored one.
	got.Name = "Lobby renamed"
	got.APISecret = ""
	if err := env.routers.Update(env.ctx, got); err != nil {
		t.Fatalf("update router: %v", err)
	}
	reloaded, err := env.routers.Get(env.ctx, id)
	if err != nil {
		t.Fatalf("reload router: %v", err)
	}
	if reloaded.Name != "Lobby renamed" {
		t.Fatalf("expected the rename to persist, got %q", reloaded.Name)
	}
	if reloaded.APISecret != "super-secret-password" {
		t.Fatalf("an empty secret should keep the stored value, got %q", reloaded.APISecret)
	}

	// Duplicate endpoints must be rejected.
	if _, err := env.routers.Create(env.ctx, sampleRouter("Other", "192.168.88.1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for a duplicate address, got %v", err)
	}

	// The registry lists what was stored.
	list, err := env.routers.List(env.ctx)
	if err != nil {
		t.Fatalf("list routers: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("expected one router in the list, got %d", len(list))
	}

	if err := env.routers.Delete(env.ctx, id); err != nil {
		t.Fatalf("delete router: %v", err)
	}
	if _, err := env.routers.Get(env.ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRouterEnabledToggleAndPortalToken(t *testing.T) {
	env := newTestEnv(t)

	id, err := env.routers.Create(env.ctx, sampleRouter("Lobby", "192.168.88.1"))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	if err := env.routers.SetEnabled(env.ctx, id, false); err != nil {
		t.Fatalf("disable router: %v", err)
	}
	router, err := env.routers.Get(env.ctx, id)
	if err != nil {
		t.Fatalf("get router: %v", err)
	}
	if router.Enabled {
		t.Fatal("expected the router to be disabled")
	}

	// A disabled router must not resolve from a portal token.
	if err := env.routers.SetPortalToken(env.ctx, id, "portal-token-123"); err != nil {
		t.Fatalf("set portal token: %v", err)
	}
	if _, err := env.routers.GetByPortalToken(env.ctx, "portal-token-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected a disabled router not to resolve, got %v", err)
	}

	if err := env.routers.SetEnabled(env.ctx, id, true); err != nil {
		t.Fatalf("enable router: %v", err)
	}
	found, err := env.routers.GetByPortalToken(env.ctx, "portal-token-123")
	if err != nil {
		t.Fatalf("resolve enabled router by portal token: %v", err)
	}
	if found.ID != id {
		t.Fatalf("expected router %d, got %d", id, found.ID)
	}

	if _, err := env.routers.GetByPortalToken(env.ctx, "wrong-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown token, got %v", err)
	}
}
