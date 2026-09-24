package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Router status values reported by the last connectivity probe.
const (
	RouterStatusUnknown = "unknown"
	RouterStatusOnline  = "online"
	RouterStatusOffline = "offline"
)

// Transport modes. The controller speaks two protocols: the legacy binary API
// ("api" on TCP 8728, "api-ssl" on 8729) and the RouterOS v7 REST API ("rest"
// over HTTP, "rest-ssl" over HTTPS, served by the www/www-ssl service).
// "auto" probes the candidates and remembers the winner in last_transport.
const (
	TransportAuto    = "auto"
	TransportAPI     = "api"
	TransportAPISSL  = "api-ssl"
	TransportREST    = "rest"
	TransportRESTSsl = "rest-ssl"
)

// ValidTransport reports whether mode names a transport the controller knows.
func ValidTransport(mode string) bool {
	switch mode {
	case TransportAuto, TransportAPI, TransportAPISSL, TransportREST, TransportRESTSsl:
		return true
	default:
		return false
	}
}

// NormalizeTransport maps unknown values - including the empty string - onto
// auto, so a stale form value can never break a connection.
func NormalizeTransport(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if ValidTransport(mode) {
		return mode
	}
	return TransportAuto
}

// TransportMode is the normalised transport setting of a router.
func (r Router) TransportMode() string { return NormalizeTransport(r.Transport) }

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("database: not found")

// Router is a registered MikroTik device and its API credentials.
type Router struct {
	ID       int64
	Name     string
	Host     string
	Port     int
	Username string
	// Password is the decrypted API password. It is only ever handed to the
	// RouterOS client, never rendered by a template.
	Password string
	UseTLS   bool
	// VerifyTLS enforces certificate verification for the TLS transports
	// (api-ssl and rest-ssl). MikroTik ships a self signed certificate by
	// default, so this is opt-in.
	VerifyTLS bool
	// Transport selects the protocol used to talk to the device: auto, api,
	// api-ssl, rest or rest-ssl. The empty string behaves like auto.
	Transport string
	// RestPort is the www/www-ssl port used by the REST transports. HTTPS
	// defaults to 443; plain HTTP requires an explicit port and never defaults
	// to port 80.
	RestPort int
	// LastTransport records the transport that last connected successfully, so
	// auto mode can try it first instead of probing every protocol again.
	LastTransport string
	Location      string
	// PortalTag matches the hotspot "server-name"/NAS identifier so the captive
	// portal can route a login request to the right device.
	PortalTag string
	// DefaultPortal marks the fallback device when a portal request cannot be
	// attributed to a specific router.
	DefaultPortal bool
	Notes         string

	LastStatus    string
	LastError     string
	LastLatencyMS int64
	LastSeenAt    *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Endpoint renders host:port for logs and error messages.
func (r Router) Endpoint() string {
	if r.Port <= 0 {
		return r.Host
	}
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

// MaskedPassword is shown in the UI so an operator can tell a password is
// stored without exposing it.
func (r Router) MaskedPassword() string {
	if r.Password == "" {
		return "(not set)"
	}
	return "••••••••••"
}

// RouterStore persists the router inventory.
type RouterStore struct{ db *DB }

const routerColumns = `
    id, name, host, port, username, password, use_tls, verify_tls, location,
    portal_tag, is_default_portal, notes, transport, rest_port, last_transport,
    last_status, last_error, last_latency_ms, last_seen_at, created_at, updated_at`

// List returns every registered router ordered by name.
func (s *RouterStore) List(ctx context.Context) ([]Router, error) {
	return s.query(ctx, `SELECT`+routerColumns+` FROM routers ORDER BY name COLLATE NOCASE`)
}

// Get loads one router by id.
func (s *RouterStore) Get(ctx context.Context, id int64) (Router, error) {
	row := s.db.sql.QueryRowContext(ctx, `SELECT`+routerColumns+` FROM routers WHERE id = ?`, id)
	router, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Router{}, ErrNotFound
	}
	return router, err
}

// FindByHost returns routers whose API host matches the given address. It is
// used to attribute a captive portal request through the hotspot link-login
// host.
func (s *RouterStore) FindByHost(ctx context.Context, host string) ([]Router, error) {
	return s.query(ctx,
		`SELECT`+routerColumns+` FROM routers WHERE lower(host) = lower(?) ORDER BY name COLLATE NOCASE`, host)
}

// FindByPortalTag returns routers whose portal tag matches the hotspot
// server-name reported by the client.
func (s *RouterStore) FindByPortalTag(ctx context.Context, tag string) ([]Router, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, nil
	}
	return s.query(ctx,
		`SELECT`+routerColumns+` FROM routers WHERE portal_tag <> '' AND lower(portal_tag) = lower(?) ORDER BY name COLLATE NOCASE`, tag)
}

