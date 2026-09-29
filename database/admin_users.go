package database

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Admin authentication parameters.
//
// PBKDF2-HMAC-SHA256 is used because it lives in the Go standard library
// (crypto/pbkdf2) and therefore adds no dependency to a controller that
// deliberately ships with almost none. The iteration count follows the OWASP
// guidance for PBKDF2-HMAC-SHA256; it is stored per row so it can be raised
// later while existing hashes keep verifying.
const (
	// pbkdf2Iterations is the work factor for new password hashes.
	pbkdf2Iterations = 210_000
	// pbkdf2SaltBytes is the per-password salt length. NIST SP 800-132 asks
	// for at least 16 bytes.
	pbkdf2SaltBytes = 16
	// pbkdf2KeyBytes is the derived key length.
	pbkdf2KeyBytes = 32
	// adminSessionTTL is how long a panel login stays valid.
	adminSessionTTL = 12 * time.Hour
)

// MinAdminPasswordLength is the shortest password the panel accepts.
const MinAdminPasswordLength = 10

// ErrWeakPassword is returned when a password does not meet the minimum
// length. It is a distinct error so the form can show it inline.
var ErrWeakPassword = errors.New("database: password is too short")

// ErrNoAdminUser is returned when the panel has no operator account yet.
var ErrNoAdminUser = errors.New("database: no admin user is configured")

// AdminUser is the single operator account that can sign into the panel.
type AdminUser struct {
	ID       int64
	Username string
	// CreatedAt and UpdatedAt are lifecycle stamps.
	CreatedAt time.Time
	UpdatedAt time.Time
	// PasswordChangedAt is when the password was last set. It is nil for an
	// account created by a bootstrap that has never been changed.
	PasswordChangedAt *time.Time
}

// AdminUserStore persists the panel credentials and the login sessions.
type AdminUserStore struct{ db *DB }

// AdminUsers returns the panel credential store.
func (db *DB) AdminUsers() *AdminUserStore { return &AdminUserStore{db: db} }

// hashPassword derives a new hash for password with a fresh random salt.
func hashPassword(password string, iterations int) (hash, salt string, err error) {
	if iterations <= 0 {
		iterations = pbkdf2Iterations
	}
	buf := make([]byte, pbkdf2SaltBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("database: generate password salt: %w", err)
	}
	return derivePassword(password, buf, iterations), base64.RawStdEncoding.EncodeToString(buf), nil
}

// derivePassword runs PBKDF2 over the given salt.
func derivePassword(password string, salt []byte, iterations int) string {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, pbkdf2KeyBytes)
	if err != nil {
		// pbkdf2.Key only fails on a non-positive iteration count or key
		// length, both of which are compile-time constants here.
		panic("database: pbkdf2 parameters are invalid: " + err.Error())
	}
	return base64.RawStdEncoding.EncodeToString(key)
}

// ValidateAdminPassword enforces the minimum length, rejecting the empty
// string that would otherwise create an account anyone can walk into.
func ValidateAdminPassword(password string) error {
	if len([]rune(password)) < MinAdminPasswordLength {
		return fmt.Errorf("%w: use at least %d characters", ErrWeakPassword, MinAdminPasswordLength)
	}
	return nil
}

// NormalizeAdminUsername trims and lowercases an operator name so "Admin" and
// "admin" cannot both exist (the column is COLLATE NOCASE UNIQUE).
func NormalizeAdminUsername(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Count returns how many operator accounts exist.
func (s *AdminUserStore) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&n)
	return n, wrapDBError("count admin users", err)
}

// EnsureAdminUser creates the operator account when the table is empty and
// returns the stored user. It never overwrites an existing account, so it is
// safe to call on every boot.
func (s *AdminUserStore) EnsureAdminUser(ctx context.Context, username, password string) (AdminUser, error) {
	var n int64
	if err := s.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&n); err != nil {
		return AdminUser{}, wrapDBError("count admin users", err)
	}
	if n > 0 {
		return s.Get(ctx)
	}
	if err := s.Create(ctx, username, password); err != nil {
		return AdminUser{}, err
	}
	return s.Get(ctx)
}

// Create stores a new operator account.
func (s *AdminUserStore) Create(ctx context.Context, username, password string) error {
	username = NormalizeAdminUsername(username)
	if username == "" {
		return errors.New("database: admin username is required")
	}
	if err := ValidateAdminPassword(password); err != nil {
		return err
	}
	hash, salt, err := hashPassword(password, pbkdf2Iterations)
	if err != nil {
		return err
	}
	at := now()
	if _, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO admin_users (username, password_hash, salt, iterations, created_at, updated_at, password_changed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		username, hash, salt, pbkdf2Iterations, stamp(at), stamp(at), stamp(at)); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("the operator name %q is already taken", username)
		}
		return wrapDBError("create admin user", err)
	}
	return nil
}

