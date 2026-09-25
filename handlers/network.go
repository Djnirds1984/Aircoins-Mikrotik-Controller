package handlers

// Network tab: the hotspot configuration of one device, read live from the
// RouterOS API and edited in place.
//
// The page covers the menus that decide how a hotspot behaves: the servers of
// /ip/hotspot, the server profiles of /ip/hotspot/profile, the user profiles of
// /ip/hotspot/user/profile and the two walled garden tables. Every table is
// fetched from the device on each render, so what the operator sees is what the
// router is actually running, never a cached copy.
//
// Editing follows the same shape as the router inventory: a table of live rows
// plus a single editor form. The "Edit" link of a row re-renders the page with
// ?edit=<id>, which pre-fills the editor instead of forcing every row to carry
// its own form.

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"unicode"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// networkTab is one section of the Network page.
type networkTab string

const (
	tabHotspotServers networkTab = "servers"
	tabServerProfiles networkTab = "server-profiles"
	tabUserProfiles   networkTab = "user-profiles"
	tabWalledGarden   networkTab = "walled-garden"
	tabWalledGardenIP networkTab = "walled-garden-ip"
	tabPools          networkTab = "pools"
	tabVLANs          networkTab = "vlans"
)

// networkTabs is the order the page renders the sections in.
var networkTabs = []networkTab{
	tabHotspotServers,
	tabServerProfiles,
	tabUserProfiles,
	tabWalledGarden,
	tabWalledGardenIP,
	tabPools,
	tabVLANs,
}

// Label is the human readable name of a section.
func (t networkTab) Label() string {
	switch t {
	case tabServerProfiles:
		return "Server profiles"
	case tabUserProfiles:
		return "User profiles"
	case tabWalledGarden:
		return "Walled garden"
	case tabWalledGardenIP:
		return "Walled garden IP"
	case tabPools:
		return "IP pools"
	case tabVLANs:
		return "VLANs"
	default:
		return "Hotspot servers"
	}
}

// Hint explains what a section configures, shown under its heading.
func (t networkTab) Hint() string {
	switch t {
	case tabServerProfiles:
		return "Settings every hotspot server inherits from /ip/hotspot/profile: the address and DNS name it advertises, how clients may log in and whether RADIUS is used."
	case tabUserProfiles:
		return "Per user settings from /ip/hotspot/user/profile: how many devices one login may use, the speed and volume it gets and which scripts run around it. Vouchers are provisioned onto these profiles."
	case tabWalledGarden:
		return "Host, port, path and method patterns from /ip/hotspot/walled-garden that clients may reach before logging in. dst-address is resolved by RouterOS from dst-host and stays read only."
	case tabWalledGardenIP:
		return "Address and protocol rules from /ip/hotspot/walled-garden/ip. Unlike the host rules these take dst-address directly and use accept, drop or reject."
	case tabPools:
		return "Address ranges from /ip/pool that the hotspot, DHCP and PPPoE servers lease out. Pools are created here; manage their members on the device."
	case tabVLANs:
		return "Bridge VLAN entries from /interface/bridge/vlan. One entry may carry a single VLAN ID or a range such as 100-120, tagged and untagged on the ports you pick."
	default:
		return "Hotspot servers from /ip/hotspot, each bound to one or more interfaces. The interface is mandatory when creating a server."
	}
}

// tabFromRequest reads ?tab=, falling back to the first section.
func tabFromRequest(r *http.Request) networkTab {
	wanted := networkTab(strings.TrimSpace(r.URL.Query().Get("tab")))
	for _, tab := range networkTabs {
		if tab == wanted {
			return wanted
		}
	}
	return tabHotspotServers
}

// networkPath is the canonical URL of one device's Network page.
func networkPath(routerID int64, tab networkTab) string {
	return "/network/" + strconv.FormatInt(routerID, 10) + "?tab=" + string(tab)
}

// editPath keeps the current section while opening the editor of one row.
func editPath(routerID int64, tab networkTab, objectID string) string {
	return networkPath(routerID, tab) + "&edit=" + objectID
}

// networkPage backs the Network tab.
type networkPage struct {
	page
	Router database.Router
	Tab    networkTab
	Tabs   []networkTab

	// Routers backs the device picker shown when the controller does not know
	// which device to configure.
	Routers []database.Router

	// Live reports whether the device answered. When it did not, LiveHint
	// explains why and the page renders without any tables.
	Live      bool
	LiveError string
	LiveHint  string
	Warnings  []string

	// Editing is the id of the row whose editor is open, empty when the editor
	// is creating a new object.
	Editing string

	Servers        []HotspotServer
	ServerProfiles []HotspotServerProfile
	UserProfiles   []HotspotProfile
	WalledGarden   []WalledGardenEntry
	WalledGardenIP []WalledGardenIPEntry
	PoolRows       []IPPool
	VLANs          []BridgeVLAN

	// Option sources for the editor forms.
	Interfaces         []InterfaceStats
	Pools              []string
	Bridges            []string
	ServerNames        []string
	ServerProfileNames []string
	UserProfileNames   []string

	// Enum sources for the editor forms, copied from the device layer so the
	// template can render a select or a checkbox group without hardcoding the
	// accepted values.
	LoginByMethods        []string
	RadiusMACFormats      []string
	NASPortTypes          []string
	MACAuthModes          []string
	OpenStatusPages       []string
	QueuePositions        []string
	WalledGardenMethods   []string
	WalledGardenActions   []string
	WalledGardenIPActions []string

	// Hotspot installer mirrors /ip/hotspot setup. Steps and error retain the
	// partial result so an operator can see exactly where a device-side failure
	// stopped and safely rerun the named, idempotent steps.
	InstallerForm  *hotspotInstallerForm
	InstallerSteps []string
	InstallerError string

	ServerForm         *hotspotServerForm
	ServerProfileForm  *hotspotServerProfileForm
	UserProfileForm    *hotspotUserProfileForm
	WalledGardenForm   *walledGardenForm
	WalledGardenIPForm *walledGardenIPForm
	PoolForm           *ipPoolForm
	VLANForm           *bridgeVLANForm
}

// newNetworkPage returns a page whose forms are all initialised, so the template
// can render any section without nil checks.
func (h *Handler) newNetworkPage(tab networkTab) *networkPage {
	return &networkPage{
		page:               page{Title: "Network", Nav: "network"},
		Tab:                tab,
		Tabs:               networkTabs,
		Warnings:           []string{},
		InstallerForm:      newHotspotInstallerForm(),
		InstallerSteps:     []string{},
		ServerForm:         newHotspotServerForm(),
		ServerProfileForm:  newHotspotServerProfileForm(),
		UserProfileForm:    newHotspotUserProfileForm(),
		WalledGardenForm:   newWalledGardenForm(),
		WalledGardenIPForm: newWalledGardenIPForm(),
		PoolForm:           newIPPoolForm(),
		VLANForm:           newBridgeVLANForm(),

		LoginByMethods:        loginByMethods,
		RadiusMACFormats:      radiusMACFormats,
		NASPortTypes:          nasPortTypes,
		MACAuthModes:          macAuthModes,
		OpenStatusPages:       openStatusPages,
		QueuePositions:        queuePositions,
		WalledGardenMethods:   walledGardenMethods,
		WalledGardenActions:   walledGardenActions,
		WalledGardenIPActions: walledGardenIPActions,
	}
}

// Count is the number of rows in the given section.
func (p *networkPage) Count(tab networkTab) int {
	switch tab {
	case tabServerProfiles:
		return len(p.ServerProfiles)
	case tabUserProfiles:
		return len(p.UserProfiles)
	case tabWalledGarden:
		return len(p.WalledGarden)
	case tabWalledGardenIP:
		return len(p.WalledGardenIP)
	case tabPools:
		return len(p.PoolRows)
	case tabVLANs:
		return len(p.VLANs)
	default:
		return len(p.Servers)
	}
}

// CountFor is Count keyed by the section value the template holds while it
// ranges over the tab list, so the pills can carry a row count.
func (p *networkPage) CountFor(tab networkTab) int { return p.Count(tab) }

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

// validIPOrRange reports whether value is an IP address, a CIDR block, a
// RouterOS address range ("10.0.0.1-10.0.0.20") or such a list, so a typo is
// caught before the device rejects the whole write.
func validIPOrRange(value string) bool {
	for _, item := range splitROSList(value) {
		if !validSingleIPOrRange(item) {
			return false
		}
	}
	return true
}

// validSingleIPOrRange validates one entry of an address list.
func validSingleIPOrRange(value string) bool {
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	if _, err := netip.ParsePrefix(value); err == nil {
		return true
	}
	from, to, found := strings.Cut(value, "-")
	if !found {
		return false
	}
	_, fromErr := netip.ParseAddr(strings.TrimSpace(from))
	_, toErr := netip.ParseAddr(strings.TrimSpace(to))
	return fromErr == nil && toErr == nil
}

// validPortSpec reports whether value is a port, a range ("80-90") or such a
// list, which is what dst-port accepts on both walled garden menus.
func validPortSpec(value string) bool {
	for _, item := range splitROSList(value) {
		if !validPortRange(item) {
			return false
		}
	}
	return true
}

// validPortRange validates one port or port range.
func validPortRange(value string) bool {
	from, to, ranged := strings.Cut(value, "-")
	low, ok := parsePort(from)
	if !ok {
		return false
	}
	if !ranged {
		return true
	}
	high, ok := parsePort(to)
	return ok && high >= low
}