// Default returns the router flagged as the fallback captive portal device.
func (s *RouterStore) Default(ctx context.Context) (Router, error) {
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT`+routerColumns+` FROM routers WHERE is_default_portal = 1 ORDER BY id LIMIT 1`)
	router, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Router{}, ErrNotFound
	}
	return router, err
}

func (s *RouterStore) query(ctx context.Context, sqlText string, args ...any) ([]Router, error) {
	rows, err := s.db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, wrapDBError("query routers", err)
	}
	defer rows.Close()

	var out []Router
	for rows.Next() {
		router, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, router)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("query routers", err)
	}
	return out, nil
}

// Create inserts a router and returns it with its assigned id.
func (s *RouterStore) Create(ctx context.Context, router Router) (Router, error) {
	sealed, err := s.db.box.Seal(router.Password)
	if err != nil {
		return Router{}, err
	}
	if router.Port == 0 {
		router.Port = 8728
	}
	if router.LastStatus == "" {
		router.LastStatus = RouterStatusUnknown
	}
	ts := stamp(now())

	res, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO routers (
            name, host, port, username, password, use_tls, verify_tls, location,
            portal_tag, is_default_portal, notes, transport, rest_port,
            last_status, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		router.Name, router.Host, router.Port, router.Username, sealed,
		boolToInt(router.UseTLS), boolToInt(router.VerifyTLS), router.Location,
		router.PortalTag, boolToInt(router.DefaultPortal), router.Notes,
		NormalizeTransport(router.Transport), router.RestPort,
		router.LastStatus, ts, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return Router{}, fmt.Errorf("a router named %q already exists", router.Name)
		}
		return Router{}, wrapDBError("create router", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Router{}, wrapDBError("create router id", err)
	}
	return s.Get(ctx, id)
}

// Update writes every mutable field. An empty Password keeps the stored one so
// the edit form can leave the secret untouched.
func (s *RouterStore) Update(ctx context.Context, router Router) (Router, error) {
	if router.Port == 0 {
		router.Port = 8728
	}
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE routers SET
            name = ?, host = ?, port = ?, username = ?,
            password = COALESCE(NULLIF(?, ''), password),
            use_tls = ?, verify_tls = ?, location = ?, portal_tag = ?,
            is_default_portal = ?, notes = ?, transport = ?, rest_port = ?,
            updated_at = ?
        WHERE id = ?`,
		router.Name, router.Host, router.Port, router.Username, sealedOrEmpty(s.db, router.Password),
		boolToInt(router.UseTLS), boolToInt(router.VerifyTLS), router.Location,
		router.PortalTag, boolToInt(router.DefaultPortal), router.Notes,
		NormalizeTransport(router.Transport), router.RestPort,
		stamp(now()), router.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return Router{}, fmt.Errorf("a router named %q already exists", router.Name)
		}
		return Router{}, wrapDBError("update router", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return Router{}, ErrNotFound
	}
	return s.Get(ctx, router.ID)
}

