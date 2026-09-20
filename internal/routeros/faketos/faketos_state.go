package faketos

import (
	"fmt"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
)

// activeRows returns the simulated active session table.
func (d *Device) activeRows(args []string) []map[string]string {
	session := map[string]string{
		".id":         "*1",
		"user":        "DEMO-0001",
		"address":     "10.5.50.23",
		"mac-address": "AA:BB:CC:DD:EE:01",
		"server":      "hotspot1",
		"login-by":    "http-pap",
		"uptime":      "12m30s",
		"bytes-in":    "1048576",
		"bytes-out":   "4194304",
	}
	return filterRows([]map[string]string{session}, args)
}

// hostRows returns the simulated hotspot host table.
func (d *Device) hostRows(args []string) []map[string]string {
	entry := map[string]string{
		".id":         "*1",
		"mac-address": "AA:BB:CC:DD:EE:01",
		"address":     "10.5.50.23",
		"to-address":  "10.5.50.23",
		"server":      "hotspot1",
		"authorized":  "true",
		"bypassed":    "false",
		"found-by":    "DHCP",
		"uptime":      "12m30s",
		"idle-time":   "3s",
	}
	return filterRows([]map[string]string{entry}, args)
}

// userAdd mimics /ip/hotspot/user/add, including the =ret= id reply.
func (d *Device) userAdd(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	name := argValue(args, "name")
	if name == "" {
		return nil, &routeros.DeviceError{Message: "name is required", Category: "1"}
	}
	for _, u := range d.users {
		if u["name"] == name {
			return nil, &routeros.DeviceError{Message: "hotspot user " + name + " already exists"}
		}
	}

	id := fmt.Sprintf("*%d", d.nextID)
	d.nextID++

	row := map[string]string{".id": id, "name": name, "uptime": "0s", "bytes-in": "0", "bytes-out": "0"}
	for _, key := range []string{
		"profile", "server", "address", "mac-address",
		"limit-uptime", "limit-bytes-in", "limit-bytes-out", "limit-bytes-total",
	} {
		if v := argValue(args, key); v != "" {
			row[key] = v
		}
	}
	d.users = append(d.users, row)
	if pw := argValue(args, "password"); pw != "" {
		d.passwords[name] = pw
	}
	return &routeros.Reply{Done: map[string]string{"ret": id}}, nil
}

// userSet mimics /ip/hotspot/user/set.
func (d *Device) userSet(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	id := argValue(args, ".id")
	for _, u := range d.users {
		if u[".id"] != id {
			continue
		}
		for _, key := range []string{
			"profile", "server", "address", "mac-address",
			"limit-uptime", "limit-bytes-in", "limit-bytes-out", "limit-bytes-total", "disabled",
		} {
			if v := argValue(args, key); v != "" {
				u[key] = v
			}
		}
		if pw := argValue(args, "password"); pw != "" {
			d.passwords[u["name"]] = pw
		}
		return empty(), nil
	}
	return nil, &routeros.DeviceError{Message: "no such item", Category: "0"}
}

// userRemove mimics /ip/hotspot/user/remove.
func (d *Device) userRemove(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	id := argValue(args, ".id")
	kept := d.users[:0:0]
	removed := false
	for _, u := range d.users {
		if u[".id"] == id {
			delete(d.passwords, u["name"])
			removed = true
			continue
		}
		kept = append(kept, u)
	}
	if !removed {
		return nil, &routeros.DeviceError{Message: "no such item", Category: "0"}
	}
	d.users = kept
	return empty(), nil
}

// activeLogin mimics /ip/hotspot/active/login, including the "unknown host IP"
// trap that the real device returns when the client has no hotspot host entry.
func (d *Device) activeLogin(args []string) (*routeros.Reply, error) {
	user := argValue(args, "user")
	ip := argValue(args, "ip")
	mac := argValue(args, "mac-address")

	if user == "" {
		return nil, &routeros.DeviceError{Message: "user is required", Category: "1"}
	}
	if ip == "" && mac == "" {
		return nil, &routeros.DeviceError{
			Message:  "unknown host IP 0.0.0.0",
			Category: "4",
			Sentinel: routeros.ErrHostUnknown,
		}
	}

	known := false
	for _, h := range d.hostRows(nil) {
		if (ip != "" && h["address"] == ip) || (mac != "" && h["mac-address"] == mac) {
			known = true
			break
		}
	}
	if !known {
		return nil, &routeros.DeviceError{
			Message:  "unknown host IP " + ip,
			Category: "4",
			Sentinel: routeros.ErrHostUnknown,
		}
	}

	password, exists := d.passwords[user]
	if !exists || (password != "" && password != argValue(args, "password")) {
		return nil, &routeros.DeviceError{
			Message:  "invalid user name or password",
			Category: "4",
			Sentinel: routeros.ErrAuth,
		}
	}
	return empty(), nil
}

// profileAdd mimics /ip/hotspot/user/profile/add.
func (d *Device) profileAdd(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	name := argValue(args, "name")
	if name == "" {
		return nil, &routeros.DeviceError{Message: "name is required", Category: "1"}
	}
	for _, p := range d.profiles {
		if p["name"] == name {
			return nil, &routeros.DeviceError{Message: "hotspot user profile " + name + " already exists"}
		}
	}

	id := fmt.Sprintf("*%d", d.nextID)
	d.nextID++

	row := map[string]string{".id": id, "name": name}
	for _, key := range []string{
		"rate-limit", "shared-users", "session-timeout", "idle-timeout",
		"keepalive-timeout", "address-list", "on-login", "on-logout",
		"add-mac-cookie", "transparent-proxy",
	} {
		if v := argValue(args, key); v != "" {
			row[key] = v
		}
	}
	d.profiles = append(d.profiles, row)
	return &routeros.Reply{Done: map[string]string{"ret": id}}, nil
}

// profileSet mimics /ip/hotspot/user/profile/set.
func (d *Device) profileSet(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	id := argValue(args, ".id")
	for _, p := range d.profiles {
		if p[".id"] != id {
			continue
		}
		for _, key := range []string{
			"rate-limit", "shared-users", "session-timeout", "idle-timeout",
			"keepalive-timeout", "address-list", "on-login", "on-logout",
			"add-mac-cookie", "transparent-proxy",
		} {
			if v := argValue(args, key); v != "" {
				p[key] = v
			}
		}
		return empty(), nil
	}
	return nil, &routeros.DeviceError{Message: "no such item", Category: "0"}
}

// walledAdd mimics /ip/hotspot/walled-garden/add.
func (d *Device) walledAdd(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	host := argValue(args, "dst-host")
	if host == "" {
		return nil, &routeros.DeviceError{Message: "dst-host is required", Category: "1"}
	}
	id := fmt.Sprintf("*%d", d.nextID)
	d.nextID++

	d.walled = append(d.walled, map[string]string{
		".id":      id,
		"dst-host": host,
		"action":   argValue(args, "action"),
		"server":   argValue(args, "server"),
	})
	return &routeros.Reply{Done: map[string]string{"ret": id}}, nil
}

// walledRemove mimics /ip/hotspot/walled-garden/remove.
func (d *Device) walledRemove(args []string) (*routeros.Reply, error) {
	if d.opts.ReadOnly {
		return nil, notPermitted()
	}

	id := argValue(args, ".id")
	kept := d.walled[:0:0]
	removed := false
	for _, w := range d.walled {
		if w[".id"] == id {
			removed = true
			continue
		}
		kept = append(kept, w)
	}
	if !removed {
		return nil, &routeros.DeviceError{Message: "no such item", Category: "0"}
	}
	d.walled = kept
	return empty(), nil
}
