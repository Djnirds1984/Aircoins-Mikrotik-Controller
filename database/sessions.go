package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Session is one hotspot client observed on a device, kept locally so the
// controller still has history while the router is unreachable.
type Session struct {
	ID       int64
	RouterID int64
	// RouterName is filled by the store through a join.
	RouterName string
	// SessionKey is the RouterOS .id of the active entry, or a synthetic key.
	SessionKey string
	Username   string
	Address    string
	MACAddress string
	LoginBy    string
	Server     string
	Uptime     string
	BytesIn    int64
	BytesOut   int64

	StartedAt  time.Time
	LastSeenAt time.Time
	EndedAt    *time.Time
	EndReason  string
}

// Open reports whether the session is still active.
func (s Session) Open() bool { return s.EndedAt == nil }

// TotalBytes is the sum of both directions.
func (s Session) TotalBytes() int64 { return s.BytesIn + s.BytesOut }

// Duration is how long the session has been running (or ran).
func (s Session) Duration() time.Duration {
	end := s.LastSeenAt
	if s.EndedAt != nil {
		end = *s.EndedAt
	}
	if s.StartedAt.IsZero() || end.Before(s.StartedAt) {
		return 0
	}
	return end.Sub(s.StartedAt)
}

// SessionSnapshot is one entry of /ip/hotspot/active/print.
type SessionSnapshot struct {
	Key        string
	Username   string
	Address    string
	MACAddress string
	LoginBy    string
	Server     string
	Uptime     string
	BytesIn    int64
	BytesOut   int64
	StartedAt  time.Time
}

// SyncResult reports what a device sync changed locally.
type SyncResult struct {
	// Tracked is how many sessions the device reported.
	Tracked int
	// Closed is how many locally open sessions were no longer on the device.
	Closed int
	// Open is how many sessions are open locally after the sync.
	Open int
}

// SessionStore persists the observed sessions.
type SessionStore struct{ db *DB }

const sessionColumns = `
    s.id, s.router_id, COALESCE(r.name, ''), s.session_key, s.username, s.address,
    s.mac_address, s.login_by, s.server, s.uptime, s.bytes_in, s.bytes_out,
    s.started_at, s.last_seen_at, s.ended_at, s.end_reason`

const sessionFrom = ` FROM active_sessions s LEFT JOIN routers r ON r.id = s.router_id`

// SessionFilter narrows a session listing.
type SessionFilter struct {
	RouterID int64
	// MAC matches a client MAC address using the normalised form.
	MAC string
	// Status is "open", "closed" or empty for both.
	Status string
	// Query matches username, address or MAC.
	Query  string
	Limit  int
	Offset int
}

// SyncDevice reconciles the locally tracked sessions with the live list read
// from a router: missing entries are inserted, existing ones refreshed and any
// session the device no longer reports is closed.
func (s *SessionStore) SyncDevice(ctx context.Context, routerID int64, snapshots []SessionSnapshot, at time.Time) (SyncResult, error) {
	at = at.UTC().Truncate(time.Second)
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return SyncResult{}, wrapDBError("begin session sync", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a harmless no-op

	result := SyncResult{Tracked: len(snapshots)}
	seen := make([]string, 0, len(snapshots))
	for _, snap := range snapshots {
		key := strings.TrimSpace(snap.Key)
		if key == "" {
			key = syntheticSessionKey(snap)
		}
		started := snap.StartedAt.UTC().Truncate(time.Second)
		if started.IsZero() {
			started = at
		}

		if _, err := tx.ExecContext(ctx, `
            INSERT INTO active_sessions (
                router_id, session_key, username, address, mac_address, login_by,
                server, uptime, bytes_in, bytes_out, started_at, last_seen_at, ended_at, end_reason)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '')
            ON CONFLICT (router_id, session_key) DO UPDATE SET
                username = excluded.username,
                address = excluded.address,
                mac_address = excluded.mac_address,
                login_by = excluded.login_by,
                server = excluded.server,
                uptime = excluded.uptime,
                bytes_in = excluded.bytes_in,
                bytes_out = excluded.bytes_out,
                last_seen_at = excluded.last_seen_at,
                ended_at = NULL,
                end_reason = ''`,
			routerID, key, snap.Username, snap.Address, NormalizeMAC(snap.MACAddress),
			snap.LoginBy, snap.Server, snap.Uptime, snap.BytesIn, snap.BytesOut, stamp(started), stamp(at)); err != nil {
			return result, wrapDBError("upsert session", err)
		}
		seen = append(seen, key)
	}

	// Close every open session this router no longer reports. This is what
	// keeps the local table honest after a client disconnects, a device
	// reboots or the API connection was dropped earlier.
	query := `UPDATE active_sessions SET ended_at = ?, end_reason = ? WHERE router_id = ? AND ended_at IS NULL`
	args := []any{stamp(at), "client disconnected", routerID}
	if len(seen) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(seen)), ",")
		query += ` AND session_key NOT IN (` + placeholders + `)`
		for _, key := range seen {
			args = append(args, key)
		}
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return result, wrapDBError("close stale sessions", err)
	}
	if affected, err := res.RowsAffected(); err == nil {
		result.Closed = int(affected)
	}

	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM active_sessions WHERE router_id = ? AND ended_at IS NULL`,
		routerID).Scan(&result.Open); err != nil {
		return result, wrapDBError("count open sessions", err)
	}

	if err := tx.Commit(); err != nil {
		return result, wrapDBError("commit session sync", err)
	}
	return result, nil
}

// Close marks a session as finished. It is called after the operator
// disconnects a client and when a voucher is exhausted.
func (s *SessionStore) Close(ctx context.Context, routerID int64, sessionKey, reason string, at time.Time) (int64, error) {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return 0, errors.New("database: session key is required")
	}
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE active_sessions SET ended_at = ?, end_reason = ?
        WHERE router_id = ? AND ended_at IS NULL AND session_key = ?`,
		stamp(at.UTC()), truncate(reason, 200), routerID, key)
	if err != nil {
		return 0, wrapDBError("close session", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDBError("close session", err)
	}
	return affected, nil
}

