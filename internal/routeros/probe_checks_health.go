package routeros

import (
	"context"
	"fmt"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

const (
	// probeScratchHost is the throwaway walled-garden entry used to prove the
	// API account can actually manage the hotspot.
	probeScratchHost = "aircoins-probe.invalid"

	// bytesPerHotspotUser is a conservative config-size estimate used to budget
	// how many just-in-time hotspot users a router's free flash can hold.
	bytesPerHotspotUser = 1024

	maxEstimatedUsers    = 50000
	minRecommendedUsers  = 500
	clockToleranceSecond = 60
)

// checkWrite proves the account can create and remove hotspot configuration. It
// is opt-in because it mutates the router, and it always cleans up after itself.
func (p *prober) checkWrite(ctx context.Context) {
	p.check(ctx, "write", "Write permission", func(ctx context.Context) (domain.CheckStatus, string, string) {
		if !p.allow {
			return domain.StatusSkip,
				"skipped: write test not requested",
				"Enable the write test to confirm this account can provision hotspot users and walled-garden entries."
		}
		if !p.rep.Caps.HotspotMenuPresent {
			return domain.StatusSkip, "skipped: the hotspot menu is unavailable", ""
		}

		id, err := AddWalledGardenHost(ctx, p.t, probeScratchHost, "allow", "")
		if err != nil {
			p.rep.Caps.APIWritable = false
			return domain.StatusFail,
				"the account could not create a walled-garden entry: " + err.Error(),
				"Log in as a user in the full group, or grant this group the write, api and policy permissions it needs."
		}
		p.rep.Caps.APIWritable = true

		cleanupErr := RemoveWalledGarden(ctx, p.t, id)
		if cleanupErr != nil && id == "" {
			// Some RouterOS builds omit =ret= on add, so locate the entry.
			if existing, findErr := FindWalledGardenHost(ctx, p.t, probeScratchHost); findErr == nil && existing != nil {
				cleanupErr = RemoveWalledGarden(ctx, p.t, existing.ID)
			}
		}
		if cleanupErr != nil {
			return domain.StatusWarn,
				"created a scratch walled-garden entry but could not remove it: " + cleanupErr.Error(),
				fmt.Sprintf("Remove it manually: /ip hotspot walled-garden remove [find dst-host=%s]", probeScratchHost)
		}

		return domain.StatusPass, "the account can create and remove hotspot configuration", ""
	})
}

// checkCapacity budgets how many just-in-time hotspot users fit in free flash.
func (p *prober) checkCapacity(ctx context.Context) {
	p.check(ctx, "capacity", "Flash headroom for hotspot users", func(ctx context.Context) (domain.CheckStatus, string, string) {
		free := p.rep.Caps.FreeHDDSpace
		if free <= 0 {
			return domain.StatusWarn, "free flash could not be determined",
				"Confirm the API account may read /system/resource."
		}

		capacity := int(free / bytesPerHotspotUser)
		if capacity > maxEstimatedUsers {
			capacity = maxEstimatedUsers
		}
		p.rep.Caps.EstimatedUserCapacity = capacity

		if capacity < minRecommendedUsers {
			return domain.StatusWarn,
				fmt.Sprintf("%s free flash, roughly %d hotspot users", FormatBytes(free), capacity),
				"Vouchers are created on the router only when redeemed and removed once exhausted. Clean up exhausted vouchers, or spread vouchers over more routers."
		}
		return domain.StatusPass,
			fmt.Sprintf("%s free flash, roughly %d just-in-time hotspot users", FormatBytes(free), capacity),
			""
	})
}

// checkClock compares the device clock with the panel, because session timeouts
// and uptime limits are enforced against the device clock.
func (p *prober) checkClock(ctx context.Context) {
	p.check(ctx, "clock", "Clock agreement", func(ctx context.Context) (domain.CheckStatus, string, string) {
		clock, err := ReadClock(ctx, p.t)
		if err != nil {
			return domain.StatusWarn, "could not read /system/clock: " + err.Error(), ""
		}

		caps := &p.rep.Caps
		caps.Timezone = clock.TimeZoneName

		deviceUTC, parseErr := deviceUTC(clock)
		if parseErr != nil {
			return domain.StatusWarn,
				fmt.Sprintf("could not parse the device clock (%s %s)", clock.Date, clock.Time),
				"Check /system/clock/print on the device."
		}

		offset := int(deviceUTC.Sub(p.now()).Seconds())
		caps.ClockOffsetSec = offset

		ntp, _ := ReadNTP(ctx, p.t)
		ntpNote := ""
		if ntp.Present && !ntp.Enabled {
			ntpNote = "; the NTP client is disabled"
		}

		if abs(offset) > clockToleranceSecond {
			return domain.StatusWarn,
				fmt.Sprintf("the router clock differs from this panel by %d second(s)%s", offset, ntpNote),
				"Enable NTP on the router (/system ntp client set enabled=yes) so uptime and data limits are enforced accurately."
		}
		if ntp.Present && !ntp.Enabled {
			return domain.StatusWarn,
				fmt.Sprintf("clocks agree within %d second(s), but the NTP client is disabled", offset),
				"Enable NTP to keep the clocks aligned: /system ntp client set enabled=yes."
		}
		return domain.StatusPass,
			fmt.Sprintf("device time agrees with the panel within %d second(s) (time zone %s)",
				offset, fallback(clock.TimeZoneName, "unset")),
			""
	})
}

// deviceUTC converts a RouterOS local clock reading into UTC.
func deviceUTC(clock Clock) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", clock.Date+" "+clock.Time, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.Add(-time.Duration(clock.GMTOffsetSeconds) * time.Second), nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
