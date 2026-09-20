package faketos

import (
	"context"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
)

// Run implements routeros.Transport.
func (d *Device) Run(_ context.Context, command string, args ...string) (*routeros.Reply, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if strings.HasPrefix(command, "/ip/hotspot") && d.opts.SkipHotspot {
		return nil, noSuchCommand()
	}

	switch command {
	case "/system/identity/print":
		return rows(map[string]string{".id": "*1", "name": "Aircoins-Demo"}), nil

	case "/system/resource/print":
		return rows(d.resourceRow()), nil

	case "/system/routerboard/print":
		return rows(map[string]string{
			".id":              "*1",
			"name":             d.opts.BoardName,
			"model":            d.opts.Model,
			"serial-number":    "HEX12345678",
			"current-firmware": "7.16.2",
			"factory-firmware": "6.48.6",
		}), nil

	case "/system/clock/print":
		return rows(d.clockRow()), nil

	case "/system/ntp/client/print":
		return rows(map[string]string{
			".id":     "*1",
			"enabled": "true",
			"server":  "pool.ntp.org",
			"mode":    "unicast",
		}), nil

	case "/system/license/print":
		return rows(map[string]string{".id": "*1", "level": "4"}), nil

	case "/ip/service/print":
		return rows(
			map[string]string{".id": "*1", "name": "api", "port": "8728", "disabled": "false"},
			map[string]string{".id": "*2", "name": "api-ssl", "port": "8729", "disabled": "true"},
			map[string]string{".id": "*3", "name": "www", "port": "80", "disabled": "true"},
		), nil

	case "/system/device-mode/print":
		hotspot := "yes"
		if d.opts.BlockHotspotDeviceMode {
			hotspot = "no"
		} else {
			hotspot = "yes"
		}
		return rows(map[string]string{".id": "*1", "mode": "advanced", "hotspot": hotspot}), nil

	case "/ip/hotspot/print":
		return rows(map[string]string{
			".id":          "*1",
			"name":         "hotspot1",
			"interface":    "ether3",
			"address-pool": "hs-pool-1",
			"profile":      "hsprof1",
		}), nil

	case "/ip/hotspot/profile/print":
		return rows(map[string]string{
			".id":                     "*1",
			"name":                    "hsprof1",
			"login-by":                d.opts.LoginBy,
			"hotspot-address":         "10.5.50.1",
			"dns-name":                "wifi.example.com",
			"html-directory":          "hotspot",
			"html-directory-override": "",
			"http-cookie-lifetime":    "1d",
			"use-radius":              "false",
			"radius-accounting":       "false",
		}), nil

	case "/ip/hotspot/user/profile/print":
		return rows(filterRows(d.profiles, args)...), nil

	case "/ip/hotspot/user/profile/add":
		return d.profileAdd(args)

	case "/ip/hotspot/user/profile/set":
		return d.profileSet(args)

	case "/ip/hotspot/user/print":
		return rows(filterRows(d.users, args)...), nil

	case "/ip/hotspot/user/add":
		return d.userAdd(args)

	case "/ip/hotspot/user/set":
		return d.userSet(args)

	case "/ip/hotspot/user/remove":
		return d.userRemove(args)

	case "/ip/hotspot/active/print":
		return rows(d.activeRows(args)...), nil

	case "/ip/hotspot/active/login":
		return d.activeLogin(args)

	case "/ip/hotspot/active/remove":
		return empty(), nil

	case "/ip/hotspot/host/print":
		return rows(d.hostRows(args)...), nil

	case "/ip/hotspot/walled-garden/print":
		return rows(filterRows(d.walled, args)...), nil

	case "/ip/hotspot/walled-garden/add":
		return d.walledAdd(args)

	case "/ip/hotspot/walled-garden/remove":
		return d.walledRemove(args)

	case "/ip/hotspot/ip-binding/print":
		return rows(), nil

	case "/ip/dhcp-server/network/print":
		return rows(map[string]string{
			".id":        "*1",
			"address":    "10.5.50.0/24",
			"gateway":    "10.5.50.1",
			"dns-server": "10.5.50.1",
		}), nil

	case "/ip/pool/print":
		return rows(map[string]string{
			".id":    "*1",
			"name":   "hs-pool-1",
			"ranges": "10.5.50.2-10.5.50.254",
		}), nil
	}

	return nil, noSuchCommand()
}

func noSuchCommand() error {
	return &routeros.DeviceError{
		Message:  "no such command prefix",
		Category: "0",
		Sentinel: routeros.ErrNoMenu,
	}
}

func notPermitted() error {
	return &routeros.DeviceError{
		Message:  "not enough permissions",
		Category: "3",
		Sentinel: routeros.ErrPermission,
	}
}