// CloseByUser marks every open session of a hotspot user as finished.
func (s *SessionStore) CloseByUser(ctx context.Context, routerID int64, username, reason string, at time.Time) (int64, error) {
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE active_sessions SET ended_at = ?, end_reason = ?
        WHERE router_id = ? AND ended_at IS NULL AND lower(username) = lower(?)`,
		stamp(at.UTC()), truncate(reason, 200), routerID, strings.TrimSpace(username))
	if err != nil {
		return 0, wrapDBError("close sessions by user", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDBError("close sessions by user", err)
	}
	return affected, nil
}

// Register opens a session locally for a client that just authenticated
// through the captive portal, before the device reports it.
func (s *SessionStore) Register(ctx context.Context, session Session, at time.Time) error {
	key := strings.TrimSpace(session.SessionKey)
	if key == "" {
		key = syntheticSessionKey(SessionSnapshot{
			Username: session.Username, Address: session.Address, MACAddress: session.MACAddress,
		})
	}
	started := session.StartedAt.UTC().Truncate(time.Second)
	if started.IsZero() {
		started = at.UTC().Truncate(time.Second)
	}
	_, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO active_sessions (
            router_id, session_key, username, address, mac_address, login_by,
            server, uptime, started_at, last_seen_at, end_reason)
        VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, '')
        ON CONFLICT (router_id, session_key) DO UPDATE SET
            username = excluded.username,
            address = excluded.address,
            mac_address = excluded.mac_address,
            last_seen_at = excluded.last_seen_at,
            ended_at = NULL,
            end_reason = ''`,
		session.RouterID, key, session.Username, session.Address,
		NormalizeMAC(session.MACAddress), session.LoginBy, session.Server,
		stamp(started), stamp(at.UTC()))
	return wrapDBError("register session", err)
}

// List returns sessions matching the filter, newest first.
func (s *SessionStore) List(ctx context.Context, filter SessionFilter) ([]Session, error) {
	query, args := sessionQuery("SELECT"+sessionColumns+sessionFrom, filter)
	query += " ORDER BY s.last_seen_at DESC, s.id DESC LIMIT ? OFFSET ?"
	args = append(args, normalizedLimit(filter.Limit), maxInt(filter.Offset, 0))

	rows, err := s.db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list sessions", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, wrapDBError("list sessions", rows.Err())
}

// Count returns how many sessions match the filter.
func (s *SessionStore) Count(ctx context.Context, filter SessionFilter) (int64, error) {
	query, args := sessionQuery("SELECT COUNT(*) FROM active_sessions s", filter)
	var n int64
	if err := s.db.sql.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, wrapDBError("count sessions", err)
	}
	return n, nil
}