// parsePort reads one port number.
func parsePort(value string) (int, bool) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// validEnum checks a value against the options a select offers.
func validEnum(value string, allowed []string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, option := range allowed {
		if strings.EqualFold(option, value) {
			return true
		}
	}
	return false
}

// validSet checks a comma separated value against the options a checkbox group
// offers, which is how login-by and method are entered.
func validSet(value string, allowed []string) bool {
	for _, item := range splitROSList(value) {
		if !validEnum(item, allowed) {
			return false
		}
	}
	return true
}

// validSharedUsers accepts the RouterOS symbolic value "unlimited" or a
// positive integer.
func validSharedUsers(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "unlimited") {
		return true
	}
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

// validAddressesPerMAC accepts "unlimited" or a positive integer.
func validAddressesPerMAC(value string) bool { return validSharedUsers(value) }

// maximumTextLength bounds the free text fields so a paste accident cannot push
// a megabyte of text into the device configuration.
const maximumTextLength = 200

// tooLong reports whether a field exceeds the accepted length.
func tooLong(value string) bool { return len(strings.TrimSpace(value)) > maximumTextLength }

// ---------------------------------------------------------------------------
// Hotspot server editor
// ---------------------------------------------------------------------------

// hotspotServerForm carries the hotspot server editor values, including the
// ones that failed validation so the operator does not lose their typing.
type hotspotServerForm struct {
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
	Errors           map[string]string
}

// newHotspotServerForm returns an empty editor. Fields are left blank on purpose
// so RouterOS applies its own default instead of the controller guessing one.
func newHotspotServerForm() *hotspotServerForm {
	return &hotspotServerForm{Errors: map[string]string{}}
}

// hotspotServerFormFromRequest reads the submitted editor.
func hotspotServerFormFromRequest(r *http.Request) *hotspotServerForm {
	return &hotspotServerForm{
		Name: strings.TrimSpace(r.PostFormValue("name")),
		// The interface field accepts a comma separated list; normalise it here
		// so "ether1, ether2" travels as "ether1,ether2" and the existence
		// check sees exactly the entries the write would send.
		Interface:        joinROSList(splitROSList(r.PostFormValue("interface"))),
		AddressPool:      strings.TrimSpace(r.PostFormValue("address_pool")),
		Profile:          strings.TrimSpace(r.PostFormValue("profile")),
		IdleTimeout:      strings.TrimSpace(r.PostFormValue("idle_timeout")),
		KeepaliveTimeout: strings.TrimSpace(r.PostFormValue("keepalive_timeout")),
		LoginTimeout:     strings.TrimSpace(r.PostFormValue("login_timeout")),
		AddressesPerMAC:  strings.TrimSpace(r.PostFormValue("addresses_per_mac")),
		Comment:          strings.TrimSpace(r.PostFormValue("comment")),
		Disabled:         r.PostFormValue("disabled") != "",
		Errors:           map[string]string{},
	}
}

// hotspotServerFormFromServer pre-fills the editor of an existing server.
func hotspotServerFormFromServer(server HotspotServer) *hotspotServerForm {
	return &hotspotServerForm{
		Name:             server.Name,
		Interface:        server.Interface,
		AddressPool:      server.AddressPool,
		Profile:          server.Profile,
		IdleTimeout:      server.IdleTimeout,
		KeepaliveTimeout: server.KeepaliveTimeout,
		LoginTimeout:     server.LoginTimeout,
		AddressesPerMAC:  server.AddressesPerMAC,
		Comment:          server.Comment,
		Disabled:         server.Disabled,
		Errors:           map[string]string{},
	}
}

// validate checks the editor. On create the name and the interface are required
// because RouterOS needs an interface and an unnamed server is hard to find
// again in the tables.
func (f *hotspotServerForm) validate(isCreate bool) bool {
	if isCreate && f.Name == "" {
		f.Errors["name"] = "Name the server so it can be identified in the walled garden and in the ticket tables."
	} else if tooLong(f.Name) {
		f.Errors["name"] = "Keep the name under 200 characters."
	}
	if isCreate && f.Interface == "" {
		f.Errors["interface"] = "Pick at least one interface: RouterOS requires the interface a hotspot listens on."
	}
	if !validAddressesPerMAC(f.AddressesPerMAC) {
		f.Errors["addresses_per_mac"] = "Use a whole number of MAC addresses per client, or leave it blank."
	}
	if !validROSTime(f.IdleTimeout) {
		f.Errors["idle_timeout"] = "Use a RouterOS interval such as 10m, 1h or none."
	}
	if !validROSTime(f.KeepaliveTimeout) {
		f.Errors["keepalive_timeout"] = "Use a RouterOS interval such as 2m, 1h or none."
	}
	if !validROSTime(f.LoginTimeout) {
		f.Errors["login_timeout"] = "Use a RouterOS interval such as 1m, 1h or none."
	}
	if tooLong(f.Comment) {
		f.Errors["comment"] = "Keep the comment under 200 characters."
	}
	return len(f.Errors) == 0
}

// refusedValueProperty reports which property the device rejected with the
// RouterOS sentence "input does not match any value of <property>", or "" for
// any other failure. The check runs on the decorated error text because no
// sentinel tags this condition. The controller never refuses a write on its
// own: only the device decides which interface names exist.
func refusedValueProperty(err error) string {
	if err == nil {
		return ""
	}
	const marker = "does not match any value of"
	lowered := strings.ToLower(err.Error())
	at := strings.Index(lowered, marker)
	if at < 0 {
		return ""
	}
	// Mirror unknownParameterName: skip separator-only words so both
	// "value of interface ..." and "value of: interface ..." parse.
	for _, word := range strings.Fields(lowered[at+len(marker):]) {
		if name := strings.Trim(word, ":='\"`.,()"); name != "" {
			return name
		}
	}
	return ""
}

// visibleName folds an interface name to what the eye sees: case is ignored
// and characters that occupy no width (zero-width spaces and joiners, soft
// hyphens, byte order marks, non-breaking spaces) are dropped, so two names
// that render identically compare equal. RouterOS compares bytes, and so does
// the write; this is only ever used to EXPLAIN a refusal, never to allow one.
func visibleName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r == '\u00a0' || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// interfaceRefusalText explains a refusal the device has already made: it
// quotes the value the operator sent and lists what the router reports, so the
// two can be compared. The list is evidence, not a verdict - a name may be
// missing from it for reasons the write itself would have settled. Ephemeral
// "<...>" session interfaces are counted but not spelled out; they would only
// push the interfaces an operator binds a hotspot to out of view. An empty
// list still yields the refusal itself, which is the fact that matters.
func interfaceRefusalText(wanted string, available []string) string {
	text := "The device refused interface \"" + wanted + "\"."
	if len(available) == 0 {
		return text
	}
	var shown []string
	sessions := 0
	for _, name := range available {
		if strings.HasPrefix(name, "<") {
			sessions++
			continue
		}
		shown = append(shown, name)
	}
	if len(shown) > 20 {
		shown = append(shown[:20:20], "…")
	}
	text += " Interfaces this router reports: " + strings.Join(shown, ", ") + "."
	if sessions > 0 {
		text += " (omitted " + strconv.Itoa(sessions) + " dynamic session interfaces)"
	}

	// Diagnose against the list for the operator's benefit only: the device
	// has already refused the write, and this can be wrong without costing
	// anything. splitROSList splits a multi-interface field the same way the
	// write did, so "ether1, ghost0" is judged entry by entry.
	exact := make(map[string]bool, len(available))
	folded := make(map[string]bool, len(available))
	for _, name := range available {
		exact[name] = true
		if key := visibleName(name); key != "" {
			folded[key] = true
		}
	}
	var absent, lookAlike []string
	for _, name := range splitROSList(wanted) {
		switch {
		case exact[name]:
		case folded[visibleName(name)]:
			lookAlike = append(lookAlike, name)
		default:
			absent = append(absent, name)
		}
	}
	if len(absent) > 0 {
		text += " Not among the names the device reports: " + strings.Join(absent, ", ") + "."
	}
	if len(lookAlike) > 0 {
		text += " " + strings.Join(lookAlike, ", ") +
			" looks like an entry in that list but is not the same text: pick it from the list instead of typing it."
	}
	if len(absent) == 0 && len(lookAlike) == 0 {
		text += " The device lists this name, so it may have changed since the list was read: reload the page and try again."
	}
	return text
}

// explainInterfaceRefusal turns a device refusal of an interface value into a
// field error and reports whether it did: false means the failure is something
// else, and the caller flashes the device's message unchanged. The write
// always reaches the device first - this runs only after RouterOS itself has
// said no, and the list it shows comes from the same InterfaceList the
// editor's datalist was built from. A list that cannot be read costs the
// listing, never the explanation: the refusal itself is already established.
func (h *Handler) explainInterfaceRefusal(ctx context.Context, client *MikrotikClient, form *hotspotServerForm, cause error) bool {
	if refusedValueProperty(cause) != "interface" {
		return false
	}
	h.log.Warn("device refused an interface value",
		"interface", form.Interface, "cause", cause)
	text := interfaceRefusalText(form.Interface, nil)
	if interfaces, err := client.InterfaceList(ctx); err != nil {
		h.log.Warn("cannot read the interface list to explain a device refusal",
			"error", err)
	} else {
		names := make([]string, 0, len(interfaces))
		for _, iface := range interfaces {
			if iface.Name != "" {
				names = append(names, iface.Name)
			}
		}
		text = interfaceRefusalText(form.Interface, names)
	}
	form.Errors["interface"] = text
	return true
}

