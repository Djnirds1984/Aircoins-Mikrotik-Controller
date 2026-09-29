package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// hashSessionToken is the one-way function that stands in for a session token
// in the database.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession mints a session for userID and returns the opaque bearer
// token together with its expiry.
//
// Only the SHA-256 of the token is stored, so a copied database file cannot be
// replayed as a set of live logins.
func (s *AdminUserStore) CreateSession(ctx context.Context, userID int64, remote string, ttl time.Duration) (string, time.Time, error) {
	// Only a zero TTL means "unset". A negative one is a caller asking for a
	// session that is already expired (tests, and any future "revoke now"
	// path), so clamping it to the default here would silently hand out a
	// valid 12 hour session instead.
	if ttl == 0 {
		ttl = adminSessionTTL
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("database: generate session token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	at := now()
	expires := at.Add(ttl)
	_, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO admin_sessions (user_id, token_hash, created_at, expires_at, last_seen, remote)
        VALUES (?, ?, ?, ?, ?, ?)`,
		userID, hashSessionToken(raw), stamp(at), stamp(expires), stamp(at), truncate(remote, 200))
	if err != nil {
		return "", time.Time{}, wrapDBError("create admin session", err)
	}
	return raw, expires, nil
}

// SessionUser resolves a bearer token to its operator, refreshing last_seen.
// An expired or unknown token yields ErrNotFound and leaves no trace.
func (s *AdminUserStore) SessionUser(ctx context.Context, token string) (AdminUser, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return AdminUser{}, ErrNotFound
	}
	var (
		userID    int64
		expiresAt string
	)
	err := s.db.sql.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM admin_sessions WHERE token_hash = ?`, hashSessionToken(token)).
		Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	if err != nil {
		return AdminUser{}, wrapDBError("read admin session", err)
	}
	expires := parseStamp(sql.NullString{String: expiresAt, Valid: expiresAt != ""})
	if !expires.IsZero() && now().After(expires) {
		// Clean up as we go so the table does not grow without bound.
		_ = s.DeleteSession(ctx, token)
		return AdminUser{}, ErrNotFound
	}
	_, _ = s.db.sql.ExecContext(ctx,
		`UPDATE admin_sessions SET last_seen = ? WHERE token_hash = ?`, stamp(now()), hashSessionToken(token))
	return s.getByID(ctx, userID)
}

// DeleteSession revokes one token (logout).
func (s *AdminUserStore) DeleteSession(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	_, err := s.db.sql.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE token_hash = ?`, hashSessionToken(token))
	return wrapDBError("delete admin session", err)
}

// DeleteUserSessions revokes every session of a user. Changing the password
// calls it, so a stolen cookie dies with the old credentials.
func (s *AdminUserStore) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.sql.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ?`, userID)
	return wrapDBError("delete admin sessions", err)
}

// CountUserSessions returns how many live (unexpired) sessions a user holds.
func (s *AdminUserStore) CountUserSessions(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_sessions WHERE user_id = ? AND expires_at >= ?`,
		userID, stamp(now())).Scan(&n)
	return n, wrapDBError("count admin sessions", err)
}

// PruneExpiredSessions drops sessions that expired before the cutoff and
// returns how many rows were removed.
func (s *AdminUserStore) PruneExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.sql.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE expires_at < ?`, stamp(before.UTC()))
	if err != nil {
		return 0, wrapDBError("prune admin sessions", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDBError("count pruned admin sessions", err)
	}
	return affected, nil
}
