package routeros

import "context"

// Service is one row of /ip/service/print, used to check whether the API
// service is enabled before we try to use it.
type Service struct {
	Name     string
	Port     int
	Disabled bool
	Address  string
	CertName string
}

// DeviceMode is /system/device-mode/print. The feature list differs between
// RouterOS versions, so it is kept as a raw map.
type DeviceMode struct {
	Present bool
	Mode    string
	Flags   map[string]string
}

// Allows reports the device-mode flag for a feature. The second result is
// false when the flag is not reported by this RouterOS version, i.e. unknown.
func (d DeviceMode) Allows(feature string) (bool, bool) {
	if d.Flags == nil {
		return true, false
	}
	raw, ok := d.Flags[feature]
	if !ok {
		return true, false
	}
	return ParseBool(raw), true
}

// DHCPNetwork is /ip/dhcp-server/network/print.
type DHCPNetwork struct {
	Address   string
	Gateway   string
	DNSServer string
	Netmask   string
	Comment   string
}

// Pool is /ip/pool/print.
type Pool struct {
	Name    string
	Ranges  string
	Comment string
}

// ReadServices runs /ip/service/print.
func ReadServices(ctx context.Context, t Transport) ([]Service, error) {
	reply, err := t.Run(ctx, "/ip/service/print")
	if err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, Service{
			Name:     row["name"],
			Port:     int(ParseInt(row["port"])),
			Disabled: ParseBool(row["disabled"]),
			Address:  row["address"],
			CertName: row["certificate"],
		})
	}
	return out, nil
}

// ReadDeviceMode runs /system/device-mode/print, tolerating a missing menu
// (RouterOS 6 has no device-mode).
func ReadDeviceMode(ctx context.Context, t Transport) (DeviceMode, error) {
	reply, err := t.Run(ctx, "/system/device-mode/print")
	if err != nil {
		if isMissingMenu(err) {
			return DeviceMode{Present: false}, nil
		}
		return DeviceMode{}, err
	}
	row := reply.First()
	if row == nil {
		return DeviceMode{Present: false}, nil
	}

	flags := make(map[string]string, len(row))
	for k, v := range row {
		if k == ".id" {
			continue
		}
		flags[k] = v
	}
	return DeviceMode{Present: true, Mode: row["mode"], Flags: flags}, nil
}

// ReadDHCPNetworks runs /ip/dhcp-server/network/print.
func ReadDHCPNetworks(ctx context.Context, t Transport) ([]DHCPNetwork, error) {
	reply, err := t.Run(ctx, "/ip/dhcp-server/network/print")
	if err != nil {
		if isMissingMenu(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]DHCPNetwork, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, DHCPNetwork{
			Address:   row["address"],
			Gateway:   row["gateway"],
			DNSServer: row["dns-server"],
			Netmask:   row["netmask"],
			Comment:   row["comment"],
		})
	}
	return out, nil
}

// ReadPools runs /ip/pool/print.
func ReadPools(ctx context.Context, t Transport) ([]Pool, error) {
	reply, err := t.Run(ctx, "/ip/pool/print")
	if err != nil {
		if isMissingMenu(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Pool, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, Pool{
			Name:    row["name"],
			Ranges:  row["ranges"],
			Comment: row["comment"],
		})
	}
	return out, nil
}