// spec converts the editor into the device layer spec.
func (f *hotspotServerForm) spec() HotspotServerSpec {
	return HotspotServerSpec{
		Name:             f.Name,
		Interface:        f.Interface,
		AddressPool:      f.AddressPool,
		Profile:          f.Profile,
		IdleTimeout:      f.IdleTimeout,
		KeepaliveTimeout: f.KeepaliveTimeout,
		LoginTimeout:     f.LoginTimeout,
		AddressesPerMAC:  f.AddressesPerMAC,
		Comment:          f.Comment,
		Disabled:         f.Disabled,
	}
}

// ---------------------------------------------------------------------------
// Server profile editor
// ---------------------------------------------------------------------------

// hotspotServerProfileForm carries the server profile editor values.
type hotspotServerProfileForm struct {
	Name                  string
	HotspotAddress        string
	DNSName               string
	HTMLDirectory         string
	HTMLDirectoryOverride string
	InstallHotspotQueue   bool
	RateLimit             string
	HTTPProxy             string
	SMTPServer            string
	LoginBy               []string
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
	Errors                map[string]string
}

// newHotspotServerProfileForm returns an empty editor.
func newHotspotServerProfileForm() *hotspotServerProfileForm {
	return &hotspotServerProfileForm{Errors: map[string]string{}}
}

// hotspotServerProfileFormFromRequest reads the submitted editor. login-by is
// collected from a checkbox group, so the values arrive as separate fields.
func hotspotServerProfileFormFromRequest(r *http.Request) *hotspotServerProfileForm {
	_ = r.ParseForm()
	return &hotspotServerProfileForm{
		Name:                  strings.TrimSpace(r.PostFormValue("name")),
		HotspotAddress:        strings.TrimSpace(r.PostFormValue("hotspot_address")),
		DNSName:               strings.TrimSpace(r.PostFormValue("dns_name")),
		HTMLDirectory:         strings.TrimSpace(r.PostFormValue("html_directory")),
		HTMLDirectoryOverride: strings.TrimSpace(r.PostFormValue("html_directory_override")),
		InstallHotspotQueue:   r.PostFormValue("install_hotspot_queue") != "",
		RateLimit:             strings.TrimSpace(r.PostFormValue("rate_limit")),
		HTTPProxy:             strings.TrimSpace(r.PostFormValue("http_proxy")),
		SMTPServer:            strings.TrimSpace(r.PostFormValue("smtp_server")),
		LoginBy:               r.PostForm["login_by"],
		MACAuthMode:           strings.TrimSpace(r.PostFormValue("mac_auth_mode")),
		MACAuthPassword:       strings.TrimSpace(r.PostFormValue("mac_auth_password")),
		HTTPCookieLifetime:    strings.TrimSpace(r.PostFormValue("http_cookie_lifetime")),
		SSLCertificate:        strings.TrimSpace(r.PostFormValue("ssl_certificate")),
		SplitUserDomain:       r.PostFormValue("split_user_domain") != "",
		TrialUptimeLimit:      strings.TrimSpace(r.PostFormValue("trial_uptime_limit")),
		TrialUptimeReset:      strings.TrimSpace(r.PostFormValue("trial_uptime_reset")),
		TrialUserProfile:      strings.TrimSpace(r.PostFormValue("trial_user_profile")),
		UseRadius:             r.PostFormValue("use_radius") != "",
		RadiusAccounting:      r.PostFormValue("radius_accounting") != "",
		RadiusInterimUpdate:   strings.TrimSpace(r.PostFormValue("radius_interim_update")),
		NASPortType:           strings.TrimSpace(r.PostFormValue("nas_port_type")),
		RadiusDefaultDomain:   strings.TrimSpace(r.PostFormValue("radius_default_domain")),
		RadiusLocationID:      strings.TrimSpace(r.PostFormValue("radius_location_id")),
		RadiusLocationName:    strings.TrimSpace(r.PostFormValue("radius_location_name")),
		RadiusMACFormat:       strings.TrimSpace(r.PostFormValue("radius_mac_format")),
		Errors:                map[string]string{},
	}
}

// hotspotServerProfileFormFromProfile pre-fills the editor of a profile.
func hotspotServerProfileFormFromProfile(p HotspotServerProfile) *hotspotServerProfileForm {
	return &hotspotServerProfileForm{
		Name:                  p.Name,
		HotspotAddress:        p.HotspotAddress,
		DNSName:               p.DNSName,
		HTMLDirectory:         p.HTMLDirectory,
		HTMLDirectoryOverride: p.HTMLDirectoryOverride,
		InstallHotspotQueue:   p.InstallHotspotQueue,
		RateLimit:             p.RateLimit,
		HTTPProxy:             p.HTTPProxy,
		SMTPServer:            p.SMTPServer,
		LoginBy:               p.LoginMethods(),
		MACAuthMode:           p.MACAuthMode,
		MACAuthPassword:       p.MACAuthPassword,
		HTTPCookieLifetime:    p.HTTPCookieLifetime,
		SSLCertificate:        p.SSLCertificate,
		SplitUserDomain:       p.SplitUserDomain,
		TrialUptimeLimit:      p.TrialUptimeLimit,
		TrialUptimeReset:      p.TrialUptimeReset,
		TrialUserProfile:      p.TrialUserProfile,
		UseRadius:             p.UseRadius,
		RadiusAccounting:      p.RadiusAccounting,
		RadiusInterimUpdate:   p.RadiusInterimUpdate,
		NASPortType:           p.NASPortType,
		RadiusDefaultDomain:   p.RadiusDefaultDomain,
		RadiusLocationID:      p.RadiusLocationID,
		RadiusLocationName:    p.RadiusLocationName,
		RadiusMACFormat:       p.RadiusMACFormat,
		Errors:                map[string]string{},
	}
}

// HasLoginMethod reports whether a login method checkbox should be ticked.
func (f *hotspotServerProfileForm) HasLoginMethod(method string) bool {
	for _, value := range f.LoginBy {
		if strings.EqualFold(strings.TrimSpace(value), method) {
			return true
		}
	}
	return false
}

// validHTTPProxy accepts the "address:port" form RouterOS uses, the symbolic
// "none" or a plain address, which RouterOS pairs with port 8080.
func validHTTPProxy(value string) bool {
	if value == "" || strings.EqualFold(value, "none") {
		return true
	}
	address, port, found := strings.Cut(value, ":")
	if !found {
		_, err := netip.ParseAddr(value)
		return err == nil
	}
	if _, ok := parsePort(port); !ok {
		return false
	}
	_, err := netip.ParseAddr(strings.TrimSpace(address))
	return err == nil
}

// validate checks the server profile editor.
func (f *hotspotServerProfileForm) validate(isCreate bool) bool {
	if isCreate && f.Name == "" {
		f.Errors["name"] = "Name the profile so hotspot servers and RADIUS can refer to it."
	}
	if !validIPOrRange(f.HotspotAddress) {
		f.Errors["hotspot_address"] = "Enter the address of the hotspot gateway, for example 192.168.88.1."
	}
	if f.SMTPServer != "" && !validSingleIPOrRange(f.SMTPServer) {
		f.Errors["smtp_server"] = "Enter the address of the SMTP server, or leave it blank."
	}
	if !validHTTPProxy(f.HTTPProxy) {
		f.Errors["http_proxy"] = "Use address:port (for example 192.168.88.1:8080) or none."
	}
	if !validSet(joinROSList(f.LoginBy), loginByMethods) {
		f.Errors["login_by"] = "Pick the login methods from the list."
	}
	if !validEnum(f.NASPortType, nasPortTypes) {
		f.Errors["nas_port_type"] = "Pick one of ethernet, cable or wireless-802.11."
	}
	if !validEnum(f.MACAuthMode, macAuthModes) {
		f.Errors["mac_auth_mode"] = "Pick how the MAC address is turned into a username."
	}
	if !validEnum(f.RadiusMACFormat, radiusMACFormats) {
		f.Errors["radius_mac_format"] = "Pick one of the documented RADIUS MAC formats."
	}
	if f.DNSName != "" && strings.ContainsAny(f.DNSName, " /\\") {
		f.Errors["dns_name"] = "Enter a host name without spaces or slashes, for example hotspot.local."
	}
	for field, value := range map[string]string{
		"http_cookie_lifetime": f.HTTPCookieLifetime,
		"trial_uptime_limit":   f.TrialUptimeLimit,
		"trial_uptime_reset":   f.TrialUptimeReset,
	} {
		if !validROSTime(value) {
			f.Errors[field] = "Use a RouterOS interval such as 1h, 1d or none."
		}
	}
	if f.RadiusInterimUpdate != "" &&
		!strings.EqualFold(f.RadiusInterimUpdate, "received") &&
		!validROSTime(f.RadiusInterimUpdate) {
		f.Errors["radius_interim_update"] = "Use \"received\", an interval such as 5m, or leave it blank."
	}
	for field, value := range map[string]string{
		"rate_limit":            f.RateLimit,
		"dns_name":              f.DNSName,
		"html_directory":        f.HTMLDirectory,
		"radius_default_domain": f.RadiusDefaultDomain,
		"radius_location_id":    f.RadiusLocationID,
		"radius_location_name":  f.RadiusLocationName,
	} {
		if tooLong(value) {
			f.Errors[field] = "Keep this under 200 characters."
		}
	}
	return len(f.Errors) == 0
}

