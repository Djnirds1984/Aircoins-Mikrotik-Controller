package handlers

// RouterOS hotspot configuration layer: the server entries of /ip/hotspot, the
// server profiles of /ip/hotspot/profile, the user profiles of
// /ip/hotspot/user/profile and the two walled garden tables.
//
// The property names come from the RouterOS 7 CLI reference. Arguments are
// always built through rosWriter so the forms in the Network tab behave the same
// way on every build:
//
//   - a blank field is omitted, which on create means "use the device default"
//     and on update means "leave what is already there";
//   - the comment is the one exception and is always sent, because an empty
//     comment is a valid value and clearing one is a deliberate action;
//   - boolean flags are always sent so a checkbox can turn a setting back off;
//   - timeouts accept the RouterOS symbolic value "none" to clear them.
//
// A property the local build does not know about is rejected by RouterOS with
// "unknown parameter"; addROSObject and setROSObject drop that one argument
// and retry (logging the drop at Warn so the operator knows the setting was
// not applied) so a version-skewed property cannot block the whole write, and
// routerErrorHint() still explains whatever error is left.

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Argument building
// ---------------------------------------------------------------------------

// rosWriter accumulates RouterOS API arguments in the order they are sent.
type rosWriter struct {
	args []string
}

// set appends one argument verbatim, without trimming the value.
func (w *rosWriter) set(name, value string) {
	w.args = append(w.args, "="+name+"="+value)
}

// opt appends an argument only when the operator supplied a value, so a blank
// field asks RouterOS to keep its own default.
func (w *rosWriter) opt(name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		w.set(name, value)
	}
}

// text always sends the value. It is meant for properties where an empty value
// is meaningful, such as the comment, so that clearing one works.
func (w *rosWriter) text(name, value string) {
	w.set(name, strings.TrimSpace(value))
}

// flag always sends a boolean so the device can turn a setting off as well as on.
func (w *rosWriter) flag(name string, value bool) {
	if value {
		w.set(name, "yes")
		return
	}
	w.set(name, "no")
}

// validROSTime reports whether value looks like a RouterOS time interval such
// as "10m", "1h30m", "01:00:00", "1d2h" or the symbolic value "none".
func validROSTime(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return true
	}
	if _, err := time.ParseDuration(value); err == nil {
		return true
	}
	// RouterOS also accepts day and week units, which time.ParseDuration
	// does not understand, plus the "hh:mm:ss" form.
	for _, r := range value {
		if !strings.ContainsRune("0123456789wdhms:", r) {
			return false
		}
	}
	return true
}

// splitROSList splits a comma separated RouterOS list such as the login-by set,
// dropping empty entries so the UI never renders a stray comma.
func splitROSList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// joinROSList is the inverse of splitROSList, used to send a multi value set
// back to the device.
func joinROSList(values []string) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return strings.Join(cleaned, ",")
}

// rosFlagSet reports whether a RouterOS item flag such as "default" or
// "dynamic" is set. Depending on the build RouterOS returns item flags either
// as "=flag=true" or as a bare "=flag=", so a present key with a value that is
// not explicitly false counts as set.
func rosFlagSet(row map[string]string, name string) bool {
	value, present := row[name]
	if !present {
		return false
	}
	if strings.TrimSpace(value) == "" {
		return true
	}
	return parseRouterOSBool(value)
}

// ---------------------------------------------------------------------------
// Shared CRUD plumbing
// ---------------------------------------------------------------------------

// stripUnknownParameter removes the argument RouterOS refused in an
// "unknown parameter <name>" rejection. It returns the remaining arguments,
// the name that was dropped and whether anything was actually removed: when
// the error is about something else, or names a property this request never
// sent, ok is false and the caller must surface the original error unchanged.
// ok guarantees the returned slice is shorter, which is what bounds the retry
// loops in addROSObject and setROSObject.
func stripUnknownParameter(args []string, err error) ([]string, string, bool) {
	name := unknownParameterName(err.Error())
	if name == "" {
		return args, "", false
	}
	prefix := "=" + name + "="
	kept := make([]string, 0, len(args))
	removed := false
	for _, arg := range args {
		if !removed && strings.HasPrefix(arg, prefix) {
			removed = true
			continue
		}
		kept = append(kept, arg)
	}
	if !removed {
		return args, "", false
	}
	return kept, name, true
}

