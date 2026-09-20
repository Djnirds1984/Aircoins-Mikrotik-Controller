package routeros

import (
	"context"
	"errors"
	"fmt"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// checks runs the device and capability portion of the ladder, from step 3 on.
func (p *prober) checks(ctx context.Context) {
	p.checkDevice(ctx)
	p.checkHotspot(ctx)
	p.checkLoginMethods(ctx)
	p.checkAPIService(ctx)
	p.checkDeviceMode(ctx)
	p.checkWrite(ctx)
	p.checkCapacity(ctx)
	p.checkClock(ctx)
}

func (p *prober) checkDevice(ctx context.Context) {
	p.check(ctx, "device", "Device identification", func(ctx context.Context) (domain.CheckStatus, string, string) {
		caps := &p.rep.Caps

		identity, err := ReadIdentity(ctx, p.t)
		if err != nil {
			return domain.StatusFail, "could not read /system/identity: " + err.Error(),
				"The account may be restricted. Confirm it can run /system/identity/print."
		}
		caps.Identity = identity.Name

		resource, err := ReadResource(ctx, p.t)
		if err != nil {
			return domain.StatusFail, "could not read /system/resource: " + err.Error(),
				"Confirm the API account has read access to /system."
		}
		caps.ROSVersion = resource.Version
		caps.Arch = resource.ArchitectureName
		caps.BoardName = resource.BoardName
		caps.FreeHDDSpace = resource.FreeHDDSpace
		caps.FreeMemory = resource.FreeMemory
		caps.CPUCount = resource.CPUCount
		caps.UptimeSeconds = resource.UptimeSeconds

		if board, err := ReadBoard(ctx, p.t); err == nil && board.Present {
			caps.Model = board.Model
			if caps.Model == "" {
				caps.Model = board.Name
			}
		}
		if lic, err := ReadLicense(ctx, p.t); err == nil && lic.Present {
			caps.LicenseLevel = licenseName(lic.Level)
		}

		return domain.StatusPass, fmt.Sprintf("%s - %s %s (%s), RouterOS %s",
			fallback(identity.Name, "unnamed device"),
			fallback(caps.BoardName, "unknown board"),
			fallback(caps.Model, ""),
			fallback(caps.Arch, "unknown arch"),
			fallback(caps.ROSVersion, "unknown version"),
		), ""
	})
}

func (p *prober) checkHotspot(ctx context.Context) {
	p.check(ctx, "hotspot", "Hotspot inventory", func(ctx context.Context) (domain.CheckStatus, string, string) {
		caps := &p.rep.Caps

		servers, err := ReadHotspotServers(ctx, p.t)
		if err != nil {
			caps.HotspotMenuPresent = false
			if errors.Is(err, ErrNoMenu) {
				return domain.StatusFail,
					"the /ip hotspot menu is not available on this device",
					"Install or enable the hotspot package (RouterOS 6), or check /system/device-mode on RouterOS 7."
			}
			return domain.StatusFail, "could not read /ip/hotspot: " + err.Error(),
				"Confirm the API account may read /ip/hotspot."
		}
		caps.HotspotMenuPresent = true

		for _, s := range servers {
			caps.HotspotServers = append(caps.HotspotServers, s.Name)
		}

		if profiles, err := ReadHotspotProfiles(ctx, p.t); err == nil {
			for _, pr := range profiles {
				if caps.LoginMethods == "" {
					caps.LoginMethods = pr.LoginBy
				}
			}
		}
		if userProfiles, err := ReadHotspotUserProfiles(ctx, p.t); err == nil {
			for _, up := range userProfiles {
				caps.HotspotUserProfiles = append(caps.HotspotUserProfiles, up.Name)
			}
		}
		if users, err := ReadHotspotUsers(ctx, p.t); err == nil {
			caps.HotspotUsers = len(users)
		}
		if active, err := ReadHotspotActive(ctx, p.t); err == nil {
			caps.ActiveSessions = len(active)
		}
		if hosts, err := ReadHotspotHosts(ctx, p.t); err == nil {
			caps.Hosts = len(hosts)
		}

		summary := fmt.Sprintf("%d server(s), %d hotspot user(s), %d active session(s)",
			len(caps.HotspotServers), caps.HotspotUsers, caps.ActiveSessions)

		if len(servers) == 0 {
			return domain.StatusWarn, summary + " - no hotspot server is configured yet",
				"Run the provisioning wizard to create a hotspot server, or configure one manually on the router."
		}
		return domain.StatusPass, summary, ""
	})
}

// licenseName renders a RouterOS license level number.
func licenseName(level int) string {
	switch level {
	case 0:
		return "unknown"
	case 1:
		return "Level 1"
	case 2:
		return "Level 2"
	case 3:
		return "Level 3"
	case 4:
		return "Level 4"
	case 5:
		return "Level 5"
	case 6:
		return "Level 6"
	default:
		return fmt.Sprintf("Level %d", level)
	}
}

func fallback(value, replacement string) string {
	if value == "" {
		return replacement
	}
	return value
}