// spec converts the editor into the device layer spec.
func (f *hotspotServerProfileForm) spec() HotspotServerProfileSpec {
	return HotspotServerProfileSpec{
		Name:                  f.Name,
		HotspotAddress:        f.HotspotAddress,
		DNSName:               f.DNSName,
		HTMLDirectory:         f.HTMLDirectory,
		HTMLDirectoryOverride: f.HTMLDirectoryOverride,
		InstallHotspotQueue:   f.InstallHotspotQueue,
		RateLimit:             f.RateLimit,
		HTTPProxy:             f.HTTPProxy,
		SMTPServer:            f.SMTPServer,
		LoginBy:               joinROSList(f.LoginBy),
		MACAuthMode:           f.MACAuthMode,
		MACAuthPassword:       f.MACAuthPassword,
		HTTPCookieLifetime:    f.HTTPCookieLifetime,
		SSLCertificate:        f.SSLCertificate,
		SplitUserDomain:       f.SplitUserDomain,
		TrialUptimeLimit:      f.TrialUptimeLimit,
		TrialUptimeReset:      f.TrialUptimeReset,
		TrialUserProfile:      f.TrialUserProfile,
		UseRadius:             f.UseRadius,
		RadiusAccounting:      f.RadiusAccounting,
		RadiusInterimUpdate:   f.RadiusInterimUpdate,
		NASPortType:           f.NASPortType,
		RadiusDefaultDomain:   f.RadiusDefaultDomain,
		RadiusLocationID:      f.RadiusLocationID,
		RadiusLocationName:    f.RadiusLocationName,
		RadiusMACFormat:       f.RadiusMACFormat,
	}
}

// ---------------------------------------------------------------------------
// User profile editor
// ---------------------------------------------------------------------------

// hotspotUserProfileForm carries the user profile editor values. shared-users
// stays a string because RouterOS spells "no limit" as "unlimited".
type hotspotUserProfileForm struct {
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
	Errors            map[string]string
}

// newHotspotUserProfileForm returns an empty editor.
func newHotspotUserProfileForm() *hotspotUserProfileForm {
	return &hotspotUserProfileForm{Errors: map[string]string{}}
}

// hotspotUserProfileFormFromRequest reads the submitted editor.
func hotspotUserProfileFormFromRequest(r *http.Request) *hotspotUserProfileForm {
	return &hotspotUserProfileForm{
		Name:              strings.TrimSpace(r.PostFormValue("name")),
		SharedUsers:       strings.TrimSpace(r.PostFormValue("shared_users")),
		AddressPool:       strings.TrimSpace(r.PostFormValue("address_pool")),
		RateLimit:         strings.TrimSpace(r.PostFormValue("rate_limit")),
		SessionTime:       strings.TrimSpace(r.PostFormValue("session_timeout")),
		IdleTimeout:       strings.TrimSpace(r.PostFormValue("idle_timeout")),
		Keepalive:         strings.TrimSpace(r.PostFormValue("keepalive_timeout")),
		StatusAutorefresh: strings.TrimSpace(r.PostFormValue("status_autorefresh")),
		AddMACCookie:      r.PostFormValue("add_mac_cookie") != "",
		MACCookieTimeout:  strings.TrimSpace(r.PostFormValue("mac_cookie_timeout")),
		AddressList:       strings.TrimSpace(r.PostFormValue("address_list")),
		IncomingFilter:    strings.TrimSpace(r.PostFormValue("incoming_filter")),
		OutgoingFilter:    strings.TrimSpace(r.PostFormValue("outgoing_filter")),
		IncomingPktMark:   strings.TrimSpace(r.PostFormValue("incoming_packet_mark")),
		OutgoingPktMark:   strings.TrimSpace(r.PostFormValue("outgoing_packet_mark")),
		QueueType:         strings.TrimSpace(r.PostFormValue("queue_type")),
		ParentQueue:       strings.TrimSpace(r.PostFormValue("parent_queue")),
		InsertQueueBefore: strings.TrimSpace(r.PostFormValue("insert_queue_before")),
		OnLogin:           strings.TrimSpace(r.PostFormValue("on_login")),
		OnLogout:          strings.TrimSpace(r.PostFormValue("on_logout")),
		TransparentProxy:  r.PostFormValue("transparent_proxy") != "",
		OpenStatusPage:    strings.TrimSpace(r.PostFormValue("open_status_page")),
		Advertise:         r.PostFormValue("advertise") != "",
		AdvertiseURL:      strings.TrimSpace(r.PostFormValue("advertise_url")),
		AdvertiseInterval: strings.TrimSpace(r.PostFormValue("advertise_interval")),
		AdvertiseTimeout:  strings.TrimSpace(r.PostFormValue("advertise_timeout")),
		Errors:            map[string]string{},
	}
}

// hotspotUserProfileFormFromProfile pre-fills the editor of a user profile.
func hotspotUserProfileFormFromProfile(p HotspotProfile) *hotspotUserProfileForm {
	return &hotspotUserProfileForm{
		Name:              p.Name,
		SharedUsers:       p.SharedUsersLabel(),
		AddressPool:       p.AddressPool,
		RateLimit:         p.RateLimit,
		SessionTime:       p.SessionTime,
		IdleTimeout:       p.IdleTimeout,
		Keepalive:         p.Keepalive,
		StatusAutorefresh: p.StatusAutorefresh,
		AddMACCookie:      p.AddMACCookie,
		MACCookieTimeout:  p.MACCookieTimeout,
		AddressList:       p.AddressList,
		IncomingFilter:    p.IncomingFilter,
		OutgoingFilter:    p.OutgoingFilter,
		IncomingPktMark:   p.IncomingPktMark,
		OutgoingPktMark:   p.OutgoingPktMark,
		QueueType:         p.QueueType,
		ParentQueue:       p.ParentQueue,
		InsertQueueBefore: p.InsertQueueBefore,
		OnLogin:           p.OnLogin,
		OnLogout:          p.OnLogout,
		TransparentProxy:  p.TransparentProxy,
		OpenStatusPage:    p.OpenStatusPage,
		Advertise:         p.Advertise,
		AdvertiseURL:      p.AdvertiseURL,
		AdvertiseInterval: p.AdvertiseInterval,
		AdvertiseTimeout:  p.AdvertiseTimeout,
		Errors:            map[string]string{},
	}
}

// validate checks the user profile editor.
func (f *hotspotUserProfileForm) validate(isCreate bool) bool {
	if isCreate && f.Name == "" {
		f.Errors["name"] = "Name the profile: vouchers are provisioned onto a profile by name."
	}
	if !validSharedUsers(f.SharedUsers) {
		f.Errors["shared_users"] = "Use a whole number of devices per login, \"unlimited\", or leave it blank."
	}
	if !validEnum(f.OpenStatusPage, openStatusPages) {
		f.Errors["open_status_page"] = "Pick http-login or always."
	}
	if !validEnum(f.InsertQueueBefore, queuePositions) {
		f.Errors["insert_queue_before"] = "Pick bottom or first."
	}
	for field, value := range map[string]string{
		"session_timeout":    f.SessionTime,
		"idle_timeout":       f.IdleTimeout,
		"keepalive_timeout":  f.Keepalive,
		"status_autorefresh": f.StatusAutorefresh,
		"mac_cookie_timeout": f.MACCookieTimeout,
		"advertise_interval": f.AdvertiseInterval,
	} {
		if !validROSTime(value) {
			f.Errors[field] = "Use a RouterOS interval such as 1h, 1d or none."
		}
	}
	if f.AdvertiseTimeout != "" &&
		!strings.EqualFold(f.AdvertiseTimeout, "immediately") &&
		!strings.EqualFold(f.AdvertiseTimeout, "never") &&
		!validROSTime(f.AdvertiseTimeout) {
		f.Errors["advertise_timeout"] = "Use immediately, never, an interval such as 30s, or leave it blank."
	}
	for field, value := range map[string]string{
		"rate_limit":           f.RateLimit,
		"address_list":         f.AddressList,
		"incoming_filter":      f.IncomingFilter,
		"outgoing_filter":      f.OutgoingFilter,
		"incoming_packet_mark": f.IncomingPktMark,
		"outgoing_packet_mark": f.OutgoingPktMark,
		"queue_type":           f.QueueType,
		"parent_queue":         f.ParentQueue,
		"advertise_url":        f.AdvertiseURL,
		"on_login":             f.OnLogin,
		"on_logout":            f.OnLogout,
	} {
		if tooLong(value) {
			f.Errors[field] = "Keep this under 200 characters."
		}
	}
	return len(f.Errors) == 0
}

