package domain

import "time"

// CheckStatus is the outcome of a single probe check.
type CheckStatus string

// Probe check outcomes.
const (
	StatusPass CheckStatus = "PASS"
	StatusWarn CheckStatus = "WARN"
	StatusFail CheckStatus = "FAIL"
	StatusSkip CheckStatus = "SKIP"
)

// ProbeCheck is one step of the connection test ladder.
type ProbeCheck struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Status     CheckStatus `json:"status"`
	Message    string      `json:"message"`
	Fix        string      `json:"fix,omitempty"`
	DurationMS int64       `json:"duration_ms"`
}

// Capabilities is the RouterOS feature inventory gathered during a probe.
// Every field is a fact read from the device, never an assumption.
type Capabilities struct {
	Identity     string `json:"identity,omitempty"`
	ROSVersion   string `json:"ros_version,omitempty"`
	Arch         string `json:"arch,omitempty"`
	BoardName    string `json:"board_name,omitempty"`
	Model        string `json:"model,omitempty"`
	LicenseLevel string `json:"license_level,omitempty"`

	HotspotMenuPresent  bool     `json:"hotspot_menu_present"`
	HotspotServers      []string `json:"hotspot_servers,omitempty"`
	HotspotUserProfiles []string `json:"hotspot_user_profiles,omitempty"`
	HotspotUsers        int      `json:"hotspot_users"`
	ActiveSessions      int      `json:"active_sessions"`
	Hosts               int      `json:"hosts"`

	APIEnabled    bool `json:"api_enabled"`
	APISSLEnabled bool `json:"api_ssl_enabled"`
	APIWritable   bool `json:"api_writable"`

	DeviceModeProbed         bool `json:"device_mode_probed"`
	DeviceModeHotspotAllowed bool `json:"device_mode_hotspot_allowed"`

	// LoginMethods is the login-by list of the first hotspot profile. It must
	// contain http-pap for panel driven API logins to succeed.
	LoginMethods string `json:"login_methods,omitempty"`

	FreeHDDSpace   int64  `json:"free_hdd_space"`
	FreeMemory     int64  `json:"free_memory"`
	CPUCount       int    `json:"cpu_count"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	ClockOffsetSec int    `json:"clock_offset_sec"`
	Timezone       string `json:"timezone,omitempty"`

	// EstimatedUserCapacity is a rough budget for just-in-time provisioned
	// hotspot users, derived from free flash.
	EstimatedUserCapacity int `json:"estimated_user_capacity"`
}

// ProbeReport is the full outcome of a connection test.
type ProbeReport struct {
	RouterID   int64        `json:"router_id,omitempty"`
	RouterName string       `json:"router_name,omitempty"`
	Address    string       `json:"address"`
	ProbedAt   time.Time    `json:"probed_at"`
	LatencyMS  int64        `json:"latency_ms"`
	Result     CheckStatus  `json:"result"`
	Checks     []ProbeCheck `json:"checks"`
	Caps       Capabilities `json:"capabilities"`

	// Token is a short lived HMAC binding this report to the probed endpoint.
	// It must accompany the create/update request for the save to be accepted.
	Token string `json:"token,omitempty"`
}

// Failed returns the checks that did not pass, for compact summaries.
func (r *ProbeReport) Failed() []ProbeCheck {
	var out []ProbeCheck
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			out = append(out, c)
		}
	}
	return out
}

// HasFailures reports whether the probe produced any FAIL check.
func (r *ProbeReport) HasFailures() bool { return len(r.Failed()) > 0 }
