package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/crypto"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// Update saves an existing router.
//
// Secrets use an empty string to mean "leave the stored value alone", so the
// edit form never has to round-trip a password back to the browser.
func (s *Routers) Update(ctx context.Context, r *domain.Router) error {
	encAPI, err := crypto.Encrypt(s.key, r.APISecret)
	if err != nil {
		return fmt.Errorf("encrypt api password: %w", err)
	}
	encFTP, err := crypto.Encrypt(s.key, r.FTPSecret)
	if err != nil {
		return fmt.Errorf("encrypt ftp password: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE routers SET
			name = ?, host = ?, api_port = ?, api_tls = ?, api_user = ?,
			api_password_enc = CASE WHEN ? = '' THEN api_password_enc ELSE ? END,
			ftp_host = ?, ftp_port = ?, ftp_user = ?,
			ftp_password_enc = CASE WHEN ? = '' THEN ftp_password_enc ELSE ? END,
			verified = ?, verify_override = ?, probe_state = ?, enabled = ?, notes = ?,
			updated_at = ?
		WHERE id = ?`,
		r.Name, r.Host, r.APIPort, boolInt(r.APITLS), r.APIUser,
		encAPI, encAPI,
		r.FTPHost, r.FTPPort, r.FTPUser,
		encFTP, encFTP,
		boolInt(r.Verified), boolInt(r.VerifyOverride), r.ProbeState, boolInt(r.Enabled), r.Notes,
		nowStamp(),
		r.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: another router already uses this name or address", ErrConflict)
		}
		return fmt.Errorf("update router %d: %w", r.ID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update router %d: %w", r.ID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a router and everything that belongs to it.
func (s *Routers) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM routers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete router %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete router %d: %w", id, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEnabled enables or disables polling and management of a router.
func (s *Routers) SetEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE routers SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolInt(enabled), nowStamp(), id)
	if err != nil {
		return fmt.Errorf("set router %d enabled=%v: %w", id, enabled, err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPortalToken stores the unguessable token used by the captive portal URLs.
func (s *Routers) SetPortalToken(ctx context.Context, id int64, token string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE routers SET portal_token = ?, updated_at = ? WHERE id = ?`,
		token, nowStamp(), id); err != nil {
		return fmt.Errorf("set portal token for router %d: %w", id, err)
	}
	return nil
}

// SetStubHash records which portal stub revision is installed on the router.
func (s *Routers) SetStubHash(ctx context.Context, id int64, hash string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE routers SET stub_hash = ?, updated_at = ? WHERE id = ?`,
		hash, nowStamp(), id); err != nil {
		return fmt.Errorf("set stub hash for router %d: %w", id, err)
	}
	return nil
}

// MarkSeen records a successful health poll.
func (s *Routers) MarkSeen(ctx context.Context, id int64, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE routers SET last_seen_at = ? WHERE id = ?`,
		stamp(at), id); err != nil {
		return fmt.Errorf("mark router %d seen: %w", id, err)
	}
	return nil
}

// CountByEndpoint reports how many routers already use an address, which lets
// the create form warn before hitting the unique index.
func (s *Routers) CountByEndpoint(ctx context.Context, host string, port int, excludeID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM routers WHERE host = ? AND api_port = ? AND id <> ?`,
		host, port, excludeID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count routers at %s:%d: %w", host, port, err)
	}
	return n, nil
}

// GetByPortalToken resolves the router a portal URL belongs to.
func (s *Routers) GetByPortalToken(ctx context.Context, token string) (*domain.Router, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM routers WHERE portal_token = ? AND enabled = 1`, token).Scan(&id)
	if errors.Is(err, sqlErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve portal token: %w", err)
	}
	return s.Get(ctx, id)
}
