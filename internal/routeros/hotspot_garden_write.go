package routeros

import (
	"context"
	"errors"
	"fmt"
)

// Walled garden write helpers.
//
// The HTTP walled-garden matches on the Host header, so it only unblocks plain
// HTTP. HTTPS destinations need an entry in the IP walled-garden instead, which
// is why both are provided: the panel is reached over plain HTTP for the portal
// itself, and over the IP table for anything encrypted.

// AddWalledGardenHost allows a hostname before authentication and returns the
// new entry's .id. action is "allow" or "deny".
func AddWalledGardenHost(ctx context.Context, t Transport, dstHost, action, server string) (string, error) {
	if dstHost == "" {
		return "", errors.New("walled garden: dst-host is required")
	}
	if action == "" {
		action = "allow"
	}
	args := []string{"=dst-host=" + dstHost, "=action=" + action}
	if server != "" {
		args = append(args, "=server="+server)
	}

	reply, err := t.Run(ctx, "/ip/hotspot/walled-garden/add", args...)
	if err != nil {
		return "", fmt.Errorf("add walled garden host %q: %w", dstHost, err)
	}
	return reply.Done["ret"], nil
}

// RemoveWalledGarden deletes an HTTP walled-garden entry by .id.
func RemoveWalledGarden(ctx context.Context, t Transport, id string) error {
	if id == "" {
		return errors.New("walled garden: id is required")
	}
	if _, err := t.Run(ctx, "/ip/hotspot/walled-garden/remove", "=.id="+id); err != nil {
		return fmt.Errorf("remove walled garden %s: %w", id, err)
	}
	return nil
}

// FindWalledGardenHost returns the entry for an exact dst-host, or nil.
func FindWalledGardenHost(ctx context.Context, t Transport, dstHost string) (*WalledGardenEntry, error) {
	entries, err := ReadWalledGarden(ctx, t, "?dst-host="+dstHost)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].DstHost == dstHost {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// AddWalledGardenIP allows an address or subnet before authentication and
// returns the new entry's .id. action is "accept" or "drop".
func AddWalledGardenIP(ctx context.Context, t Transport, dstAddress, action, server string) (string, error) {
	if dstAddress == "" {
		return "", errors.New("ip walled garden: dst-address is required")
	}
	if action == "" {
		action = "accept"
	}
	args := []string{"=dst-address=" + dstAddress, "=action=" + action}
	if server != "" {
		args = append(args, "=server="+server)
	}

	reply, err := t.Run(ctx, "/ip/hotspot/walled-garden/ip/add", args...)
	if err != nil {
		return "", fmt.Errorf("add ip walled garden %q: %w", dstAddress, err)
	}
	return reply.Done["ret"], nil
}

// RemoveWalledGardenIP deletes an IP walled-garden entry by .id.
func RemoveWalledGardenIP(ctx context.Context, t Transport, id string) error {
	if id == "" {
		return errors.New("ip walled garden: id is required")
	}
	if _, err := t.Run(ctx, "/ip/hotspot/walled-garden/ip/remove", "=.id="+id); err != nil {
		return fmt.Errorf("remove ip walled garden %s: %w", id, err)
	}
	return nil
}

// IPBindingAdd creates a MAC based binding. bindType is "regular", "bypassed"
// or "blocked": bypassed devices skip the portal entirely, which is how free or
// whitelisted devices are handled without RADIUS.
func IPBindingAdd(ctx context.Context, t Transport, mac, bindType, server, address string) (string, error) {
	if mac == "" && address == "" {
		return "", errors.New("ip binding: mac-address or address is required")
	}
	if bindType == "" {
		bindType = "bypassed"
	}
	args := []string{"=type=" + bindType}
	if mac != "" {
		args = append(args, "=mac-address="+mac)
	}
	if address != "" {
		args = append(args, "=address="+address)
	}
	if server != "" {
		args = append(args, "=server="+server)
	}

	reply, err := t.Run(ctx, "/ip/hotspot/ip-binding/add", args...)
	if err != nil {
		return "", fmt.Errorf("add ip binding for %q: %w", mac, err)
	}
	return reply.Done["ret"], nil
}

// IPBindingRemove deletes an ip-binding entry by .id.
func IPBindingRemove(ctx context.Context, t Transport, id string) error {
	if id == "" {
		return errors.New("ip binding: id is required")
	}
	if _, err := t.Run(ctx, "/ip/hotspot/ip-binding/remove", "=.id="+id); err != nil {
		return fmt.Errorf("remove ip binding %s: %w", id, err)
	}
	return nil
}