// SetPassword replaces only the stored API password.
func (s *RouterStore) SetPassword(ctx context.Context, id int64, password string) error {
	sealed, err := s.db.box.Seal(password)
	if err != nil {
		return err
	}
	res, err := s.db.sql.ExecContext(ctx,
		`UPDATE routers SET password = ?, updated_at = ? WHERE id = ?`, sealed, stamp(now()), id)
	if err != nil {
		return wrapDBError("set router password", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a router. Sessions cascade, vouchers keep their history with a
// NULL router reference.
func (s *RouterStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.sql.ExecContext(ctx, `DELETE FROM routers WHERE id = ?`, id)
	if err != nil {
		return wrapDBError("delete router", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearDefaultPortal unsets the fallback flag on every router except keepID so
// only one device can own the captive portal fallback.
func (s *RouterStore) ClearDefaultPortal(ctx context.Context, keepID int64) error {
	_, err := s.db.sql.ExecContext(ctx,
		`UPDATE routers SET is_default_portal = 0 WHERE id <> ? AND is_default_portal = 1`, keepID)
	return wrapDBError("clear default portal", err)
}

// RecordStatus stores the outcome of the last connection attempt.
func (s *RouterStore) RecordStatus(ctx context.Context, id int64, status, errMsg string, latency time.Duration) error {
	var seen any
	if status == RouterStatusOnline {
		seen = stamp(now())
	}
	_, err := s.db.sql.ExecContext(ctx, `
        UPDATE routers SET last_status = ?, last_error = ?, last_latency_ms = ?,
            last_seen_at = COALESCE(?, last_seen_at)
        WHERE id = ?`,
		status, truncate(errMsg, 500), latency.Milliseconds(), seen, id)
	return wrapDBError("record router status", err)
}

// RecordTransport remembers the transport that actually worked, so auto mode
// can start with it next time instead of probing every protocol again.
func (s *RouterStore) RecordTransport(ctx context.Context, id int64, transport string) error {
	_, err := s.db.sql.ExecContext(ctx,
		`UPDATE routers SET last_transport = ? WHERE id = ?`, NormalizeTransport(transport), id)
	return wrapDBError("record router transport", err)
}

// Count returns the number of registered routers.
func (s *RouterStore) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM routers`).Scan(&n)
	return n, wrapDBError("count routers", err)
}

// CountByStatus returns the number of routers per last known status.
func (s *RouterStore) CountByStatus(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT last_status, COUNT(*) FROM routers GROUP BY last_status`)
	if err != nil {
		return nil, wrapDBError("count routers by status", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, wrapDBError("count routers by status", err)
		}
		out[status] = count
	}
	return out, wrapDBError("count routers by status", rows.Err())
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func (s *RouterStore) scan(row scanner) (Router, error) {
	var (
		r             Router
		password      string
		useTLS        int
		verifyTLS     int
		defaultPrt    int
		transport     string
		restPort      int
		lastTransport string
		seenAt        sql.NullString
		createdAt     string
		updatedAt     string
	)
	err := row.Scan(
		&r.ID, &r.Name, &r.Host, &r.Port, &r.Username, &password, &useTLS, &verifyTLS,
		&r.Location, &r.PortalTag, &defaultPrt, &r.Notes, &transport, &restPort, &lastTransport,
		&r.LastStatus, &r.LastError,
		&r.LastLatencyMS, &seenAt, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Router{}, err
		}
		return Router{}, wrapDBError("scan router", err)
	}

	plain, err := s.db.box.Open(password)
	if err != nil {
		return Router{}, fmt.Errorf("router %q (id %d): %w", r.Name, r.ID, err)
	}
	r.Password = plain
	r.UseTLS = useTLS != 0
	r.VerifyTLS = verifyTLS != 0
	r.DefaultPortal = defaultPrt != 0
	r.Transport = NormalizeTransport(transport)
	r.RestPort = restPort
	r.LastTransport = strings.TrimSpace(lastTransport)
	r.LastSeenAt = parseStampPtr(seenAt)
	r.CreatedAt = parseStamp(sql.NullString{String: createdAt, Valid: createdAt != ""})
	r.UpdatedAt = parseStamp(sql.NullString{String: updatedAt, Valid: updatedAt != ""})
	return r, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// sealedOrEmpty encrypts a password for an UPDATE, leaving it empty when the
// caller wants to keep the stored value.
func sealedOrEmpty(db *DB, password string) string {
	if password == "" {
		return ""
	}
	sealed, err := db.box.Seal(password)
	if err != nil {
		// Sealing only fails on entropy errors; return an empty value so the
		// COALESCE keeps the previous password instead of writing garbage.
		db.log.Error("cannot encrypt router password", "error", err)
		return ""
	}
	return sealed
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}
