package routeros

import "context"

// WalledGardenEntry is one row of /ip/hotspot/walled-garden/print (HTTP rules).
// These rules match on the Host header, so they only work for plain HTTP.
type WalledGardenEntry struct {
	ID         string
	DstHost    string
	DstAddress string
	Action     string
	Server     string
}

// IPBinding is one row of /ip/hotspot/ip-binding/print. type=bypassed devices
// skip the portal entirely, which is how free/whitelisted devices are handled
// when no RADIUS server is in play.
type IPBinding struct {
	ID         string
	MACAddress string
	Address    string
	ToAddress  string
	Server     string
	Type       string
	Disabled   bool
}

// ReadWalledGarden runs /ip/hotspot/walled-garden/print (HTTP host rules).
// Pass query pairs such as "?dst-host=example.com" to filter server side.
func ReadWalledGarden(ctx context.Context, t Transport, query ...string) ([]WalledGardenEntry, error) {
	reply, err := t.Run(ctx, "/ip/hotspot/walled-garden/print", query...)
	if err != nil {
		if isMissingMenu(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]WalledGardenEntry, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, WalledGardenEntry{
			ID:         row[".id"],
			DstHost:    row["dst-host"],
			DstAddress: row["dst-address"],
			Action:     row["action"],
			Server:     row["server"],
		})
	}
	return out, nil
}

// ReadIPBindings runs /ip/hotspot/ip-binding/print.
func ReadIPBindings(ctx context.Context, t Transport, query ...string) ([]IPBinding, error) {
	reply, err := t.Run(ctx, "/ip/hotspot/ip-binding/print", query...)
	if err != nil {
		if isMissingMenu(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]IPBinding, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, IPBinding{
			ID:         row[".id"],
			MACAddress: row["mac-address"],
			Address:    row["address"],
			ToAddress:  row["to-address"],
			Server:     row["server"],
			Type:       row["type"],
			Disabled:   ParseBool(row["disabled"]),
		})
	}
	return out, nil
}