// spec converts the editor into the device layer spec.
func (f *hotspotUserProfileForm) spec() HotspotUserProfileSpec {
	return HotspotUserProfileSpec{
		Name:              f.Name,
		SharedUsers:       f.SharedUsers,
		AddressPool:       f.AddressPool,
		RateLimit:         f.RateLimit,
		SessionTime:       f.SessionTime,
		IdleTimeout:       f.IdleTimeout,
		Keepalive:         f.Keepalive,
		StatusAutorefresh: f.StatusAutorefresh,
		AddMACCookie:      f.AddMACCookie,
		MACCookieTimeout:  f.MACCookieTimeout,
		AddressList:       f.AddressList,
		IncomingFilter:    f.IncomingFilter,
		OutgoingFilter:    f.OutgoingFilter,
		IncomingPktMark:   f.IncomingPktMark,
		OutgoingPktMark:   f.OutgoingPktMark,
		QueueType:         f.QueueType,
		ParentQueue:       f.ParentQueue,
		InsertQueueBefore: f.InsertQueueBefore,
		OnLogin:           f.OnLogin,
		OnLogout:          f.OnLogout,
		TransparentProxy:  f.TransparentProxy,
		OpenStatusPage:    f.OpenStatusPage,
		Advertise:         f.Advertise,
		AdvertiseURL:      f.AdvertiseURL,
		AdvertiseInterval: f.AdvertiseInterval,
		AdvertiseTimeout:  f.AdvertiseTimeout,
	}
}

// ---------------------------------------------------------------------------
// Walled garden editors
// ---------------------------------------------------------------------------

// walledGardenMethods are the HTTP methods a host rule can be limited to.
var walledGardenMethods = []string{"GET", "HEAD", "POST", "PUT", "CONNECT", "OPTIONS", "DELETE", "TRACE"}

// walledGardenActions are the documented values of the host rule action.
var walledGardenActions = []string{"allow", "deny"}

// walledGardenForm carries the host based walled garden editor values.
type walledGardenForm struct {
	Server     string
	SrcAddress string
	Method     []string
	DstHost    string
	DstPort    string
	Path       string
	Action     string
	Comment    string
	Disabled   bool
	Errors     map[string]string
}

// newWalledGardenForm returns an editor with the action RouterOS defaults to.
func newWalledGardenForm() *walledGardenForm {
	return &walledGardenForm{Action: "allow", Errors: map[string]string{}}
}

// walledGardenFormFromRequest reads the submitted editor.
func walledGardenFormFromRequest(r *http.Request) *walledGardenForm {
	_ = r.ParseForm()
	return &walledGardenForm{
		Server:     strings.TrimSpace(r.PostFormValue("server")),
		SrcAddress: strings.TrimSpace(r.PostFormValue("src_address")),
		Method:     r.PostForm["method"],
		DstHost:    strings.TrimSpace(r.PostFormValue("dst_host")),
		DstPort:    strings.TrimSpace(r.PostFormValue("dst_port")),
		Path:       strings.TrimSpace(r.PostFormValue("path")),
		Action:     strings.TrimSpace(defaultValue(r.PostFormValue("action"), "allow")),
		Comment:    strings.TrimSpace(r.PostFormValue("comment")),
		Disabled:   r.PostFormValue("disabled") != "",
		Errors:     map[string]string{},
	}
}

// walledGardenFormFromEntry pre-fills the editor of an existing rule.
func walledGardenFormFromEntry(entry WalledGardenEntry) *walledGardenForm {
	return &walledGardenForm{
		Server:     entry.Server,
		SrcAddress: entry.SrcAddress,
		Method:     entry.Methods(),
		DstHost:    entry.DstHost,
		DstPort:    entry.DstPort,
		Path:       entry.Path,
		Action:     defaultValue(entry.Action, "allow"),
		Comment:    entry.Comment,
		Disabled:   entry.Disabled,
		Errors:     map[string]string{},
	}
}

// HasMethod reports whether a method checkbox should be ticked.
func (f *walledGardenForm) HasMethod(method string) bool {
	return containsFold(f.Method, method)
}

// validate checks the host rule editor. RouterOS needs at least one matcher,
// and dst-address cannot be used because it is read only on this menu.
func (f *walledGardenForm) validate() bool {
	if !validEnum(f.Action, walledGardenActions) {
		f.Errors["action"] = "Pick allow or deny."
	}
	if !validSet(joinROSList(f.Method), walledGardenMethods) {
		f.Errors["method"] = "Pick the HTTP methods from the list."
	}
	if !validIPOrRange(f.SrcAddress) {
		f.Errors["src_address"] = "Enter a client address, a subnet such as 192.168.88.0/24, or leave it blank."
	}
	if !validPortSpec(f.DstPort) {
		f.Errors["dst_port"] = "Enter a port (80) or a range (80-90) or a comma separated list."
	}
	if strings.TrimSpace(f.DstHost) == "" && strings.TrimSpace(f.DstPort) == "" &&
		strings.TrimSpace(f.Path) == "" && strings.TrimSpace(f.SrcAddress) == "" {
		f.Errors["dst_host"] = "Give the rule something to match: a destination host, port, path or client address."
	}
	if tooLong(f.DstHost) {
		f.Errors["dst_host"] = "Keep the host under 200 characters."
	}
	if tooLong(f.Path) {
		f.Errors["path"] = "Keep the path under 200 characters."
	}
	if tooLong(f.Comment) {
		f.Errors["comment"] = "Keep the comment under 200 characters."
	}
	return len(f.Errors) == 0
}

// spec converts the editor into the device layer spec.
func (f *walledGardenForm) spec() WalledGardenSpec {
	return WalledGardenSpec{
		Server:     f.Server,
		SrcAddress: f.SrcAddress,
		Method:     joinROSList(f.Method),
		DstHost:    f.DstHost,
		DstPort:    f.DstPort,
		Path:       f.Path,
		Action:     f.Action,
		Comment:    f.Comment,
		Disabled:   f.Disabled,
	}
}

// containsFold reports whether a list holds a value, ignoring case.
func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

// walledGardenIPForm carries the IP based walled garden editor values.
type walledGardenIPForm struct {
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
	Errors         map[string]string
}

// newWalledGardenIPForm returns an editor with the action RouterOS defaults to.
func newWalledGardenIPForm() *walledGardenIPForm {
	return &walledGardenIPForm{Action: WalledGardenIPAction, Errors: map[string]string{}}
}

// walledGardenIPFormFromRequest reads the submitted editor.
func walledGardenIPFormFromRequest(r *http.Request) *walledGardenIPForm {
	return &walledGardenIPForm{
		Server:         strings.TrimSpace(r.PostFormValue("server")),
		SrcAddress:     strings.TrimSpace(r.PostFormValue("src_address")),
		DstAddress:     strings.TrimSpace(r.PostFormValue("dst_address")),
		DstHost:        strings.TrimSpace(r.PostFormValue("dst_host")),
		Protocol:       strings.TrimSpace(r.PostFormValue("protocol")),
		DstPort:        strings.TrimSpace(r.PostFormValue("dst_port")),
		SrcAddressList: strings.TrimSpace(r.PostFormValue("src_address_list")),
		DstAddressList: strings.TrimSpace(r.PostFormValue("dst_address_list")),
		Action:         strings.TrimSpace(defaultValue(r.PostFormValue("action"), WalledGardenIPAction)),
		Comment:        strings.TrimSpace(r.PostFormValue("comment")),
		Disabled:       r.PostFormValue("disabled") != "",
		Errors:         map[string]string{},
	}
}

// walledGardenIPFormFromEntry pre-fills the editor of an existing IP rule.
func walledGardenIPFormFromEntry(entry WalledGardenIPEntry) *walledGardenIPForm {
	return &walledGardenIPForm{
		Server:         entry.Server,
		SrcAddress:     entry.SrcAddress,
		DstAddress:     entry.DstAddress,
		DstHost:        entry.DstHost,
		Protocol:       entry.Protocol,
		DstPort:        entry.DstPort,
		SrcAddressList: entry.SrcAddressList,
		DstAddressList: entry.DstAddressList,
		Action:         defaultValue(entry.Action, WalledGardenIPAction),
		Comment:        entry.Comment,
		Disabled:       entry.Disabled,
		Errors:         map[string]string{},
	}
}

// validate checks the IP rule editor. At least one matcher is required, the same
// rule RouterOS applies when the rule is added.
func (f *walledGardenIPForm) validate() bool {
	if !validEnum(f.Action, walledGardenIPActions) {
		f.Errors["action"] = "Pick accept, drop or reject."
	}
	if !validIPOrRange(f.SrcAddress) {
		f.Errors["src_address"] = "Enter a client address or subnet, or leave it blank."
	}
	if !validIPOrRange(f.DstAddress) {
		f.Errors["dst_address"] = "Enter a destination address or subnet, or leave it blank."
	}
	if !validPortSpec(f.DstPort) {
		f.Errors["dst_port"] = "Enter a port (80), a range (80-90) or a comma separated list."
	}
	if strings.TrimSpace(f.DstAddress) == "" && strings.TrimSpace(f.DstHost) == "" &&
		strings.TrimSpace(f.DstPort) == "" && strings.TrimSpace(f.SrcAddress) == "" &&
		strings.TrimSpace(f.SrcAddressList) == "" && strings.TrimSpace(f.DstAddressList) == "" {
		f.Errors["dst_address"] = "Give the rule something to match: an address, a host, a port or an address list."
	}
	for field, value := range map[string]string{
		"dst_host":         f.DstHost,
		"protocol":         f.Protocol,
		"src_address_list": f.SrcAddressList,
		"dst_address_list": f.DstAddressList,
		"comment":          f.Comment,
	} {
		if tooLong(value) {
			f.Errors[field] = "Keep this under 200 characters."
		}
	}
	return len(f.Errors) == 0
}

