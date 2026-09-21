package database

import (
	"context"
	"fmt"
)

// Stats is the aggregate view backing the dashboard.
type Stats struct {
	RoutersTotal   int64
	RoutersOnline  int64
	RoutersOffline int64
	RoutersUnknown int64
	SessionsOpen   int64
	Vouchers       VoucherStats
}

// Dashboard collects every counter the landing page needs in one round trip per
// table.
func (db *DB) Dashboard(ctx context.Context) (Stats, error) {
	var stats Stats

	byStatus, err := db.Routers().CountByStatus(ctx)
	if err != nil {
		return Stats{}, err
	}
	stats.RoutersOnline = byStatus[RouterStatusOnline]
	stats.RoutersOffline = byStatus[RouterStatusOffline]
	stats.RoutersUnknown = byStatus[RouterStatusUnknown]
	for _, count := range byStatus {
		stats.RoutersTotal += count
	}

	if stats.SessionsOpen, err = db.Sessions().CountOpen(ctx, 0); err != nil {
		return Stats{}, err
	}
	if stats.Vouchers, err = db.Vouchers().Stats(ctx, 0); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// Check runs a lightweight self test used by the /healthz endpoint.
func (db *DB) Check(ctx context.Context) error {
	if err := db.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	var one int
	if err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations LIMIT 1`).Scan(&one); err != nil {
		return fmt.Errorf("database: schema not initialised: %w", err)
	}
	return nil
}
