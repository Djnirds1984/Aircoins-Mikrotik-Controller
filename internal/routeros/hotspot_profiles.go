package routeros

import (
	"context"
	"errors"
	"fmt"
)

// UserProfileSpec is a RouterOS hotspot user profile. In this panel a "Plan"
// maps one-to-one onto a user profile, because rate-limit, shared-users and
// session-timeout are profile level settings and cannot be set per user.
type UserProfileSpec struct {
	Name string

	RateLimit        string // e.g. "10M/10M"
	SharedUsers      string // e.g. "1", "3" or "unlimited"
	SessionTimeout   string // e.g. "1h", "1d"
	IdleTimeout      string
	KeepaliveTimeout string
	AddressList      string

	AddMacCookie     *bool
	TransparentProxy *bool

	OnLogin  string
	OnLogout string
}

// FindUserProfileByName returns the profile with the given name, or nil.
func FindUserProfileByName(ctx context.Context, t Transport, name string) (*HotspotUserProfile, error) {
	profiles, err := ReadHotspotUserProfiles(ctx, t)
	if err != nil {
		return nil, err
	}
	for i := range profiles {
		if profiles[i].Name == name {
			return &profiles[i], nil
		}
	}
	return nil, nil
}

// AddUserProfile creates a hotspot user profile.
func AddUserProfile(ctx context.Context, t Transport, spec UserProfileSpec) error {
	if spec.Name == "" {
		return errors.New("add user profile: name is required")
	}
	args := append([]string{"=name=" + spec.Name}, spec.args()...)
	if _, err := t.Run(ctx, "/ip/hotspot/user/profile/add", args...); err != nil {
		return fmt.Errorf("add hotspot user profile %q: %w", spec.Name, err)
	}
	return nil
}

// SetUserProfile updates a hotspot user profile by .id.
func SetUserProfile(ctx context.Context, t Transport, id string, spec UserProfileSpec) error {
	if id == "" {
		return errors.New("set user profile: id is required")
	}
	args := append([]string{"=.id=" + id}, spec.args()...)
	if _, err := t.Run(ctx, "/ip/hotspot/user/profile/set", args...); err != nil {
		return fmt.Errorf("set hotspot user profile %q: %w", spec.Name, err)
	}
	return nil
}

// EnsureUserProfile creates the profile when missing and updates it otherwise,
// which is what makes plan synchronisation idempotent and re-runnable.
func EnsureUserProfile(ctx context.Context, t Transport, spec UserProfileSpec) (created bool, err error) {
	existing, err := FindUserProfileByName(ctx, t, spec.Name)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return true, AddUserProfile(ctx, t, spec)
	}
	return false, SetUserProfile(ctx, t, existing.ID, spec)
}

// args renders settable attributes, skipping empty values.
func (s UserProfileSpec) args() []string {
	args := make([]string, 0, 12)
	add := func(key, value string) {
		if value != "" {
			args = append(args, "="+key+"="+value)
		}
	}
	add("rate-limit", s.RateLimit)
	add("shared-users", s.SharedUsers)
	add("session-timeout", s.SessionTimeout)
	add("idle-timeout", s.IdleTimeout)
	add("keepalive-timeout", s.KeepaliveTimeout)
	add("address-list", s.AddressList)
	add("on-login", s.OnLogin)
	add("on-logout", s.OnLogout)
	if s.AddMacCookie != nil {
		args = append(args, "=add-mac-cookie="+boolWord(*s.AddMacCookie))
	}
	if s.TransparentProxy != nil {
		args = append(args, "=transparent-proxy="+boolWord(*s.TransparentProxy))
	}
	return args
}

func boolWord(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