// spec converts the editor into the device layer spec.
func (f *walledGardenIPForm) spec() WalledGardenIPSpec {
	return WalledGardenIPSpec{
		Server:         f.Server,
		SrcAddress:     f.SrcAddress,
		DstAddress:     f.DstAddress,
		DstHost:        f.DstHost,
		Protocol:       f.Protocol,
		DstPort:        f.DstPort,
		SrcAddressList: f.SrcAddressList,
		DstAddressList: f.DstAddressList,
		Action:         f.Action,
		Comment:        f.Comment,
		Disabled:       f.Disabled,
	}
}

// ---------------------------------------------------------------------------
// IP pool editor
// ---------------------------------------------------------------------------

// ipPoolForm carries the /ip/pool create editor values. Pools are add-only:
// their ranges are edited on the device where the used portion is visible.
type ipPoolForm struct {
	Name     string
	Ranges   string
	NextPool string
	Comment  string
	Errors   map[string]string
}

// newIPPoolForm returns an empty editor.
func newIPPoolForm() *ipPoolForm {
	return &ipPoolForm{Errors: map[string]string{}}
}

// ipPoolFormFromRequest reads the submitted editor.
func ipPoolFormFromRequest(r *http.Request) *ipPoolForm {
	return &ipPoolForm{
		Name:     strings.TrimSpace(r.PostFormValue("name")),
		Ranges:   strings.TrimSpace(r.PostFormValue("ranges")),
		NextPool: strings.TrimSpace(r.PostFormValue("next_pool")),
		Comment:  strings.TrimSpace(r.PostFormValue("comment")),
		Errors:   map[string]string{},
	}
}

// validate checks the create editor. The ranges are the pool: without a valid
// address, range or list of either, RouterOS would reject the whole add.
func (f *ipPoolForm) validate() bool {
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	if f.Name == "" {
		f.Errors["name"] = "Name the pool so hotspot and DHCP forms can offer it."
	} else if tooLong(f.Name) {
		f.Errors["name"] = "Keep the name under 200 characters."
	}
	if f.Ranges == "" {
		f.Errors["ranges"] = "Give the pool at least one address, such as 10.0.0.10-10.0.0.200."
	} else if !validIPOrRange(f.Ranges) {
		f.Errors["ranges"] = "Enter an address, a range (10.0.0.10-10.0.0.200) or a comma separated list of them."
	}
	if tooLong(f.NextPool) {
		f.Errors["next_pool"] = "Keep the next pool name under 200 characters."
	}
	if tooLong(f.Comment) {
		f.Errors["comment"] = "Keep the comment under 200 characters."
	}
	return len(f.Errors) == 0
}

// ---------------------------------------------------------------------------
// Bridge VLAN editor
// ---------------------------------------------------------------------------

// bridgeVLANForm carries the /interface/bridge/vlan create editor values. Like
// the pools, entries are added here and managed on the device afterwards.
type bridgeVLANForm struct {
	Bridge   string
	VLANIDs  string
	Tagged   string
	Untagged string
	Errors   map[string]string
}

// newBridgeVLANForm returns an empty editor.
func newBridgeVLANForm() *bridgeVLANForm {
	return &bridgeVLANForm{Errors: map[string]string{}}
}

// bridgeVLANFormFromRequest reads the submitted editor.
func bridgeVLANFormFromRequest(r *http.Request) *bridgeVLANForm {
	return &bridgeVLANForm{
		Bridge:   strings.TrimSpace(r.PostFormValue("bridge")),
		VLANIDs:  strings.TrimSpace(r.PostFormValue("vlan_ids")),
		Tagged:   strings.TrimSpace(r.PostFormValue("tagged")),
		Untagged: strings.TrimSpace(r.PostFormValue("untagged")),
		Errors:   map[string]string{},
	}
}

// validate checks the create editor: a bridge owns the entry, the VLAN IDs
// decide what the entry is, and at least one port list makes it useful.
func (f *bridgeVLANForm) validate() bool {
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	if f.Bridge == "" {
		f.Errors["bridge"] = "Pick the bridge that owns this VLAN entry."
	} else if tooLong(f.Bridge) {
		f.Errors["bridge"] = "Keep the bridge name under 200 characters."
	}
	if f.VLANIDs == "" {
		f.Errors["vlan_ids"] = "Enter a VLAN ID (100) or a range (100-120)."
	} else if !validVLANIDSpec(f.VLANIDs) {
		f.Errors["vlan_ids"] = "Use VLAN IDs from 1 to 4094, as 100, or as ranges like 100-120."
	}
	if f.Tagged == "" && f.Untagged == "" {
		f.Errors["tagged"] = "List at least one tagged or untagged port for the VLAN."
	}
	if tooLong(f.Tagged) {
		f.Errors["tagged"] = "Keep the tagged port list under 200 characters."
	}
	if tooLong(f.Untagged) {
		f.Errors["untagged"] = "Keep the untagged port list under 200 characters."
	}
	return len(f.Errors) == 0
}

// validVLANIDSpec validates a RouterOS vlan-ids value: a comma separated list
// of single IDs or ranges, every ID between 1 and 4094 and every range rising.
func validVLANIDSpec(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, item := range splitROSList(value) {
		if !validVLANIDItem(item) {
			return false
		}
	}
	return true
}

// validVLANIDItem validates one "100" or "100-120" entry.
func validVLANIDItem(value string) bool {
	lowRaw, highRaw, ranged := strings.Cut(value, "-")
	low, ok := parseVLANID(lowRaw)
	if !ok {
		return false
	}
	if !ranged {
		return true
	}
	high, ok := parseVLANID(highRaw)
	return ok && high >= low
}

// parseVLANID reads one VLAN ID, accepting only the 1-4094 IEEE range.
func parseVLANID(value string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || id < 1 || id > 4094 {
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// Read handlers
// ---------------------------------------------------------------------------

// NetworkOverview picks the device to configure.
//
// The Network tab is about one router at a time, so the overview only exists to
// choose one: a single device, or a device flagged as the default portal, is
// opened straight away and everything else gets a picker.
func (h *Handler) NetworkOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		h.fail(w, r, "load router inventory", err)
		return
	}
	tab := tabFromRequest(r)

	// ?router=<id> jumps straight to one device.
	if raw := strings.TrimSpace(r.URL.Query().Get("router")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			http.Redirect(w, r, networkPath(id, tab), http.StatusSeeOther)
			return
		}
	}
	if pick, ok := preferredRouter(routers); ok {
		http.Redirect(w, r, networkPath(pick.ID, tab), http.StatusSeeOther)
		return
	}

	view := h.newNetworkPage(tab)
	view.Routers = routers
	h.render(w, r, http.StatusOK, "network.html", view)
}

// preferredRouter returns the device the operator most likely wants: the only
// one, or the one flagged as the default portal.
func preferredRouter(routers []database.Router) (database.Router, bool) {
	if len(routers) == 1 {
		return routers[0], true
	}
	for _, router := range routers {
		if router.DefaultPortal {
			return router, true
		}
	}
	return database.Router{}, false
}

// NetworkDetail renders the hotspot configuration of one device.
//
// A device that cannot be reached is not treated as an error: the page renders
// with the reason, the same way the device manager does, so a broken router
// cannot lock an operator out of the controller.
func (h *Handler) NetworkDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
	}

	view := h.newNetworkPage(tabFromRequest(r))
	view.Router = router
	view.Back = networkPath(router.ID, view.Tab)

	h.loadNetwork(ctx, view)

	// ?edit=<id> opens the editor of one row, pre-filled from the live values.
	if editID := strings.TrimSpace(r.URL.Query().Get("edit")); editID != "" {
		view.applyEdit(editID)
	}

	h.render(w, r, http.StatusOK, "network.html", view)
}

// loadNetwork fills the page with the live configuration of its device. Each
// menu is read independently so one unavailable command (an old build without
// /ip/hotspot/walled-garden/ip, say) still leaves the rest of the page usable.
func (h *Handler) loadNetwork(ctx context.Context, view *networkPage) {
	client, err := h.dialRouter(ctx, view.Router)
	if err != nil {
		view.LiveError = err.Error()
		view.LiveHint = routerErrorHint(err)
		return
	}
	defer client.Close()
	view.Live = true

	callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout)
	defer cancel()

	if servers, err := client.HotspotServers(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Hotspot servers unavailable: "+routerErrorHint(err))
	} else {
		view.Servers = servers
		for _, server := range servers {
			view.ServerNames = append(view.ServerNames, server.Name)
		}
	}

	if profiles, err := client.HotspotServerProfiles(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Server profiles unavailable: "+routerErrorHint(err))
	} else {
		view.ServerProfiles = profiles
		for _, profile := range profiles {
			view.ServerProfileNames = append(view.ServerProfileNames, profile.Name)
		}
	}

	if profiles, err := client.HotspotProfiles(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "User profiles unavailable: "+routerErrorHint(err))
	} else {
		view.UserProfiles = profiles
		for _, profile := range profiles {
			view.UserProfileNames = append(view.UserProfileNames, profile.Name)
		}
	}

	if entries, err := client.WalledGardenEntries(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Walled garden unavailable: "+routerErrorHint(err))
	} else {
		view.WalledGarden = entries
	}

	if entries, err := client.WalledGardenIPEntries(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Walled garden IP rules unavailable: "+routerErrorHint(err))
	} else {
		view.WalledGardenIP = entries
	}

	if interfaces, err := client.InterfaceList(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Interface list unavailable: "+routerErrorHint(err))
	} else {
		view.Interfaces = interfaces
	}

	if pools, err := client.IPPoolNames(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Address pools unavailable: "+routerErrorHint(err))
	} else {
		view.Pools = pools
	}

	if pools, err := client.IPPools(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "IP pool table unavailable: "+routerErrorHint(err))
	} else {
		view.PoolRows = pools
	}

	if vlans, err := client.BridgeVLANs(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Bridge VLAN table unavailable: "+routerErrorHint(err))
	} else {
		view.VLANs = vlans
	}

	if bridges, err := client.Bridges(callCtx); err != nil {
		view.Warnings = append(view.Warnings, "Bridge list unavailable: "+routerErrorHint(err))
	} else {
		view.Bridges = bridges
	}
}

