package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/crypto"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// RouterColumns lists the routers table columns in scan order.
const RouterColumns = `id, name, host, api_port, api_tls, api_user, api_password_enc,
	ftp_host, ftp_port, ftp_user, ftp_password_enc, portal_token, stub_hash,
	verified, verify_override, probe_state, enabled, notes,
	identity, model, board_name, ros_version, arch, license_level,
	free_hdd_space, uptime_seconds, clock_offset_secs,
	last_seen_at, last_probe_at, created_at, updated_at`

// Routers is the repository for the router registry.
type Routers struct {
	db  *sql.DB
	key []byte
}

// NewRouters returns a router repository. key encrypts the stored API secret.
func NewRouters(db *sql.DB, key []byte) *Routers {
	return &Routers{db: db, key: key}
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanRouter reads one routers row and returns the encrypted secrets, which
// callers decrypt only when they actually need them.
func scanRouter(row rowScanner, r *domain.Router) (encAPI, encFTP string, err error) {
	var (
		apiTLS, verified, verifyOverride, enabled int
		lastSeen, lastProbe                       sql.NullString
		created, updated                          string
	)
	err = row.Scan(
		&r.ID, &r.Name, &r.Host, &r.APIPort, &apiTLS, &r.APIUser, &encAPI,
		&r.FTPHost, &r.FTPPort, &r.FTPUser, &encFTP, &r.PortalToken, &r.StubHash,
		&verified, &verifyOverride, &r.ProbeState, &enabled, &r.Notes,
		&r.Identity, &r.Model, &r.BoardName, &r.ROSVersion, &r.Arch, &r.LicenseLevel,
		&r.FreeHDDSpace, &r.UptimeSeconds, &r.ClockOffsetSecs,
		&lastSeen, &lastProbe, &created, &updated,
	)
	if err != nil {
		return "", "", err
	}

	r.APITLS = apiTLS != 0
	r.Verified = verified != 0
	r.VerifyOverride = verifyOverride != 0
	r.Enabled = enabled != 0

	r.LastSeenAt = nullTimePtr(lastSeen)
	r.LastProbeAt = nullTimePtr(lastProbe)
	r.CreatedAt = parseTimeParsed(created)
	r.UpdatedAt = parseTimeParsed(updated)
	return encAPI, encFTP, nil
}

// decrypt opens the stored secrets into memory.
func (s *Routers) decrypt(r *domain.Router, encAPI, encFTP string) error {
	api, err := crypto.Decrypt(s.key, encAPI)
	if err != nil {
		return fmt.Errorf("decrypt api password for router %d: %w", r.ID, err)
	}
	ftp, err := crypto.Decrypt(s.key, encFTP)
	if err != nil {
		return fmt.Errorf("decrypt ftp password for router %d: %w", r.ID, err)
	}
	r.APISecret = api
	r.FTPSecret = ftp
	return nil
}

// List returns every router ordered by name.
func (s *Routers) List(ctx context.Context) ([]domain.Router, error) {
	query := `SELECT ` + RouterColumns + ` FROM routers ORDER BY name COLLATE NOCASE`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list routers: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Router, 0, 8)
	for rows.Next() {
		var r domain.Router
		if _, _, err := scanRouter(rows, &r); err != nil {
			return nil, fmt.Errorf("scan router: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate routers: %w", err)
	}
	return out, nil
}

// Get returns the router with the given id, including its decrypted secrets.
func (s *Routers) Get(ctx context.Context, id int64) (*domain.Router, error) {
	query := `SELECT ` + RouterColumns + ` FROM routers WHERE id = ?`

	var r domain.Router
	encAPI, encFTP, err := scanRouter(s.db.QueryRowContext(ctx, query, id), &r)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get router %d: %w", id, err)
	}
	if err := s.decrypt(&r, encAPI, encFTP); err != nil {
		return nil, err
	}
	return &r, nil
}

// Create inserts a router and returns its new id.
func (s *Routers) Create(ctx context.Context, r *domain.Router) (int64, error) {
	encAPI, err := crypto.Encrypt(s.key, r.APISecret)
	if err != nil {
		return 0, fmt.Errorf("encrypt api password: %w", err)
	}
	encFTP, err := crypto.Encrypt(s.key, r.FTPSecret)
	if err != nil {
		return 0, fmt.Errorf("encrypt ftp password: %w", err)
	}

	now := nowStamp()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO routers (
			name, host, api_port, api_tls, api_user, api_password_enc,
			ftp_host, ftp_port, ftp_user, ftp_password_enc,
			portal_token, stub_hash, verified, verify_override, probe_state,
			enabled, notes, identity, model, board_name, ros_version, arch,
			license_level, free_hdd_space, uptime_seconds, clock_offset_secs,
			last_seen_at, last_probe_at, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.Host, r.APIPort, boolInt(r.APITLS), r.APIUser, encAPI,
		r.FTPHost, r.FTPPort, r.FTPUser, encFTP,
		r.PortalToken, r.StubHash, boolInt(r.Verified), boolInt(r.VerifyOverride), r.ProbeState,
		boolInt(r.Enabled), r.Notes, r.Identity, r.Model, r.BoardName, r.ROSVersion, r.Arch,
		r.LicenseLevel, r.FreeHDDSpace, r.UptimeSeconds, r.ClockOffsetSecs,
		nullableStamp(r.LastSeenAt), nullableStamp(r.LastProbeAt), now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: a router with this name or address already exists", ErrConflict)
		}
		return 0, fmt.Errorf("create router: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create router: %w", err)
	}
	return id, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableStamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(*t)
}
