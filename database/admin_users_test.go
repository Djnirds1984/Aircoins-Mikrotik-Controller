package database

import (
	"context"
	"errors"
	"testing"
)

// TestSetPasswordReplacesCredentialsAndKeepsTheName covers the recovery path
// behind `aircoins-controller passwd`: an operator locked out of the panel
// must be able to set a new password on the account that already exists,
// without having to know or change the operator name.
func TestSetPasswordReplacesCredentialsAndKeepsTheName(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.AdminUsers()

	if _, err := store.EnsureAdminUser(ctx, "admin", "the-original-password"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	// A session exists and must not survive a password reset, or a stolen
	// cookie would outlive the reset meant to remove it.
	token, _, err := store.CreateSession(ctx, 1, "test", 0)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	user, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if err := store.SetPassword(ctx, user.ID, "a-replacement-password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	after, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get admin after reset: %v", err)
	}
	if after.Username != "admin" {
		t.Errorf("username = %q, want it unchanged at admin", after.Username)
	}
	if _, ok, _ := store.VerifyPassword(ctx, "admin", "a-replacement-password"); !ok {
		t.Error("the new password does not verify")
	}
	if _, ok, _ := store.VerifyPassword(ctx, "admin", "the-original-password"); ok {
		t.Error("the OLD password still verifies after a reset")
	}
	if _, err := store.SessionUser(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Error("a session survived the password reset")
	}
}

// TestSetPasswordRefusesWeakInput keeps the strength rule in force on the
// recovery path, not just on the web form.
func TestSetPasswordRefusesWeakInput(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.AdminUsers()

	user, err := store.EnsureAdminUser(ctx, "admin", "the-original-password")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := store.SetPassword(ctx, user.ID, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("SetPassword(short) = %v, want ErrWeakPassword", err)
	}
	// The rejected attempt must not have changed anything.
	if _, ok, _ := store.VerifyPassword(ctx, "admin", "the-original-password"); !ok {
		t.Error("a rejected reset damaged the stored password")
	}
}
