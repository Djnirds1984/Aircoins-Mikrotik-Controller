package routeros

import (
	"context"
	"errors"
	"fmt"
)

// HotspotUserSpec describes a hotspot user to create or update.
//
// RouterOS 7.24 only allows these values per user. Rate limit, shared-users and
// session-timeout are user *profile* settings, so a panel "Plan" maps onto a
// RouterOS hotspot user profile and this struct carries only per-user overrides.
type HotspotUserSpec struct {
	Name     string
	Password string
	Profile  string
	Server   string

	Address    string
	MACAddress string

	LimitUptime     string
	LimitBytesIn    string
	LimitBytesOut   string
	LimitBytesTotal string

	Disabled bool
}

// AddHotspotUser creates a hotspot user and returns its RouterOS .id.
func AddHotspotUser(ctx context.Context, t Transport, spec HotspotUserSpec) (string, error) {
	if spec.Name == "" {
		return "", errors.New("add hotspot user: name is required")
	}
	args := append([]string{"=name=" + spec.Name}, spec.setArgs()...)

	reply, err := t.Run(ctx, "/ip/hotspot/user/add", args...)
	if err != nil {
		return "", fmt.Errorf("add hotspot user %q: %w", spec.Name, err)
	}
	// /add answers with =ret=<generated id>.
	return reply.Done["ret"], nil
}

// SetHotspotUser updates an existing hotspot user identified by .id.
func SetHotspotUser(ctx context.Context, t Transport, id string, spec HotspotUserSpec) error {
	if id == "" {
		return errors.New("set hotspot user: id is required")
	}
	args := append([]string{"=.id=" + id}, spec.setArgs()...)
	if _, err := t.Run(ctx, "/ip/hotspot/user/set", args...); err != nil {
		return fmt.Errorf("set hotspot user %s: %w", spec.Name, err)
	}
	return nil
}

// RemoveHotspotUser deletes a hotspot user by .id.
func RemoveHotspotUser(ctx context.Context, t Transport, id string) error {
	if id == "" {
		return errors.New("remove hotspot user: id is required")
	}
	if _, err := t.Run(ctx, "/ip/hotspot/user/remove", "=.id="+id); err != nil {
		return fmt.Errorf("remove hotspot user %s: %w", id, err)
	}
	return nil
}

// SetHotspotUserDisabled enables or disables a user without deleting it.
func SetHotspotUserDisabled(ctx context.Context, t Transport, id string, disabled bool) error {
	value := "no"
	if disabled {
		value = "yes"
	}
	if _, err := t.Run(ctx, "/ip/hotspot/user/set", "=.id="+id, "=disabled="+value); err != nil {
		return fmt.Errorf("set hotspot user %s disabled=%s: %w", id, value, err)
	}
	return nil
}

// FindHotspotUserByName returns the user with the given name, or nil.
func FindHotspotUserByName(ctx context.Context, t Transport, name string) (*HotspotUser, error) {
	users, err := ReadHotspotUsers(ctx, t, "?name="+name)
	if err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].Name == name {
			return &users[i], nil
		}
	}
	return nil, nil
}

// setArgs renders the settable attributes, skipping empty values so that an
// update never clears a field the caller did not intend to touch.
func (s HotspotUserSpec) setArgs() []string {
	args := make([]string, 0, 10)
	add := func(key, value string) {
		if value != "" {
			args = append(args, "="+key+"="+value)
		}
	}
	add("password", s.Password)
	add("profile", s.Profile)
	add("server", s.Server)
	add("address", s.Address)
	add("mac-address", s.MACAddress)
	add("limit-uptime", s.LimitUptime)
	add("limit-bytes-in", s.LimitBytesIn)
	add("limit-bytes-out", s.LimitBytesOut)
	add("limit-bytes-total", s.LimitBytesTotal)
	if s.Disabled {
		args = append(args, "=disabled=yes")
	}
	return args
}
