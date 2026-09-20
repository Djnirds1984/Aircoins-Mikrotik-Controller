package routeros

import "context"

// HotspotServer is one row of /ip/hotspot/print.
type HotspotServer struct {
	ID          string
	Name        string
	Interface   string
	AddressPool string
	Profile     string
	Disabled    bool
}

// HotspotProfile is one row of /ip/hotspot/profile/print. LoginBy matters most:
// panel driven logins are PAP, so it must contain "http-pap".
type HotspotProfile struct {
	ID                    string
	Name                  string
	LoginBy               string
	HotspotAddress        string
	DNSName               string
	HTMLDirectory         string
	HTMLDirectoryOverride string
	HTTPCookieLifetime    string
	NASPortType           string
	UseRadius             bool
	RadiusAccounting      bool
	MacAuthMode           string
	TrialUptimeLimit      string
	TrialUserProfile      string
}

// HotspotUserProfile is one row of /ip/hotspot/user/profile/print. Rate limits,
// device limits and session timeouts live here, which is why every panel "Plan"
// maps onto a RouterOS user profile.
type HotspotUserProfile struct {
	ID               string
	Name             string
	RateLimit        string
	SharedUsers      string
	SessionTimeout   string
	IdleTimeout      string
	KeepaliveTimeout string
	AddressList      string
	AddMacCookie     bool
	TransparentProxy bool
	OnLogin          string
	OnLogout         string
}

// ReadHotspotServers runs /ip/hotspot/print.
func ReadHotspotServers(ctx context.Context, t Transport) ([]HotspotServer, error) {
	reply, err := t.Run(ctx, "/ip/hotspot/print")
	if err != nil {
		return nil, err
	}
	out := make([]HotspotServer, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, HotspotServer{
			ID:          row[".id"],
			Name:        row["name"],
			Interface:   row["interface"],
			AddressPool: row["address-pool"],
			Profile:     row["profile"],
			Disabled:    ParseBool(row["disabled"]),
		})
	}
	return out, nil
}

// ReadHotspotProfiles runs /ip/hotspot/profile/print.
func ReadHotspotProfiles(ctx context.Context, t Transport) ([]HotspotProfile, error) {
	reply, err := t.Run(ctx, "/ip/hotspot/profile/print")
	if err != nil {
		return nil, err
	}
	out := make([]HotspotProfile, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, HotspotProfile{
			ID:                    row[".id"],
			Name:                  row["name"],
			LoginBy:               row["login-by"],
			HotspotAddress:        row["hotspot-address"],
			DNSName:               row["dns-name"],
			HTMLDirectory:         row["html-directory"],
			HTMLDirectoryOverride: row["html-directory-override"],
			HTTPCookieLifetime:    row["http-cookie-lifetime"],
			NASPortType:           row["nas-port-type"],
			UseRadius:             ParseBool(row["use-radius"]),
			RadiusAccounting:      ParseBool(row["radius-accounting"]),
			MacAuthMode:           row["mac-auth-mode"],
			TrialUptimeLimit:      row["trial-uptime-limit"],
			TrialUserProfile:      row["trial-user-profile"],
		})
	}
	return out, nil
}

// ReadHotspotUserProfiles runs /ip/hotspot/user/profile/print.
func ReadHotspotUserProfiles(ctx context.Context, t Transport) ([]HotspotUserProfile, error) {
	reply, err := t.Run(ctx, "/ip/hotspot/user/profile/print")
	if err != nil {
		return nil, err
	}
	out := make([]HotspotUserProfile, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, HotspotUserProfile{
			ID:               row[".id"],
			Name:             row["name"],
			RateLimit:        row["rate-limit"],
			SharedUsers:      row["shared-users"],
			SessionTimeout:   row["session-timeout"],
			IdleTimeout:      row["idle-timeout"],
			KeepaliveTimeout: row["keepalive-timeout"],
			AddressList:      row["address-list"],
			AddMacCookie:     ParseBool(row["add-mac-cookie"]),
			TransparentProxy: ParseBool(row["transparent-proxy"]),
			OnLogin:          row["on-login"],
			OnLogout:         row["on-logout"],
		})
	}
	return out, nil
}

// SupportsPAPLogin reports whether a login-by list allows plaintext (PAP)
// authentication, which is what an API driven login sends.
func SupportsPAPLogin(loginBy string) bool {
	return containsMethod(loginBy, "http-pap")
}

// SupportsHTTPChap reports whether the http-chap method is enabled.
func SupportsHTTPChap(loginBy string) bool {
	return containsMethod(loginBy, "http-chap")
}

func containsMethod(list, method string) bool {
	for _, part := range splitList(list) {
		if part == method {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := trimSpace(s[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