// Get loads the single operator account.
func (s *AdminUserStore) Get(ctx context.Context) (AdminUser, error) {
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT id, username, created_at, updated_at, password_changed_at FROM admin_users ORDER BY id LIMIT 1`)
	user, err := scanAdminUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNoAdminUser
	}
	return user, err
}

// FindByUsername looks an operator up by name, case-insensitively.
func (s *AdminUserStore) FindByUsername(ctx context.Context, username string) (AdminUser, error) {
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT id, username, created_at, updated_at, password_changed_at FROM admin_users WHERE username = ?`,
		NormalizeAdminUsername(username))
	user, err := scanAdminUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	return user, err
}

func (s *AdminUserStore) getByID(ctx context.Context, id int64) (AdminUser, error) {
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT id, username, created_at, updated_at, password_changed_at FROM admin_users WHERE id = ?`, id)
	user, err := scanAdminUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	return user, err
}

func scanAdminUser(row interface{ Scan(...any) error }) (AdminUser, error) {
	var (
		user      AdminUser
		created   string
		updated   string
		changedAt sql.NullString
	)
	if err := row.Scan(&user.ID, &user.Username, &created, &updated, &changedAt); err != nil {
		return AdminUser{}, err
	}
	user.CreatedAt = parseStamp(sql.NullString{String: created, Valid: created != ""})
	user.UpdatedAt = parseStamp(sql.NullString{String: updated, Valid: updated != ""})
	user.PasswordChangedAt = parseStampPtr(changedAt)
	return user, nil
}

// SetCredentials replaces the operator name and/or password. An empty field
// keeps the current value, so the form can change one at a time.
func (s *AdminUserStore) SetCredentials(ctx context.Context, id int64, username, password string) error {
	current, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	username = NormalizeAdminUsername(username)
	if username == "" {
		username = current.Username
	}
	if username != current.Username {
		if err := s.renameTaken(ctx, username, id); err != nil {
			return err
		}
	}

	if password == "" {
		_, err := s.db.sql.ExecContext(ctx,
			`UPDATE admin_users SET username = ?, updated_at = ? WHERE id = ?`,
			username, stamp(now()), id)
		return wrapDBError("update admin username", err)
	}
	if err := ValidateAdminPassword(password); err != nil {
		return err
	}
	hash, salt, err := hashPassword(password, pbkdf2Iterations)
	if err != nil {
		return err
	}
	at := now()
	_, err = s.db.sql.ExecContext(ctx, `
        UPDATE admin_users
           SET username = ?, password_hash = ?, salt = ?, iterations = ?,
               updated_at = ?, password_changed_at = ?
         WHERE id = ?`,
		username, hash, salt, pbkdf2Iterations, stamp(at), stamp(at), id)
	return wrapDBError("update admin credentials", err)
}

// renameTaken reports a friendly error when the new name belongs to another
// account instead of leaking a raw UNIQUE constraint failure.
func (s *AdminUserStore) renameTaken(ctx context.Context, username string, id int64) error {
	var n int64
	err := s.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_users WHERE username = ? AND id <> ?`, username, id).Scan(&n)
	if err != nil {
		return wrapDBError("check admin username", err)
	}
	if n > 0 {
		return fmt.Errorf("the operator name %q is already taken", username)
	}
	return nil
}

// VerifyPassword reports whether password matches the stored hash for user.
//
// The comparison is constant time, and an unknown account still performs a
// derivation so an attacker cannot tell "no such user" from "wrong password"
// by timing alone.
func (s *AdminUserStore) VerifyPassword(ctx context.Context, username, password string) (AdminUser, bool, error) {
	user, err := s.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Burn comparable time against a throwaway derivation.
			derivePassword(password, make([]byte, pbkdf2SaltBytes), pbkdf2Iterations)
			return AdminUser{}, false, nil
		}
		return AdminUser{}, false, err
	}
	var storedHash, salt string
	var iterations int
	err = s.db.sql.QueryRowContext(ctx,
		`SELECT password_hash, salt, iterations FROM admin_users WHERE id = ?`, user.ID).
		Scan(&storedHash, &salt, &iterations)
	if err != nil {
		return AdminUser{}, false, wrapDBError("read admin password", err)
	}
	rawSalt, err := base64.RawStdEncoding.DecodeString(salt)
	if err != nil {
		return AdminUser{}, false, fmt.Errorf("database: admin salt is corrupt: %w", err)
	}
	if iterations <= 0 {
		iterations = pbkdf2Iterations
	}
	candidate := derivePassword(password, rawSalt, iterations)
	if subtle.ConstantTimeCompare([]byte(candidate), []byte(storedHash)) != 1 {
		return AdminUser{}, false, nil
	}
	return user, true, nil
}
