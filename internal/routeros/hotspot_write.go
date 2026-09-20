package routeros

import (
	"context"
	"errors"
	"fmt"
)

// LoginRequest drives /ip/hotspot/active/login.
//
// RouterOS accepts exactly: ip, mac-address, user, password, domain. Empty
// values are omitted because RouterOS treats an empty attribute as a syntax
// error. This is a PAP style login, so the hotspot profile's login-by list must
// contain http-pap.
type LoginRequest struct {
	User     string
	Password string
	// IP is the client address as recorded in the hotspot host table.
	IP string
	// MAC is the client MAC address. Supplying both IP and MAC is the most
	// reliable combination.
	MAC string
	// Domain is only needed when a hotspot user carries a domain.
	Domain string
}

// Args renders the RouterOS API words for this login.
func (r LoginRequest) Args() []string {
	args := make([]string, 0, 5)
	if r.User != "" {
		args = append(args, "=user="+r.User)
	}
	if r.Password != "" {
		args = append(args, "=password="+r.Password)
	}
	if r.IP != "" {
		args = append(args, "=ip="+r.IP)
	}
	if r.MAC != "" {
		args = append(args, "=mac-address="+r.MAC)
	}
	if r.Domain != "" {
		args = append(args, "=domain="+r.Domain)
	}
	return args
}

// LoginActive logs a hotspot client in from the panel.
//
// The client must already exist in the hotspot host table: fetching the portal
// page through the hotspot guarantees that. ErrHostUnknown means the host entry
// is missing, which is the most common failure in the field.
func LoginActive(ctx context.Context, t Transport, req LoginRequest) error {
	if req.User == "" {
		return errors.New("hotspot login: user is required")
	}
	if req.IP == "" && req.MAC == "" {
		return errors.New("hotspot login: either ip or mac-address is required")
	}
	if _, err := t.Run(ctx, "/ip/hotspot/active/login", req.Args()...); err != nil {
		return fmt.Errorf("hotspot login %q: %w", req.User, err)
	}
	return nil
}

// RemoveActive kicks a single active session by its .id.
func RemoveActive(ctx context.Context, t Transport, id string) error {
	if id == "" {
		return errors.New("kick: session id is required")
	}
	if _, err := t.Run(ctx, "/ip/hotspot/active/remove", "=.id="+id); err != nil {
		return fmt.Errorf("kick session %s: %w", id, err)
	}
	return nil
}

// RemoveActiveByUser kicks every session belonging to a hotspot user and
// reports how many were removed.
func RemoveActiveByUser(ctx context.Context, t Transport, user string) (int, error) {
	sessions, err := ReadHotspotActive(ctx, t, "?user="+user)
	if err != nil {
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, s := range sessions {
		if err := RemoveActive(ctx, t, s.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
