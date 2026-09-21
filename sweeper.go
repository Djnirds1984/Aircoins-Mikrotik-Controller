package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// expirySweeper flags lapsed vouchers every 5 minutes until released, so the
// badges stay honest even on an idle controller.
func expirySweeper(db *database.DB, logger *slog.Logger, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			n, err := db.Vouchers().SyncExpired(ctx, time.Now())
			cancel()
			if err != nil {
				logger.Error("voucher expiry sweep failed", "error", err)
				continue
			}
			if n > 0 {
				logger.Info("voucher expiry sweep", "expired", n)
			}
		}
	}
}