// unknownParameterName digs the property name out of device prose such as
// "unknown parameter comment Bad Request at host:port (/ip/hotspot/add)" and
// returns "" when the message is about something else. Separator and quote
// styles vary between the binary API trap and the REST detail, so each word
// after the marker is trimmed before it is taken as the name.
func unknownParameterName(message string) string {
	const marker = "unknown parameter"
	lowered := strings.ToLower(message)
	at := strings.Index(lowered, marker)
	if at < 0 {
		return ""
	}
	for _, word := range strings.Fields(lowered[at+len(marker):]) {
		if name := strings.Trim(word, ":='\"`.,()"); name != "" {
			return name
		}
	}
	return ""
}

// addROSObject creates an object and returns the id RouterOS assigned to it.
//
// RouterOS builds differ in which properties a menu accepts. When the device
// answers "unknown parameter <name>", the named argument is dropped and the
// command retried, so one property this build does not know cannot block the
// whole write; the drop is logged because the setting was not applied.
func (c *MikrotikClient) addROSObject(ctx context.Context, menu string, args []string) (string, error) {
	for {
		reply, err := c.Run(ctx, menu+"/add", args...)
		if err == nil {
			return reply.ID(), nil
		}
		stripped, name, ok := stripUnknownParameter(args, err)
		if !ok || len(stripped) == 0 {
			return "", err
		}
		c.log.Warn("device rejected a parameter, retrying without it",
			"menu", menu, "parameter", name, "error", err)
		args = stripped
	}
}

// setROSObject updates a single object by id. Like addROSObject it retries
// without a property this build reports as unknown.
func (c *MikrotikClient) setROSObject(ctx context.Context, menu, id string, args []string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("handlers: an object id is required")
	}
	for {
		full := make([]string, 0, len(args)+1)
		full = append(full, "=.id="+id)
		full = append(full, args...)
		_, err := c.Run(ctx, menu+"/set", full...)
		if err == nil {
			return nil
		}
		stripped, name, ok := stripUnknownParameter(args, err)
		if !ok || len(stripped) == 0 {
			return err
		}
		c.log.Warn("device rejected a parameter, retrying without it",
			"menu", menu, "parameter", name, "error", err)
		args = stripped
	}
}

