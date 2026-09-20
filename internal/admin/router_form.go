package admin

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// routerForm is the parsed create/edit form.
type routerForm struct {
	ID     int64
	Name   string
	Notes  string
	Enable bool

	Host      string
	APIPort   int
	APITLS    bool
	APIUser   string
	APISecret string

	FTPHost   string
	FTPPort   int
	FTPUser   string
	FTPSecret string

	// AllowWrite opts into the mutating check during a probe.
	AllowWrite bool
	// ProbeToken is the signed proof that the endpoint was tested.
	ProbeToken string
	// SkipVerification lets an unreachable router be stored for later setup.
	SkipVerification bool
}

// Credentials renders the form values as probe credentials.
func (f *routerForm) Credentials() domain.RouterCredentials {
	return domain.RouterCredentials{
		Host:     f.Host,
		Port:     f.APIPort,
		TLS:      f.APITLS,
		User:     f.APIUser,
		Password: f.APISecret,
	}
}

// parseRouterForm reads the create/edit form.
func parseRouterForm(r *http.Request) (*routerForm, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("read form: %w", err)
	}

	f := &routerForm{
		Name:             strings.TrimSpace(r.PostFormValue("name")),
		Notes:            strings.TrimSpace(r.PostFormValue("notes")),
		Host:             strings.TrimSpace(r.PostFormValue("host")),
		APIUser:          strings.TrimSpace(r.PostFormValue("api_user")),
		APISecret:        r.PostFormValue("api_password"),
		FTPHost:          strings.TrimSpace(r.PostFormValue("ftp_host")),
		FTPUser:          strings.TrimSpace(r.PostFormValue("ftp_user")),
		FTPSecret:        r.PostFormValue("ftp_password"),
		APIPort:          intFormValue(r, "api_port", 8728),
		FTPPort:          intFormValue(r, "ftp_port", 21),
		APITLS:           r.PostFormValue("api_tls") != "",
		Enable:           r.PostFormValue("disabled") == "",
		AllowWrite:       r.PostFormValue("allow_write") != "",
		ProbeToken:       r.PostFormValue("probe_token"),
		SkipVerification: r.PostFormValue("skip_verification") != "",
	}

	if raw := r.PostFormValue("id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid router id %q", raw)
		}
		f.ID = id
	}
	return f, nil
}

// validate checks the form fields that do not need a network round trip.
func (f *routerForm) validate() error {
	switch {
	case f.Name == "":
		return fmt.Errorf("give the router a name so it is recognisable in lists")
	case f.Host == "":
		return fmt.Errorf("enter the router address")
	case net.ParseIP(f.Host) == nil && !isHostname(f.Host):
		return fmt.Errorf("%q is not a valid IPv4 address or host name", f.Host)
	case f.APIPort < 1 || f.APIPort > 65535:
		return fmt.Errorf("the API port must be between 1 and 65535")
	case f.APIUser == "":
		return fmt.Errorf("enter the API username")
	}
	if f.FTPPort < 0 || f.FTPPort > 65535 {
		return fmt.Errorf("the FTP port must be between 1 and 65535")
	}
	return nil
}

// isHostname applies a deliberately loose check: the device may be addressed by
// a DNS name, and the connection test is the real validation.
func isHostname(s string) bool {
	if len(s) > 253 || strings.ContainsAny(s, " \t/\\") {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
	}
	return true
}

// intFormValue reads an integer form field with a default.
func intFormValue(r *http.Request, key string, def int) int {
	raw := strings.TrimSpace(r.PostFormValue(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// routerListView is the data for the router list page.
type routerListView struct {
	Routers    []domain.Router
	Healthy    int
	Offline    int
	Unverified int
}

// routerFormView is the data for the create and edit pages.
type routerFormView struct {
	IsNew      bool
	Router     *domain.Router
	Error      string
	Notice     string
	Report     *domain.ProbeReport
	Token      string
	PanelURL   string
	AllowWrite bool
}

// routerDetailView is the data for the router detail page.
type routerDetailView struct {
	Router    *domain.Router
	Report    *domain.ProbeReport
	Servers   []domain.HotspotServerCache
	PortalURL string
	StubHint  string
}

// Default ports offered by the create form.
const (
	defaultAPIPort = 8728
	defaultFTPPort = 21
)

// toRouter converts parsed form values into a domain router. Secrets are copied
// straight through: an empty string means "keep the stored value" for updates.
func (f *routerForm) toRouter() *domain.Router {
	return &domain.Router{
		ID:        f.ID,
		Name:      f.Name,
		Notes:     f.Notes,
		Enabled:   f.Enable,
		Host:      f.Host,
		APIPort:   f.APIPort,
		APITLS:    f.APITLS,
		APIUser:   f.APIUser,
		APISecret: f.APISecret,
		FTPHost:   f.FTPHost,
		FTPPort:   f.FTPPort,
		FTPUser:   f.FTPUser,
		FTPSecret: f.FTPSecret,
	}
}

// itoa renders an int64 for URLs.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
