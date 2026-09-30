package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// expirySweeper does the controller's two periodic housekeeping passes: it
// flags lapsed vouchers every 5 minutes until released, so the badges stay
// honest even on an idle controller, and it releases coin balances that were
// topped up but never connected.
//
// The coin pass matters for the piso Wi-Fi boxes: a customer who walks away
// from the coin slot would otherwise leave a balance that the next person on
// the same DHCP lease inherits for free.
func expirySweeper(db *database.DB, logger *slog.Logger, coinIdle time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			n, err := db.Vouchers().SyncExpired(ctx, time.Now())
			if err != nil {
				logger.Error("voucher expiry sweep failed", "error", err)
			} else if n > 0 {
				logger.Info("voucher expiry sweep", "expired", n)
			}

			// Only run the coin pass when the feature is configured. With the
			// default of 20 minutes the window is shorter than the tick, so a
			// balance is released between one sweep and the next, never much
			// later than the operator promised the customer.
			if coinIdle > 0 {
				released, err := db.Coins().ExpireIdle(ctx, coinIdle, time.Now())
				if err != nil {
					logger.Error("coin credit sweep failed", "error", err)
				} else if released > 0 {
					logger.Info("coin credit sweep", "released", released)
				}
			}

			cancel()
		}
	}
}