// removeROSObject deletes an object, treating an already missing object as
// success so a double click cannot produce a confusing error.
func (c *MikrotikClient) removeROSObject(ctx context.Context, menu, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("handlers: an object id is required")
	}
	if _, err := c.Run(ctx, menu+"/remove", "=.id="+id); err != nil {
		if errors.Is(err, ErrRouterNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// setROSFlag toggles one boolean property without disturbing the others.
func (c *MikrotikClient) setROSFlag(ctx context.Context, menu, id, property string, value bool) error {
	flag := "no"
	if value {
		flag = "yes"
	}
	return c.setROSObject(ctx, menu, id, []string{"=" + property + "=" + flag})
}

// rosRow reads a single object by id.
func (c *MikrotikClient) rosRow(ctx context.Context, menu, id string) (map[string]string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("handlers: an object id is required")
	}
	reply, err := c.Run(ctx, menu+"/print", "=.id="+id)
	if err != nil {
		return nil, err
	}
	if row := reply.First(); row != nil {
		return row, nil
	}
	return nil, ErrRouterNotFound
}

// ---------------------------------------------------------------------------
// /ip/hotspot : the hotspot servers
// ---------------------------------------------------------------------------

// hotspotServerMenu is the RouterOS menu holding the hotspot servers.
const hotspotServerMenu = "/ip/hotspot"

// HotspotServer is one entry of /ip/hotspot/print: a hotspot a client can log
// in to, bound to one or more interfaces.
type HotspotServer struct {
	ID               string
	Name             string
	Interface        string
	AddressPool      string
	Profile          string
	IdleTimeout      string
	KeepaliveTimeout string
	LoginTimeout     string
	AddressesPerMAC  string
	Disabled         bool
	Comment          string
	// IPOfDNSName and ProxyStatus are read only properties.
	IPOfDNSName string
	ProxyStatus string
}

// Interfaces splits the comma separated interface list RouterOS reports.
func (s HotspotServer) Interfaces() []string {
	return splitROSList(s.Interface)
}

// HotspotServerSpec carries the writable properties of a hotspot server.
type HotspotServerSpec struct {
	Name             string
	Interface        string
	AddressPool      string
	Profile          string
	IdleTimeout      string
	KeepaliveTimeout string
	LoginTimeout     string
	AddressesPerMAC  string
	Comment          string
	Disabled         bool
}

// args renders the spec as RouterOS arguments.
func (s HotspotServerSpec) args() []string {
	w := &rosWriter{}
	w.opt("name", s.Name)
	w.opt("interface", s.Interface)
	w.opt("address-pool", s.AddressPool)
	w.opt("profile", s.Profile)
	w.opt("idle-timeout", s.IdleTimeout)
	w.opt("keepalive-timeout", s.KeepaliveTimeout)
	w.opt("login-timeout", s.LoginTimeout)
	w.opt("addresses-per-mac", s.AddressesPerMAC)
	w.text("comment", s.Comment)
	w.flag("disabled", s.Disabled)
	return w.args
}

// hotspotServerFromRow maps one API row onto the typed struct.
func hotspotServerFromRow(row map[string]string) HotspotServer {
	return HotspotServer{
		ID:               row[".id"],
		Name:             row["name"],
		Interface:        row["interface"],
		AddressPool:      row["address-pool"],
		Profile:          row["profile"],
		IdleTimeout:      row["idle-timeout"],
		KeepaliveTimeout: row["keepalive-timeout"],
		LoginTimeout:     row["login-timeout"],
		AddressesPerMAC:  row["addresses-per-mac"],
		Disabled:         parseRouterOSBool(row["disabled"]),
		Comment:          row["comment"],
		IPOfDNSName:      row["ip-of-dns-name"],
		ProxyStatus:      row["proxy-status"],
	}
}

// HotspotServers lists the hotspot servers configured on a device.
func (c *MikrotikClient) HotspotServers(ctx context.Context) ([]HotspotServer, error) {
	reply, err := c.Run(ctx, hotspotServerMenu+"/print")
	if err != nil {
		return nil, err
	}
	servers := make([]HotspotServer, 0, len(reply.Re))
	for _, row := range reply.Re {
		servers = append(servers, hotspotServerFromRow(row))
	}
	return servers, nil
}

// GetHotspotServer reads one hotspot server by id.
func (c *MikrotikClient) GetHotspotServer(ctx context.Context, id string) (HotspotServer, error) {
	row, err := c.rosRow(ctx, hotspotServerMenu, id)
	if err != nil {
		return HotspotServer{}, err
	}
	return hotspotServerFromRow(row), nil
}

// AddHotspotServer creates a hotspot server and returns its new id.
func (c *MikrotikClient) AddHotspotServer(ctx context.Context, spec HotspotServerSpec) (string, error) {
	return c.addROSObject(ctx, hotspotServerMenu, spec.args())
}

// SetHotspotServer applies the spec to an existing hotspot server.
func (c *MikrotikClient) SetHotspotServer(ctx context.Context, id string, spec HotspotServerSpec) error {
	return c.setROSObject(ctx, hotspotServerMenu, id, spec.args())
}

// SetHotspotServerDisabled turns one hotspot server on or off without touching
// its other settings.
func (c *MikrotikClient) SetHotspotServerDisabled(ctx context.Context, id string, disabled bool) error {
	return c.setROSFlag(ctx, hotspotServerMenu, id, "disabled", disabled)
}

// RemoveHotspotServer deletes a hotspot server.
func (c *MikrotikClient) RemoveHotspotServer(ctx context.Context, id string) error {
	return c.removeROSObject(ctx, hotspotServerMenu, id)
}

// IPPoolNames lists the /ip/pool names so the hotspot server form can offer the
// pools that already exist instead of asking for a name to be typed.
func (c *MikrotikClient) IPPoolNames(ctx context.Context) ([]string, error) {
	reply, err := c.Run(ctx, "/ip/pool/print", "=.proplist=name")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reply.Re))
	for _, row := range reply.Re {
		if name := strings.TrimSpace(row["name"]); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// ---------------------------------------------------------------------------
// /ip/hotspot/profile : the hotspot server profiles
// ---------------------------------------------------------------------------

// hotspotServerProfileMenu is the RouterOS menu holding the server profiles.
// These are the settings a hotspot server inherits: the address it advertises,
// how clients may log in and whether RADIUS is used.
const hotspotServerProfileMenu = "/ip/hotspot/profile"

// HotspotServerProfile is one entry of /ip/hotspot/profile/print.
type HotspotServerProfile struct {
	ID                    string
	Name                  string
	HotspotAddress        string
	DNSName               string
	HTMLDirectory         string
	HTMLDirectoryOverride string
	InstallHotspotQueue   bool
	RateLimit             string
	HTTPProxy             string
	SMTPServer            string
	LoginBy               string
	MACAuthMode           string
	MACAuthPassword       string
	HTTPCookieLifetime    string
	SSLCertificate        string
	SplitUserDomain       bool
	TrialUptimeLimit      string
	TrialUptimeReset      string
	TrialUserProfile      string
	UseRadius             bool
	RadiusAccounting      bool
	RadiusInterimUpdate   string
	NASPortType           string
	RadiusDefaultDomain   string
	RadiusLocationID      string
	RadiusLocationName    string
	RadiusMACFormat       string
	// IsDefault marks the built in profile every server falls back to.
	IsDefault bool
}

// LoginMethods splits the comma separated login-by set, for example
// "cookie,http-chap", into a slice the form can iterate.
func (p HotspotServerProfile) LoginMethods() []string {
	return splitROSList(p.LoginBy)
}

// loginByMethods are every value RouterOS accepts in the login-by set, in the
// order the form lists them.
var loginByMethods = []string{
	"mac", "cookie", "http-chap", "https", "http-pap", "trial", "mac-cookie",
}

// radiusMACFormats are the documented values of radius-mac-format.
var radiusMACFormats = []string{
	"XX:XX:XX:XX:XX:XX", "XXXX:XXXX:XXXX", "XXXXXX:XXXXXX",
	"XX-XX-XX-XX-XX-XX", "XXXXXX-XXXXXX", "XXXXXXXXXXXX", "XX XX XX XX XX XX",
}

// nasPortTypes are the documented values of nas-port-type.
var nasPortTypes = []string{"ethernet", "cable", "wireless-802.11"}

// macAuthModes are the documented values of mac-auth-mode.
var macAuthModes = []string{"mac-as-username", "mac-as-username-and-password"}

// HotspotServerProfileSpec carries the writable properties of a server profile.
type HotspotServerProfileSpec struct {
	Name                  string
	HotspotAddress        string
	DNSName               string
	HTMLDirectory         string
	HTMLDirectoryOverride string
	InstallHotspotQueue   bool
	RateLimit             string
	HTTPProxy             string
	SMTPServer            string
	LoginBy               string
	MACAuthMode           string
	MACAuthPassword       string
	HTTPCookieLifetime    string
	SSLCertificate        string
	SplitUserDomain       bool
	TrialUptimeLimit      string
	TrialUptimeReset      string
	TrialUserProfile      string
	UseRadius             bool
	RadiusAccounting      bool
	RadiusInterimUpdate   string
	NASPortType           string
	RadiusDefaultDomain   string
	RadiusLocationID      string
	RadiusLocationName    string
	RadiusMACFormat       string
}

// args renders the spec as RouterOS arguments.
func (s HotspotServerProfileSpec) args() []string {
	w := &rosWriter{}
	w.opt("name", s.Name)
	w.opt("hotspot-address", s.HotspotAddress)
	w.opt("dns-name", s.DNSName)
	w.opt("html-directory", s.HTMLDirectory)
	w.opt("html-directory-override", s.HTMLDirectoryOverride)
	w.flag("install-hotspot-queue", s.InstallHotspotQueue)
	w.opt("rate-limit", s.RateLimit)
	w.opt("http-proxy", s.HTTPProxy)
	w.opt("smtp-server", s.SMTPServer)
	w.opt("login-by", s.LoginBy)
	w.opt("mac-auth-mode", s.MACAuthMode)
	w.opt("mac-auth-password", s.MACAuthPassword)
	w.opt("http-cookie-lifetime", s.HTTPCookieLifetime)
	w.opt("ssl-certificate", s.SSLCertificate)
	w.flag("split-user-domain", s.SplitUserDomain)
	w.opt("trial-uptime-limit", s.TrialUptimeLimit)
	w.opt("trial-uptime-reset", s.TrialUptimeReset)
	w.opt("trial-user-profile", s.TrialUserProfile)
	w.flag("use-radius", s.UseRadius)
	w.flag("radius-accounting", s.RadiusAccounting)
	w.opt("radius-interim-update", s.RadiusInterimUpdate)
	w.opt("nas-port-type", s.NASPortType)
	w.opt("radius-default-domain", s.RadiusDefaultDomain)
	w.opt("radius-location-id", s.RadiusLocationID)
	w.opt("radius-location-name", s.RadiusLocationName)
	w.opt("radius-mac-format", s.RadiusMACFormat)
	return w.args
}

// hotspotServerProfileFromRow maps one API row onto the typed struct.
func hotspotServerProfileFromRow(row map[string]string) HotspotServerProfile {
	return HotspotServerProfile{
		ID:                    row[".id"],
		Name:                  row["name"],
		HotspotAddress:        row["hotspot-address"],
		DNSName:               row["dns-name"],
		HTMLDirectory:         row["html-directory"],
		HTMLDirectoryOverride: row["html-directory-override"],
		InstallHotspotQueue:   parseRouterOSBool(row["install-hotspot-queue"]),
		RateLimit:             row["rate-limit"],
		HTTPProxy:             row["http-proxy"],
		SMTPServer:            row["smtp-server"],
		LoginBy:               row["login-by"],
		MACAuthMode:           row["mac-auth-mode"],
		MACAuthPassword:       row["mac-auth-password"],
		HTTPCookieLifetime:    row["http-cookie-lifetime"],
		SSLCertificate:        row["ssl-certificate"],
		SplitUserDomain:       parseRouterOSBool(row["split-user-domain"]),
		TrialUptimeLimit:      row["trial-uptime-limit"],
		TrialUptimeReset:      row["trial-uptime-reset"],
		TrialUserProfile:      row["trial-user-profile"],
		UseRadius:             parseRouterOSBool(row["use-radius"]),
		RadiusAccounting:      parseRouterOSBool(row["radius-accounting"]),
		RadiusInterimUpdate:   row["radius-interim-update"],
		NASPortType:           row["nas-port-type"],
		RadiusDefaultDomain:   row["radius-default-domain"],
		RadiusLocationID:      row["radius-location-id"],
		RadiusLocationName:    row["radius-location-name"],
		RadiusMACFormat:       row["radius-mac-format"],
		IsDefault:             rosFlagSet(row, "default"),
	}
}

// HotspotServerProfiles lists the server profiles configured on a device.
func (c *MikrotikClient) HotspotServerProfiles(ctx context.Context) ([]HotspotServerProfile, error) {
	reply, err := c.Run(ctx, hotspotServerProfileMenu+"/print")
	if err != nil {
		return nil, err
	}
	profiles := make([]HotspotServerProfile, 0, len(reply.Re))
	for _, row := range reply.Re {
		profiles = append(profiles, hotspotServerProfileFromRow(row))
	}
	return profiles, nil
}

// GetHotspotServerProfile reads one server profile by id.
func (c *MikrotikClient) GetHotspotServerProfile(ctx context.Context, id string) (HotspotServerProfile, error) {
	row, err := c.rosRow(ctx, hotspotServerProfileMenu, id)
	if err != nil {
		return HotspotServerProfile{}, err
	}
	return hotspotServerProfileFromRow(row), nil
}

// AddHotspotServerProfile creates a server profile and returns its new id.
func (c *MikrotikClient) AddHotspotServerProfile(ctx context.Context, spec HotspotServerProfileSpec) (string, error) {
	return c.addROSObject(ctx, hotspotServerProfileMenu, spec.args())
}

// SetHotspotServerProfile applies the spec to an existing server profile.
func (c *MikrotikClient) SetHotspotServerProfile(ctx context.Context, id string, spec HotspotServerProfileSpec) error {
	return c.setROSObject(ctx, hotspotServerProfileMenu, id, spec.args())
}

// RemoveHotspotServerProfile deletes a server profile.
func (c *MikrotikClient) RemoveHotspotServerProfile(ctx context.Context, id string) error {
	return c.removeROSObject(ctx, hotspotServerProfileMenu, id)
}

// ---------------------------------------------------------------------------
// /ip/hotspot/user/profile : the hotspot user profiles
// ---------------------------------------------------------------------------

// hotspotUserProfileMenu is the RouterOS menu holding the user profiles.
const hotspotUserProfileMenu = "/ip/hotspot/user/profile"

// openStatusPages and queuePositions are the documented enum values of
// open-status-page and insert-queue-before.
var (
	openStatusPages = []string{"http-login", "always"}
	queuePositions  = []string{"bottom", "first"}
)

// hotspotProfileFromRow maps one user profile row onto the typed struct.
func hotspotProfileFromRow(row map[string]string) HotspotProfile {
	return HotspotProfile{
		ID:                row[".id"],
		Name:              row["name"],
		SharedUsers:       int(parseInt64(row["shared-users"])),
		RateLimit:         row["rate-limit"],
		SessionTime:       row["session-timeout"],
		IdleTimeout:       row["idle-timeout"],
		Keepalive:         row["keepalive-timeout"],
		AddressPool:       row["address-pool"],
		StatusAutorefresh: row["status-autorefresh"],
		AddMACCookie:      parseRouterOSBool(row["add-mac-cookie"]),
		MACCookieTimeout:  row["mac-cookie-timeout"],
		AddressList:       row["address-list"],
		IncomingFilter:    row["incoming-filter"],
		OutgoingFilter:    row["outgoing-filter"],
		IncomingPktMark:   row["incoming-packet-mark"],
		OutgoingPktMark:   row["outgoing-packet-mark"],
		QueueType:         row["queue-type"],
		ParentQueue:       row["parent-queue"],
		InsertQueueBefore: row["insert-queue-before"],
		OnLogin:           row["on-login"],
		OnLogout:          row["on-logout"],
		TransparentProxy:  parseRouterOSBool(row["transparent-proxy"]),
		OpenStatusPage:    row["open-status-page"],
		Advertise:         parseRouterOSBool(row["advertise"]),
		AdvertiseURL:      row["advertise-url"],
		AdvertiseInterval: row["advertise-interval"],
		AdvertiseTimeout:  row["advertise-timeout"],
		IsDefault:         rosFlagSet(row, "default"),
	}
}

// HotspotUserProfileSpec carries the writable properties of a user profile.
//
// SharedUsers stays a string because RouterOS expresses "no limit" with the
// symbolic value "unlimited" rather than a number.
type HotspotUserProfileSpec struct {
	Name              string
	SharedUsers       string
	AddressPool       string
	RateLimit         string
	SessionTime       string
	IdleTimeout       string
	Keepalive         string
	StatusAutorefresh string
	AddMACCookie      bool
	MACCookieTimeout  string
	AddressList       string
	IncomingFilter    string
	OutgoingFilter    string
	IncomingPktMark   string
	OutgoingPktMark   string
	QueueType         string
	ParentQueue       string
	InsertQueueBefore string
	OnLogin           string
	OnLogout          string
	TransparentProxy  bool
	OpenStatusPage    string
	Advertise         bool
	AdvertiseURL      string
	AdvertiseInterval string
	AdvertiseTimeout  string
}

// args renders the spec as RouterOS arguments.
func (s HotspotUserProfileSpec) args() []string {
	w := &rosWriter{}
	w.opt("name", s.Name)
	w.opt("shared-users", s.SharedUsers)
	w.opt("address-pool", s.AddressPool)
	w.opt("rate-limit", s.RateLimit)
	w.opt("session-timeout", s.SessionTime)
	w.opt("idle-timeout", s.IdleTimeout)
	w.opt("keepalive-timeout", s.Keepalive)
	w.opt("status-autorefresh", s.StatusAutorefresh)
	w.flag("add-mac-cookie", s.AddMACCookie)
	w.opt("mac-cookie-timeout", s.MACCookieTimeout)
	w.opt("address-list", s.AddressList)
	w.opt("incoming-filter", s.IncomingFilter)
	w.opt("outgoing-filter", s.OutgoingFilter)
	w.opt("incoming-packet-mark", s.IncomingPktMark)
	w.opt("outgoing-packet-mark", s.OutgoingPktMark)
	w.opt("queue-type", s.QueueType)
	w.opt("parent-queue", s.ParentQueue)
	w.opt("insert-queue-before", s.InsertQueueBefore)
	w.opt("on-login", s.OnLogin)
	w.opt("on-logout", s.OnLogout)
	w.flag("transparent-proxy", s.TransparentProxy)
	w.opt("open-status-page", s.OpenStatusPage)
	w.flag("advertise", s.Advertise)
	w.opt("advertise-url", s.AdvertiseURL)
	w.opt("advertise-interval", s.AdvertiseInterval)
	w.opt("advertise-timeout", s.AdvertiseTimeout)
	return w.args
}

// GetHotspotProfile reads one user profile by id.
func (c *MikrotikClient) GetHotspotProfile(ctx context.Context, id string) (HotspotProfile, error) {
	row, err := c.rosRow(ctx, hotspotUserProfileMenu, id)
	if err != nil {
		return HotspotProfile{}, err
	}
	return hotspotProfileFromRow(row), nil
}

// AddHotspotProfile creates a user profile and returns its new id.
func (c *MikrotikClient) AddHotspotProfile(ctx context.Context, spec HotspotUserProfileSpec) (string, error) {
	return c.addROSObject(ctx, hotspotUserProfileMenu, spec.args())
}

// SetHotspotProfile applies the spec to an existing user profile.
func (c *MikrotikClient) SetHotspotProfile(ctx context.Context, id string, spec HotspotUserProfileSpec) error {
	return c.setROSObject(ctx, hotspotUserProfileMenu, id, spec.args())
}

// RemoveHotspotProfile deletes a user profile.
func (c *MikrotikClient) RemoveHotspotProfile(ctx context.Context, id string) error {
	return c.removeROSObject(ctx, hotspotUserProfileMenu, id)
}

// ---------------------------------------------------------------------------
// /ip/hotspot/walled-garden : the walled garden rules
// ---------------------------------------------------------------------------

// walledGardenMenu holds the host, port, path and method rules that decide what
// a client may reach before it has logged in.
const walledGardenMenu = "/ip/hotspot/walled-garden"

// WalledGardenEntry is one entry of /ip/hotspot/walled-garden/print.
type WalledGardenEntry struct {
	ID         string
	Server     string
	SrcAddress string
	Method     string
	DstHost    string
	DstPort    string
	Path       string
	Action     string
	Comment    string
	Disabled   bool
	// DstAddress and Hits are read only: RouterOS resolves dst-host itself and
	// counts the matches.
	DstAddress string
	Hits       int64
}

// Allows reports whether the rule lets matching traffic through.
func (e WalledGardenEntry) Allows() bool { return !strings.EqualFold(e.Action, "deny") }

// Methods splits the comma separated method set.
func (e WalledGardenEntry) Methods() []string { return splitROSList(e.Method) }

// WalledGardenSpec carries the writable properties of a walled garden rule.
// dst-address is deliberately absent: RouterOS derives it from dst-host, so
// sending it back would be rejected.
type WalledGardenSpec struct {
	Server     string
	SrcAddress string
	Method     string
	DstHost    string
	DstPort    string
	Path       string
	Action     string
	Comment    string
	Disabled   bool
}

// args renders the spec as RouterOS arguments.
func (s WalledGardenSpec) args() []string {
	w := &rosWriter{}
	w.opt("server", s.Server)
	w.opt("src-address", s.SrcAddress)
	w.opt("method", s.Method)
	w.opt("dst-host", s.DstHost)
	w.opt("dst-port", s.DstPort)
	w.opt("path", s.Path)
	w.opt("action", s.Action)
	w.text("comment", s.Comment)
	w.flag("disabled", s.Disabled)
	return w.args
}

// walledGardenFromRow maps one API row onto the typed struct.
func walledGardenFromRow(row map[string]string) WalledGardenEntry {
	return WalledGardenEntry{
		ID:         row[".id"],
		Server:     row["server"],
		SrcAddress: row["src-address"],
		Method:     row["method"],
		DstHost:    row["dst-host"],
		DstPort:    row["dst-port"],
		Path:       row["path"],
		Action:     row["action"],
		Comment:    row["comment"],
		Disabled:   parseRouterOSBool(row["disabled"]),
		DstAddress: row["dst-address"],
		Hits:       parseInt64(row["hits"]),
	}
}

// WalledGardenEntries lists the walled garden rules of a device.
func (c *MikrotikClient) WalledGardenEntries(ctx context.Context) ([]WalledGardenEntry, error) {
	reply, err := c.Run(ctx, walledGardenMenu+"/print")
	if err != nil {
		return nil, err
	}
	entries := make([]WalledGardenEntry, 0, len(reply.Re))
	for _, row := range reply.Re {
		entries = append(entries, walledGardenFromRow(row))
	}
	return entries, nil
}

// GetWalledGardenEntry reads one walled garden rule by id.
func (c *MikrotikClient) GetWalledGardenEntry(ctx context.Context, id string) (WalledGardenEntry, error) {
	row, err := c.rosRow(ctx, walledGardenMenu, id)
	if err != nil {
		return WalledGardenEntry{}, err
	}
	return walledGardenFromRow(row), nil
}

// AddWalledGardenEntry creates a walled garden rule and returns its new id.
func (c *MikrotikClient) AddWalledGardenEntry(ctx context.Context, spec WalledGardenSpec) (string, error) {
	return c.addROSObject(ctx, walledGardenMenu, spec.args())
}

// SetWalledGardenEntry applies the spec to an existing rule.
func (c *MikrotikClient) SetWalledGardenEntry(ctx context.Context, id string, spec WalledGardenSpec) error {
	return c.setROSObject(ctx, walledGardenMenu, id, spec.args())
}

// SetWalledGardenEntryDisabled turns one rule on or off.
func (c *MikrotikClient) SetWalledGardenEntryDisabled(ctx context.Context, id string, disabled bool) error {
	return c.setROSFlag(ctx, walledGardenMenu, id, "disabled", disabled)
}

// RemoveWalledGardenEntry deletes a walled garden rule.
func (c *MikrotikClient) RemoveWalledGardenEntry(ctx context.Context, id string) error {
	return c.removeROSObject(ctx, walledGardenMenu, id)
}

// ---------------------------------------------------------------------------
// /ip/hotspot/walled-garden/ip : the IP based walled garden rules
// ---------------------------------------------------------------------------

// walledGardenIPMenu holds the address and protocol based rules. Unlike the
// host rules above, this menu accepts dst-address directly and uses
// accept/drop/reject instead of allow/deny.
const walledGardenIPMenu = "/ip/hotspot/walled-garden/ip"

// WalledGardenIPAction is the action value that lets traffic through.
const WalledGardenIPAction = "accept"

// walledGardenIPActions are the documented values of the IP rule action.
var walledGardenIPActions = []string{"accept", "drop", "reject"}

// WalledGardenIPEntry is one entry of /ip/hotspot/walled-garden/ip/print.
type WalledGardenIPEntry struct {
	ID             string
	Server         string
	SrcAddress     string
	DstAddress     string
	DstHost        string
	Protocol       string
	DstPort        string
	SrcAddressList string
	DstAddressList string
	Action         string
	Comment        string
	Disabled       bool
}

// Allows reports whether the rule lets matching traffic through.
func (e WalledGardenIPEntry) Allows() bool {
	return strings.EqualFold(e.Action, WalledGardenIPAction)
}

// WalledGardenIPSpec carries the writable properties of an IP walled garden
// rule.
type WalledGardenIPSpec struct {
	Server         string
	SrcAddress     string
	DstAddress     string
	DstHost        string
	Protocol       string
	DstPort        string
	SrcAddressList string
	DstAddressList string
	Action         string
	Comment        string
	Disabled       bool
}

// args renders the spec as RouterOS arguments.
func (s WalledGardenIPSpec) args() []string {
	w := &rosWriter{}
	w.opt("server", s.Server)
	w.opt("src-address", s.SrcAddress)
	w.opt("dst-address", s.DstAddress)
	w.opt("dst-host", s.DstHost)
	w.opt("protocol", s.Protocol)
	w.opt("dst-port", s.DstPort)
	w.opt("src-address-list", s.SrcAddressList)
	w.opt("dst-address-list", s.DstAddressList)
	w.opt("action", s.Action)
	w.text("comment", s.Comment)
	w.flag("disabled", s.Disabled)
	return w.args
}

// walledGardenIPFromRow maps one API row onto the typed struct.
func walledGardenIPFromRow(row map[string]string) WalledGardenIPEntry {
	return WalledGardenIPEntry{
		ID:             row[".id"],
		Server:         row["server"],
		SrcAddress:     row["src-address"],
		DstAddress:     row["dst-address"],
		DstHost:        row["dst-host"],
		Protocol:       row["protocol"],
		DstPort:        row["dst-port"],
		SrcAddressList: row["src-address-list"],
		DstAddressList: row["dst-address-list"],
		Action:         row["action"],
		Comment:        row["comment"],
		Disabled:       parseRouterOSBool(row["disabled"]),
	}
}

// WalledGardenIPEntries lists the IP walled garden rules of a device.
func (c *MikrotikClient) WalledGardenIPEntries(ctx context.Context) ([]WalledGardenIPEntry, error) {
	reply, err := c.Run(ctx, walledGardenIPMenu+"/print")
	if err != nil {
		return nil, err
	}
	entries := make([]WalledGardenIPEntry, 0, len(reply.Re))
	for _, row := range reply.Re {
		entries = append(entries, walledGardenIPFromRow(row))
	}
	return entries, nil
}

// GetWalledGardenIPEntry reads one IP walled garden rule by id.
func (c *MikrotikClient) GetWalledGardenIPEntry(ctx context.Context, id string) (WalledGardenIPEntry, error) {
	row, err := c.rosRow(ctx, walledGardenIPMenu, id)
	if err != nil {
		return WalledGardenIPEntry{}, err
	}
	return walledGardenIPFromRow(row), nil
}

// AddWalledGardenIPEntry creates an IP walled garden rule.
func (c *MikrotikClient) AddWalledGardenIPEntry(ctx context.Context, spec WalledGardenIPSpec) (string, error) {
	return c.addROSObject(ctx, walledGardenIPMenu, spec.args())
}

// SetWalledGardenIPEntry applies the spec to an existing IP rule.
func (c *MikrotikClient) SetWalledGardenIPEntry(ctx context.Context, id string, spec WalledGardenIPSpec) error {
	return c.setROSObject(ctx, walledGardenIPMenu, id, spec.args())
}

// SetWalledGardenIPEntryDisabled turns one IP rule on or off.
func (c *MikrotikClient) SetWalledGardenIPEntryDisabled(ctx context.Context, id string, disabled bool) error {
	return c.setROSFlag(ctx, walledGardenIPMenu, id, "disabled", disabled)
}

// RemoveWalledGardenIPEntry deletes an IP walled garden rule.
func (c *MikrotikClient) RemoveWalledGardenIPEntry(ctx context.Context, id string) error {
	return c.removeROSObject(ctx, walledGardenIPMenu, id)
}
