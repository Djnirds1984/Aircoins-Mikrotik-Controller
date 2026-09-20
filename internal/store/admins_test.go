package store

import (
	"errors"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

func TestAdminSessionRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	admins, ctx := env.admins, env.ctx

	count, err := admins.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected an empty admin table, got %d rows", count)
	}

	admin := &domain.Admin{
		Username:     "admin",
		PasswordHash: "hash",
		Role:         domain.RoleSuperAdmin,
	}
	id, err := admins.Create(ctx, admin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	found, err := admins.GetByUsername(ctx, "ADMIN")
	if err != nil {
		t.Fatalf("get admin (case insensitive): %v", err)
	}
	if found.ID != id {
		t.Fatalf("expected id %d, got %d", id, found.ID)
	}

	// A fresh token must not resolve.
	if _, err := admins.SessionAdmin(ctx, "not-a-session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown token, got %v", err)
	}

	token := "session-token-for-tests"
	expires := time.Now().UTC().Add(time.Hour)
	if err := admins.CreateSession(ctx, id, token, expires, "127.0.0.1", "test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	sessionAdmin, err := admins.SessionAdmin(ctx, token)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if sessionAdmin.Username != "admin" {
		t.Fatalf("expected username admin, got %q", sessionAdmin.Username)
	}

	// An expired session must not resolve.
	expiredToken := "expired-session-token"
	if err := admins.CreateSession(ctx, id, expiredToken, time.Now().UTC().Add(-time.Minute), "", ""); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if _, err := admins.SessionAdmin(ctx, expiredToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an expired session, got %v", err)
	}

	if err := admins.DeleteSession(ctx, token); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := admins.SessionAdmin(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the deleted session to be gone, got %v", err)
	}

	removed, err := admins.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("delete expired sessions: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 expired session to be removed, got %d", removed)
	}
}

func TestAdminUniqueUsername(t *testing.T) {
	env := newTestEnv(t)
	admins, ctx := env.admins, env.ctx

	admin := &domain.Admin{Username: "admin", PasswordHash: "hash", Role: domain.RoleSuperAdmin}
	if _, err := admins.Create(ctx, admin); err != nil {
		t.Fatalf("create first admin: %v", err)
	}
	if _, err := admins.Create(ctx, admin); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for a duplicate username, got %v", err)
	}
}