// applyEdit pre-fills the editor of the row named by ?edit=. A stale link simply
// falls back to the create form instead of failing.
func (p *networkPage) applyEdit(objectID string) {
	switch p.Tab {
	case tabServerProfiles:
		for _, profile := range p.ServerProfiles {
			if profile.ID == objectID {
				p.Editing = objectID
				p.ServerProfileForm = hotspotServerProfileFormFromProfile(profile)
				return
			}
		}
	case tabUserProfiles:
		for _, profile := range p.UserProfiles {
			if profile.ID == objectID {
				p.Editing = objectID
				p.UserProfileForm = hotspotUserProfileFormFromProfile(profile)
				return
			}
		}
	case tabWalledGarden:
		for _, entry := range p.WalledGarden {
			if entry.ID == objectID {
				p.Editing = objectID
				p.WalledGardenForm = walledGardenFormFromEntry(entry)
				return
			}
		}
	case tabWalledGardenIP:
		for _, entry := range p.WalledGardenIP {
			if entry.ID == objectID {
				p.Editing = objectID
				p.WalledGardenIPForm = walledGardenIPFormFromEntry(entry)
				return
			}
		}
	default:
		for _, server := range p.Servers {
			if server.ID == objectID {
				p.Editing = objectID
				p.ServerForm = hotspotServerFormFromServer(server)
				return
			}
		}
	}
}

// renderNetworkErrors re-renders the page with a form that failed validation,
// reopening the editor with the values the operator typed.
func (h *Handler) renderNetworkErrors(w http.ResponseWriter, r *http.Request, view *networkPage) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return
	}
	view.Router = router
	view.Back = networkPath(router.ID, view.Tab)
	h.loadNetwork(ctx, view)
	h.render(w, r, http.StatusUnprocessableEntity, "network.html", view)
}

// networkDevice loads the device named in the path and opens its API
// connection, sending the operator back to the section when either step fails.
func (h *Handler) networkDevice(w http.ResponseWriter, r *http.Request, tab networkTab) (database.Router, *MikrotikClient, bool) {
	ctx := r.Context()
	id, ok := h.routerIDPath(w, r)
	if !ok {
		return database.Router{}, nil, false
	}
	router, err := h.db.Routers().Get(ctx, id)
	if err != nil {
		h.notFoundRouter(w, r, err)
		return database.Router{}, nil, false
	}
	client, err := h.dialRouter(ctx, router)
	if err != nil {
		h.flashAndRedirect(w, r, networkPath(router.ID, tab), "err",
			"Cannot reach "+router.Name+": "+routerErrorHint(err))
		return router, nil, false
	}
	return router, client, true
}

// networkObjectID reads the {sid} path segment, replying with a 400 when it is
// empty.
func (h *Handler) networkObjectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("sid"))
	if id == "" {
		http.Error(w, "invalid object id", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

// wantedDisabled reads the target state of a toggle form.
func wantedDisabled(r *http.Request) bool {
	return strings.TrimSpace(r.PostFormValue("disabled")) == "1"
}

// ---------------------------------------------------------------------------
// Hotspot servers
// ---------------------------------------------------------------------------

// HotspotServerCreate adds a hotspot server to the device.
func (h *Handler) HotspotServerCreate(w http.ResponseWriter, r *http.Request) {
	form := hotspotServerFormFromRequest(r)
	if !form.validate(true) {
		view := h.newNetworkPage(tabHotspotServers)
		view.ServerForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabHotspotServers)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	// The write always reaches the device: only RouterOS knows which
	// interface names exist, and a controller-side snapshot must never
	// invent a refusal it did not make. When the device does reject the
	// value, the same rejection is explained as a field error.
	id, err := client.AddHotspotServer(callCtx, form.spec())
	if err != nil {
		if h.explainInterfaceRefusal(callCtx, client, form, err) {
			view := h.newNetworkPage(tabHotspotServers)
			view.ServerForm = form
			h.renderNetworkErrors(w, r, view)
			return
		}
		h.flashErr(w, r, networkPath(router.ID, tabHotspotServers),
			"Creating the hotspot server failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabHotspotServers), "ok",
		"Hotspot server "+defaultValue(form.Name, id)+" created on "+router.Name)
}

// HotspotServerUpdate applies the editor to an existing hotspot server.
func (h *Handler) HotspotServerUpdate(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	form := hotspotServerFormFromRequest(r)
	if !form.validate(false) {
		view := h.newNetworkPage(tabHotspotServers)
		view.ServerForm = form
		view.Editing = objectID
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabHotspotServers)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	// Same principle as the create form: send it, and explain a refusal the
	// device actually made - a stale editor must not block a valid write.
	if err := client.SetHotspotServer(callCtx, objectID, form.spec()); err != nil {
		if h.explainInterfaceRefusal(callCtx, client, form, err) {
			view := h.newNetworkPage(tabHotspotServers)
			view.ServerForm = form
			view.Editing = objectID
			h.renderNetworkErrors(w, r, view)
			return
		}
		h.flashErr(w, r, networkPath(router.ID, tabHotspotServers),
			"Updating the hotspot server failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabHotspotServers), "ok",
		"Hotspot server "+objectID+" updated on "+router.Name)
}

// HotspotServerToggle enables or disables one hotspot server.
func (h *Handler) HotspotServerToggle(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	disabled := wantedDisabled(r)

	router, client, ok := h.networkDevice(w, r, tabHotspotServers)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetHotspotServerDisabled(callCtx, objectID, disabled); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabHotspotServers),
			"Changing the hotspot server state failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabHotspotServers), "ok",
		"Hotspot server "+objectID+stateLabel(disabled)+" on "+router.Name)
}

// stateLabel words the outcome of a toggle.
func stateLabel(disabled bool) string {
	if disabled {
		return " disabled"
	}
	return " enabled"
}

// HotspotServerDelete removes a hotspot server.
func (h *Handler) HotspotServerDelete(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}

	router, client, ok := h.networkDevice(w, r, tabHotspotServers)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.RemoveHotspotServer(callCtx, objectID); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabHotspotServers),
			"Deleting the hotspot server failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabHotspotServers), "ok",
		"Hotspot server "+objectID+" deleted from "+router.Name)
}

// ---------------------------------------------------------------------------
// Server profiles
// ---------------------------------------------------------------------------

// HotspotServerProfileCreate adds a hotspot server profile.
func (h *Handler) HotspotServerProfileCreate(w http.ResponseWriter, r *http.Request) {
	form := hotspotServerProfileFormFromRequest(r)
	if !form.validate(true) {
		view := h.newNetworkPage(tabServerProfiles)
		view.ServerProfileForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabServerProfiles)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	id, err := client.AddHotspotServerProfile(callCtx, form.spec())
	if err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabServerProfiles),
			"Creating the server profile failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabServerProfiles), "ok",
		"Server profile "+defaultValue(form.Name, id)+" created on "+router.Name)
}

// HotspotServerProfileUpdate applies the editor to an existing server profile.
func (h *Handler) HotspotServerProfileUpdate(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	form := hotspotServerProfileFormFromRequest(r)
	if !form.validate(false) {
		view := h.newNetworkPage(tabServerProfiles)
		view.ServerProfileForm = form
		view.Editing = objectID
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabServerProfiles)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetHotspotServerProfile(callCtx, objectID, form.spec()); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabServerProfiles),
			"Updating the server profile failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabServerProfiles), "ok",
		"Server profile "+objectID+" updated on "+router.Name)
}

// HotspotServerProfileDelete removes a server profile.
func (h *Handler) HotspotServerProfileDelete(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}

	router, client, ok := h.networkDevice(w, r, tabServerProfiles)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.RemoveHotspotServerProfile(callCtx, objectID); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabServerProfiles),
			"Deleting the server profile failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabServerProfiles), "ok",
		"Server profile "+objectID+" deleted from "+router.Name)
}

// ---------------------------------------------------------------------------
// User profiles
// ---------------------------------------------------------------------------

// HotspotUserProfileCreate adds a hotspot user profile.
func (h *Handler) HotspotUserProfileCreate(w http.ResponseWriter, r *http.Request) {
	form := hotspotUserProfileFormFromRequest(r)
	if !form.validate(true) {
		view := h.newNetworkPage(tabUserProfiles)
		view.UserProfileForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabUserProfiles)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	id, err := client.AddHotspotProfile(callCtx, form.spec())
	if err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabUserProfiles),
			"Creating the user profile failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabUserProfiles), "ok",
		"User profile "+defaultValue(form.Name, id)+" created on "+router.Name)
}

