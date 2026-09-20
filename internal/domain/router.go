// Package domain holds the core data types shared by the store, the RouterOS
// client and the HTTP handlers. It has no dependencies on those layers.
package domain

import (
	"net"
	"strconv"
	"time"
)

// Probe states stored on a router.
const (
	ProbeUnknown = "unknown"
	ProbePass    = "pass"
	ProbeWarn    = "warn"
	ProbeFail    = "fail"
)

// Router is a MikroTik device managed by this panel.
//
// Hotspot users are provisioned by the panel through the RouterOS API; the
// router then authenticates clients from its own local user database. The
// panel never acts as a RADIUS server.
type Router struct {
	ID   int64
	Name string

	// Management API endpoint.
	Host      string
	APIPort   int
	APITLS    bool
	APIUser   string
	APISecret string // plaintext in memory only; encrypted before persisting

	// Optional FTP endpoint used to push the portal login stub.
	FTPHost   string
	FTPPort   int
	FTPUser   string
	FTPSecret string

	PortalToken string
	StubHash    string

	// Verification state. Verified is only set by a successful probe, and the
	// probe token is bound to the endpoint the probe ran against, so a router
	// cannot be "tested" at one address and saved at another.
	Verified       bool
	VerifyOverride bool
	ProbeState     string

	Enabled bool
	Notes   string

	// Cached device facts, refreshed by probes and health checks.
	Identity        string
	Model           string
	BoardName       string
	ROSVersion      string
	Arch            string
	LicenseLevel    string
	FreeHDDSpace    int64
	UptimeSeconds   int64
	ClockOffsetSecs int
	LastSeenAt      *time.Time
	LastProbeAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Endpoint returns host:port for the management API.
func (r *Router) Endpoint() string {
	return net.JoinHostPort(r.Host, strconv.Itoa(r.APIPort))
}

// DisplayName returns a label suitable for lists and logs.
func (r *Router) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Endpoint()
}

// HasPortalToken reports whether the portal routes are available for this
// router. Tokens are minted when a router is created.
func (r *Router) HasPortalToken() bool { return r.PortalToken != "" }

// RouterCredentials is the unpersisted credential bundle used by the probe
// form and by the "test connection" endpoint.
type RouterCredentials struct {
	Host     string
	Port     int
	TLS      bool
	User     string
	Password string
}

// Endpoint returns host:port for the credentials.
func (c RouterCredentials) Endpoint() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}