// sessionQuery builds the shared WHERE clause of List and Count.
func sessionQuery(base string, filter SessionFilter) (string, []any) {
	var (
		where []string
		args  []any
	)
	if filter.RouterID > 0 {
		where = append(where, "s.router_id = ?")
		args = append(args, filter.RouterID)
	}
	switch filter.Status {
	case "open":
		where = append(where, "s.ended_at IS NULL")
	case "closed":
		where = append(where, "s.ended_at IS NOT NULL")
	}
	if mac := NormalizeMAC(filter.MAC); mac != "" {
		where = append(where, "s.mac_address = ?")
		args = append(args, mac)
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		where = append(where, `(lower(s.username) LIKE ? OR lower(s.address) LIKE ? OR lower(s.mac_address) LIKE ?)`)
		args = append(args, like, like, like)
	}
	if len(where) > 0 {
		base += " WHERE " + strings.Join(where, " AND ")
	}
	return base, args
}

// CloseByMAC ends every open session of a client MAC address, used when an
// operator blocks a device.
func (s *SessionStore) CloseByMAC(ctx context.Context, routerID int64, mac, reason string, at time.Time) (int64, error) {
	normalized := NormalizeMAC(mac)
	if normalized == "" {
		return 0, errors.New("database: a MAC address is required")
	}
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE active_sessions SET ended_at = ?, end_reason = ?
        WHERE router_id = ? AND ended_at IS NULL AND mac_address = ?`,
		stamp(at.UTC()), truncate(reason, 200), routerID, normalized)
	if err != nil {
		return 0, wrapDBError("close sessions by mac", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDBError("close sessions by mac", err)
	}
	return affected, nil
}

// OpenForRouter returns the locally tracked live sessions of one router. It
// backs the device page while the API connection is down.
func (s *SessionStore) OpenForRouter(ctx context.Context, routerID int64) ([]Session, error) {
	return s.List(ctx, SessionFilter{RouterID: routerID, Status: "open", Limit: 500})
}

// CountOpen returns the number of live sessions, optionally for one router.
func (s *SessionStore) CountOpen(ctx context.Context, routerID int64) (int64, error) {
	query := `SELECT COUNT(*) FROM active_sessions WHERE ended_at IS NULL`
	var args []any
	if routerID > 0 {
		query += ` AND router_id = ?`
		args = append(args, routerID)
	}
	var n int64
	if err := s.db.sql.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, wrapDBError("count open sessions", err)
	}
	return n, nil
}

// OpenCountsByRouter returns the live session count per router id.
func (s *SessionStore) OpenCountsByRouter(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT router_id, COUNT(*) FROM active_sessions WHERE ended_at IS NULL GROUP BY router_id`)
	if err != nil {
		return nil, wrapDBError("count open sessions by router", err)
	}
	defer rows.Close()

	out := map[int64]int64{}
	for rows.Next() {
		var (
			routerID int64
			count    int64
		)
		if err := rows.Scan(&routerID, &count); err != nil {
			return nil, wrapDBError("count open sessions by router", err)
		}
		out[routerID] = count
	}
	return out, wrapDBError("count open sessions by router", rows.Err())
}

// PruneClosed deletes finished sessions older than the cutoff and returns how
// many rows were removed.
func (s *SessionStore) PruneClosed(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.sql.ExecContext(ctx,
		`DELETE FROM active_sessions WHERE ended_at IS NOT NULL AND ended_at < ?`, stamp(before.UTC()))
	if err != nil {
		return 0, wrapDBError("prune sessions", err)
	}
	return res.RowsAffected()
}

func scanSession(row scanner) (Session, error) {
	var (
		s         Session
		startedAt string
		seenAt    string
		endedAt   sql.NullString
	)
	err := row.Scan(&s.ID, &s.RouterID, &s.RouterName, &s.SessionKey, &s.Username,
		&s.Address, &s.MACAddress, &s.LoginBy, &s.Server, &s.Uptime, &s.BytesIn,
		&s.BytesOut, &startedAt, &seenAt, &endedAt, &s.EndReason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, err
		}
		return Session{}, wrapDBError("scan session", err)
	}
	s.StartedAt = parseStamp(sql.NullString{String: startedAt, Valid: startedAt != ""})
	s.LastSeenAt = parseStamp(sql.NullString{String: seenAt, Valid: seenAt != ""})
	s.EndedAt = parseStampPtr(endedAt)
	return s, nil
}

// syntheticSessionKey builds a stable key for RouterOS versions that report no
// .id on hotspot active entries.
func syntheticSessionKey(snap SessionSnapshot) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(snap.Username)),
		strings.TrimSpace(snap.Address),
		NormalizeMAC(snap.MACAddress),
	}
	return fmt.Sprintf("synth:%s", strings.Join(parts, "|"))
}