// HotspotUserProfileUpdate applies the editor to an existing user profile.
func (h *Handler) HotspotUserProfileUpdate(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	form := hotspotUserProfileFormFromRequest(r)
	if !form.validate(false) {
		view := h.newNetworkPage(tabUserProfiles)
		view.UserProfileForm = form
		view.Editing = objectID
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabUserProfiles)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetHotspotProfile(callCtx, objectID, form.spec()); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabUserProfiles),
			"Updating the user profile failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabUserProfiles), "ok",
		"User profile "+objectID+" updated on "+router.Name)
}

// HotspotUserProfileDelete removes a user profile.
func (h *Handler) HotspotUserProfileDelete(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}

	router, client, ok := h.networkDevice(w, r, tabUserProfiles)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.RemoveHotspotProfile(callCtx, objectID); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabUserProfiles),
			"Deleting the user profile failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabUserProfiles), "ok",
		"User profile "+objectID+" deleted from "+router.Name)
}

// ---------------------------------------------------------------------------
// Walled garden rules
// ---------------------------------------------------------------------------

// WalledGardenCreate adds a host based walled garden rule.
func (h *Handler) WalledGardenCreate(w http.ResponseWriter, r *http.Request) {
	form := walledGardenFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabWalledGarden)
		view.WalledGardenForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabWalledGarden)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	id, err := client.AddWalledGardenEntry(callCtx, form.spec())
	if err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGarden),
			"Creating the walled garden rule failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGarden), "ok",
		"Walled garden rule "+id+" created on "+router.Name)
}

// WalledGardenUpdate applies the editor to an existing host rule.
func (h *Handler) WalledGardenUpdate(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	form := walledGardenFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabWalledGarden)
		view.WalledGardenForm = form
		view.Editing = objectID
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabWalledGarden)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetWalledGardenEntry(callCtx, objectID, form.spec()); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGarden),
			"Updating the walled garden rule failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGarden), "ok",
		"Walled garden rule "+objectID+" updated on "+router.Name)
}

// WalledGardenToggle enables or disables one host rule.
func (h *Handler) WalledGardenToggle(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	disabled := wantedDisabled(r)

	router, client, ok := h.networkDevice(w, r, tabWalledGarden)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetWalledGardenEntryDisabled(callCtx, objectID, disabled); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGarden),
			"Changing the walled garden rule state failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGarden), "ok",
		"Walled garden rule "+objectID+stateLabel(disabled)+" on "+router.Name)
}

// WalledGardenDelete removes a host based walled garden rule.
func (h *Handler) WalledGardenDelete(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}

	router, client, ok := h.networkDevice(w, r, tabWalledGarden)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.RemoveWalledGardenEntry(callCtx, objectID); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGarden),
			"Deleting the walled garden rule failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGarden), "ok",
		"Walled garden rule "+objectID+" deleted from "+router.Name)
}

// WalledGardenIPCreate adds an IP based walled garden rule.
func (h *Handler) WalledGardenIPCreate(w http.ResponseWriter, r *http.Request) {
	form := walledGardenIPFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabWalledGardenIP)
		view.WalledGardenIPForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabWalledGardenIP)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	id, err := client.AddWalledGardenIPEntry(callCtx, form.spec())
	if err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGardenIP),
			"Creating the walled garden IP rule failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGardenIP), "ok",
		"Walled garden IP rule "+id+" created on "+router.Name)
}

// WalledGardenIPUpdate applies the editor to an existing IP rule.
func (h *Handler) WalledGardenIPUpdate(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	form := walledGardenIPFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabWalledGardenIP)
		view.WalledGardenIPForm = form
		view.Editing = objectID
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabWalledGardenIP)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetWalledGardenIPEntry(callCtx, objectID, form.spec()); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGardenIP),
			"Updating the walled garden IP rule failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGardenIP), "ok",
		"Walled garden IP rule "+objectID+" updated on "+router.Name)
}

// WalledGardenIPToggle enables or disables one IP rule.
func (h *Handler) WalledGardenIPToggle(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}
	disabled := wantedDisabled(r)

	router, client, ok := h.networkDevice(w, r, tabWalledGardenIP)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.SetWalledGardenIPEntryDisabled(callCtx, objectID, disabled); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGardenIP),
			"Changing the walled garden IP rule state failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGardenIP), "ok",
		"Walled garden IP rule "+objectID+stateLabel(disabled)+" on "+router.Name)
}

// WalledGardenIPDelete removes an IP based walled garden rule.
func (h *Handler) WalledGardenIPDelete(w http.ResponseWriter, r *http.Request) {
	objectID, ok := h.networkObjectID(w, r)
	if !ok {
		return
	}

	router, client, ok := h.networkDevice(w, r, tabWalledGardenIP)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if err := client.RemoveWalledGardenIPEntry(callCtx, objectID); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabWalledGardenIP),
			"Deleting the walled garden IP rule failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabWalledGardenIP), "ok",
		"Walled garden IP rule "+objectID+" deleted from "+router.Name)
}

// ---------------------------------------------------------------------------
// IP pools
// ---------------------------------------------------------------------------

// IPPoolCreate adds an address pool to /ip/pool. The section is add-only:
// shrinking a pool that hands out leases is done on the device, where the used
// portion is visible.
func (h *Handler) IPPoolCreate(w http.ResponseWriter, r *http.Request) {
	form := ipPoolFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabPools)
		view.PoolForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabPools)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	id, err := client.AddIPPool(callCtx, form.Name, form.Ranges, form.NextPool, form.Comment)
	if err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabPools),
			"Creating the IP pool failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabPools), "ok",
		"IP pool "+defaultValue(form.Name, id)+" created on "+router.Name)
}

// ---------------------------------------------------------------------------
// Bridge VLANs
// ---------------------------------------------------------------------------

// BridgeVLANCreate adds one VLAN or a VLAN-ID range to a bridge's VLAN table.
// Like the pools, entries are created here and removed on the device.
func (h *Handler) BridgeVLANCreate(w http.ResponseWriter, r *http.Request) {
	form := bridgeVLANFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabVLANs)
		view.VLANForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabVLANs)
	if !ok {
		return
	}
	defer client.Close()

	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	if _, err := client.AddBridgeVLAN(callCtx, form.Bridge, form.VLANIDs, form.Tagged, form.Untagged); err != nil {
		h.flashErr(w, r, networkPath(router.ID, tabVLANs),
			"Creating the bridge VLAN entry failed", err)
		return
	}
	h.flashAndRedirect(w, r, networkPath(router.ID, tabVLANs), "ok",
		"VLAN "+form.VLANIDs+" created on "+router.Name+" ("+form.Bridge+")")
}

// ---------------------------------------------------------------------------
// Editor option lists
// ---------------------------------------------------------------------------
//
// The selects and checkbox groups of the editors are driven by the same lists
// the validators check against, so a value the template offers can never be
// rejected by validate(). They are methods on the form rather than template
// functions because the template already holds the form in a variable.

// LoginByOptions lists every value the login-by set accepts.
func (f *hotspotServerProfileForm) LoginByOptions() []string { return loginByMethods }

// MACAuthModeOptions lists every value mac-auth-mode accepts.
func (f *hotspotServerProfileForm) MACAuthModeOptions() []string { return macAuthModes }

// NASPortTypeOptions lists every value nas-port-type accepts.
func (f *hotspotServerProfileForm) NASPortTypeOptions() []string { return nasPortTypes }

// RadiusMACFormatOptions lists every documented RADIUS MAC format.
func (f *hotspotServerProfileForm) RadiusMACFormatOptions() []string { return radiusMACFormats }

// LoginByAll reports whether every login method is ticked, so the template can
// render the group with all boxes checked when the device reports an empty set.
func (f *hotspotServerProfileForm) LoginByAll() bool {
	return len(f.LoginBy) == 0
}

// OpenStatusPageOptions lists every value open-status-page accepts.
func (f *hotspotUserProfileForm) OpenStatusPageOptions() []string { return openStatusPages }

// QueuePositionOptions lists every value insert-queue-before accepts.
func (f *hotspotUserProfileForm) QueuePositionOptions() []string { return queuePositions }

// MethodOptions lists every HTTP method the host walled garden accepts.
func (f *walledGardenForm) MethodOptions() []string { return walledGardenMethods }

// MethodAll reports whether the method group should render with every box
// ticked. RouterOS treats an empty method set as "all methods", so the form
// shows that state explicitly instead of looking like nothing is selected.
func (f *walledGardenForm) MethodAll() bool { return len(f.Method) == 0 }

// ActionOptions lists the allow/deny action of a host rule.
func (f *walledGardenForm) ActionOptions() []string { return walledGardenActions }

// ActionOptions lists the accept/drop/reject action of an IP rule.
func (f *walledGardenIPForm) ActionOptions() []string { return walledGardenIPActions }

// ServerOptions returns the server names an editor may bind a rule to, with an
// empty entry first so "all servers" stays selectable.
func (p *networkPage) ServerOptions() []string {
	return append([]string{""}, p.ServerNames...)
}
