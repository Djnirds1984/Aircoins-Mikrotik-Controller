package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// Admins is the repository for panel administrators and their sessions.
type Admins struct {
	db *sql.DB
}

// NewAdmins returns an administrator repository.
func NewAdmins(db *sql.DB) *Admins { return &Admins{db: db} }

// Count returns how many administrators exist, which drives the first-run setup
// wizard.
func (s *Admins) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// Create inserts an administrator.
func (s *Admins) Create(ctx context.Context, a *domain.Admin) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO admins (username, password_hash, role, created_at)
		VALUES (?,?,?,?)`,
		a.Username, a.PasswordHash, a.Role, nowStamp())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: that username is already taken", ErrConflict)
		}
		return 0, fmt.Errorf("create admin: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create admin: %w", err)
	}
	return id, nil
}

// GetByUsername returns an administrator by login name.
func (s *Admins) GetByUsername(ctx context.Context, username string) (*domain.Admin, error) {
	var (
		a         domain.Admin
		disabled  int
		created   string
		lastLogin sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, disabled, created_at, last_login_at
		FROM admins WHERE username = ? COLLATE NOCASE`, username,
	).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &disabled, &created, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get admin %q: %w", username, err)
	}

	a.Disabled = disabled != 0
	a.CreatedAt = parseTimeParsed(created)
	a.LastLoginAt = nullTimePtr(lastLogin)
	return &a, nil
}

// MarkLogin records a successful login.
func (s *Admins) MarkLogin(ctx context.Context, id int64, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE admins SET last_login_at = ? WHERE id = ?`, stamp(at), id); err != nil {
		return fmt.Errorf("mark admin %d login: %w", id, err)
	}
	return nil
}

// SetPassword replaces an administrator's password hash.
func (s *Admins) SetPassword(ctx context.Context, id int64, hash string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE admins SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		return fmt.Errorf("set admin %d password: %w", id, err)
	}
	return nil
}

// CreateSession stores a session row keyed by the hash of the session token, so
// a database leak does not expose usable session cookies.
func (s *Admins) CreateSession(ctx context.Context, adminID int64, token string, expires time.Time, ip, userAgent string) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_sessions (token_hash, admin_id, created_at, expires_at, ip, user_agent)
		VALUES (?,?,?,?,?,?)`,
		HashToken(token), adminID, nowStamp(), stamp(expires), ip, userAgent,
	); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionAdmin resolves a session token to its administrator, rejecting expired
// sessions.
func (s *Admins) SessionAdmin(ctx context.Context, token string) (*domain.Admin, error) {
	if token == "" {
		return nil, ErrNotFound
	}

	var (
		a         domain.Admin
		disabled  int
		created   string
		lastLogin sql.NullString
		expires   string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.username, a.password_hash, a.role, a.disabled, a.created_at, a.last_login_at, s.expires_at
		FROM admin_sessions s
		JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = ?`, HashToken(token),
	).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &disabled, &created, &lastLogin, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}

	if exp := parseTimeParsed(expires); !exp.IsZero() && exp.Before(time.Now().UTC()) {
		return nil, ErrNotFound
	}

	a.Disabled = disabled != 0
	a.CreatedAt = parseTimeParsed(created)
	a.LastLoginAt = nullTimePtr(lastLogin)
	if a.Disabled {
		return nil, ErrNotFound
	}
	return &a, nil
}

// DeleteSession logs a session out.
func (s *Admins) DeleteSession(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE token_hash = ?`, HashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that have passed their expiry.
func (s *Admins) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE expires_at < ?`, nowStamp())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// HashToken returns the hex encoded SHA-256 of a session token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
