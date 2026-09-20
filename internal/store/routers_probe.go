package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
)

// sqlErrNoRows is aliased so callers do not need to import database/sql.
var sqlErrNoRows = sql.ErrNoRows

// SaveProbe stores a probe run and refreshes the cached device facts on the
// router row. A probe that does not fail is what marks a router verified.
func (s *Routers) SaveProbe(ctx context.Context, id int64, rep *domain.ProbeReport) error {
	checks, err := json.Marshal(rep.Checks)
	if err != nil {
		return fmt.Errorf("encode probe checks: %w", err)
	}
	caps, err := json.Marshal(rep.Caps)
	if err != nil {
		return fmt.Errorf("encode probe capabilities: %w", err)
	}

	probedAt := rep.ProbedAt
	if probedAt.IsZero() {
		probedAt = time.Now()
	}

	// Derive the overall result from the checks so a stored report is always
	// self-consistent, even if a caller populated checks without a result.
	rep.Result = routeros.DeriveResult(rep.Checks)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin probe transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO router_probes (router_id, probed_at, result, latency_ms, address, checks_json, caps_json)
		VALUES (?,?,?,?,?,?,?)`,
		id, stamp(probedAt), string(rep.Result), rep.LatencyMS, rep.Address,
		string(checks), string(caps),
	); err != nil {
		return fmt.Errorf("record probe for router %d: %w", id, err)
	}

	verified := 0
	if rep.Result != domain.StatusFail {
		verified = 1
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE routers SET
			probe_state = ?, verified = ?,
			identity = ?, model = ?, board_name = ?, ros_version = ?, arch = ?,
			license_level = ?, free_hdd_space = ?, uptime_seconds = ?, clock_offset_secs = ?,
			last_probe_at = ?, last_seen_at = ?, updated_at = ?
		WHERE id = ?`,
		probeState(rep.Result), verified,
		rep.Caps.Identity, rep.Caps.Model, rep.Caps.BoardName, rep.Caps.ROSVersion, rep.Caps.Arch,
		rep.Caps.LicenseLevel, rep.Caps.FreeHDDSpace, rep.Caps.UptimeSeconds, rep.Caps.ClockOffsetSec,
		stamp(probedAt), stamp(probedAt), nowStamp(),
		id,
	); err != nil {
		return fmt.Errorf("update router %d after probe: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit probe for router %d: %w", id, err)
	}
	return nil
}

// probeState maps an overall probe result onto the stored probe_state value.
func probeState(result domain.CheckStatus) string {
	switch result {
	case domain.StatusPass:
		return domain.ProbePass
	case domain.StatusWarn:
		return domain.ProbeWarn
	case domain.StatusFail:
		return domain.ProbeFail
	default:
		return domain.ProbeUnknown
	}
}

// LatestProbe returns the most recent stored probe report for a router.
func (s *Routers) LatestProbe(ctx context.Context, id int64) (*domain.ProbeReport, error) {
	var (
		probedAt        string
		result, address string
		latency         int64
		checks, caps    string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT probed_at, result, latency_ms, address, checks_json, caps_json
		FROM router_probes WHERE router_id = ?
		ORDER BY probed_at DESC, id DESC LIMIT 1`, id,
	).Scan(&probedAt, &result, &latency, &address, &checks, &caps)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read latest probe for router %d: %w", id, err)
	}

	rep := &domain.ProbeReport{
		RouterID:  id,
		Address:   address,
		ProbedAt:  parseTimeParsed(probedAt),
		LatencyMS: latency,
		Result:    domain.CheckStatus(result),
	}
	if err := json.Unmarshal([]byte(checks), &rep.Checks); err != nil {
		return nil, fmt.Errorf("decode probe checks for router %d: %w", id, err)
	}
	if err := json.Unmarshal([]byte(caps), &rep.Caps); err != nil {
		return nil, fmt.Errorf("decode probe capabilities for router %d: %w", id, err)
	}
	return rep, nil
}

// SaveHotspotServers replaces the cached hotspot server inventory for a router.
func (s *Routers) SaveHotspotServers(ctx context.Context, routerID int64, servers []domain.HotspotServerCache) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin inventory transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM hotspot_servers WHERE router_id = ?`, routerID); err != nil {
		return fmt.Errorf("clear hotspot inventory for router %d: %w", routerID, err)
	}

	for _, srv := range servers {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hotspot_servers (router_id, name, interface, address_pool, profile, disabled, raw_json, synced_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			routerID, srv.Name, srv.Interface, srv.AddressPool, srv.Profile,
			boolInt(srv.Disabled), srv.Raw, nowStamp(),
		); err != nil {
			return fmt.Errorf("store hotspot server %s: %w", srv.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hotspot inventory for router %d: %w", routerID, err)
	}
	return nil
}

// ListHotspotServers returns the cached hotspot servers for a router.
func (s *Routers) ListHotspotServers(ctx context.Context, routerID int64) ([]domain.HotspotServerCache, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, interface, address_pool, profile, disabled, synced_at
		FROM hotspot_servers WHERE router_id = ? ORDER BY name COLLATE NOCASE`, routerID)
	if err != nil {
		return nil, fmt.Errorf("list hotspot servers for router %d: %w", routerID, err)
	}
	defer rows.Close()

	out := make([]domain.HotspotServerCache, 0, 4)
	for rows.Next() {
		var (
			srv      domain.HotspotServerCache
			disabled int
			synced   string
		)
		if err := rows.Scan(&srv.Name, &srv.Interface, &srv.AddressPool, &srv.Profile, &disabled, &synced); err != nil {
			return nil, fmt.Errorf("scan hotspot server: %w", err)
		}
		srv.Disabled = disabled != 0
		srv.SyncedAt = parseTimeParsed(synced)
		out = append(out, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hotspot servers: %w", err)
	}
	return out, nil
}
