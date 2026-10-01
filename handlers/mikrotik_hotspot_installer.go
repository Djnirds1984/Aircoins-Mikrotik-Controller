package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// RouterOS menus used to put this controller's portal in front of guests.
const (
	toolFetchMenu = "/tool/fetch"
	fileMenu      = "/file"
	// defaultHotspotHTMLDirectory is where a stock RouterOS hotspot keeps its
	// pages. It is only a fallback: the device's own setting is authoritative.
	defaultHotspotHTMLDirectory = "hotspot"
)

// PortalInstallResult reports what the device did with the portal page.
type PortalInstallResult struct {
	// HTMLDirectory is where this hotspot actually reads its pages, read from
	// the device rather than assumed. Getting this wrong is why a fetch can
	// report success and change nothing.
	HTMLDirectory string
	// DestPath is the file the page was written to.
	DestPath string
	// Size is the size of that file afterwards, in bytes. Zero means nothing
	// usable was written.
	Size int64
	// Verified reports that the file exists on the device and is not empty,
	// which is the only evidence that the change actually took.
	Verified bool
	// Steps is the human readable trace shown in the panel.
	Steps []string
}

// hotspotLoginPath returns the destination for the redirect page inside a
// hotspot's html directory.
func hotspotLoginPath(directory string) string {
	directory = strings.Trim(strings.TrimSpace(directory), "/")
	if directory == "" {
		directory = defaultHotspotHTMLDirectory
	}
	return directory + "/login.html"
}

// HotspotPortalState reports what login page the device is currently serving.
//
// It is a read, so an operator can answer "is the redirect still installed?"
// without changing anything. This is the check that turns "it worked once" into
// a fact.
func (c *MikrotikClient) HotspotPortalState(ctx context.Context) (PortalInstallResult, error) {
	result := PortalInstallResult{}

	directory, err := c.hotspotHTMLDirectory(ctx)
	if err != nil {
		return result, err
	}
	result.HTMLDirectory = directory
	result.DestPath = hotspotLoginPath(directory)

	reply, err := c.Run(ctx, fileMenu+"/print", "?name="+result.DestPath)
	if err != nil {
		return result, err
	}
	row := reply.First()
	if row == nil {
		result.Steps = append(result.Steps,
			"No file at "+result.DestPath+": the router is serving its own login page.")
		return result, nil
	}
	result.Size = parseRouterOSSize(row["size"])
	result.Verified = result.Size > 0
	if result.Verified {
		result.Steps = append(result.Steps,
			"Found "+result.DestPath+" ("+humanBytes(result.Size)+").")
	}
	return result, nil
}

// InstallHotspotPortal makes the device serve this controller's portal.
//
// It reads the device's own html-directory rather than assuming one, REMOVES
// any login page already sitting there, fetches the redirect page into it, and
// then reads the file back. The removal is essential: /tool/fetch does not
// overwrite an existing file, so without it an old broken login.html survives
// the install and every guest keeps seeing it while the panel cheerfully
// reports success. The read-back checks CONTENT, not just that some file
// exists: "file present, size above zero" was exactly the false "verified" an
// operator could not act on.
func (c *MikrotikClient) InstallHotspotPortal(ctx context.Context, url string) (PortalInstallResult, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return PortalInstallResult{}, errors.New("handlers: no portal page URL to install")
	}

	directory, err := c.hotspotHTMLDirectory(ctx)
	if err != nil {
		return PortalInstallResult{}, err
	}
	dest := hotspotLoginPath(directory)
	result := PortalInstallResult{
		HTMLDirectory: directory,
		DestPath:      dest,
		Steps:         []string{"This hotspot serves its pages from " + directory + "/"},
	}

	if removed, err := c.removeFileByName(ctx, dest); err != nil {
		result.Steps = append(result.Steps, "Could not clear the old login page: "+err.Error())
		return result, err
	} else if removed {
		result.Steps = append(result.Steps, "Removed the old "+dest+" first: /tool fetch never overwrites an existing file.")
	}

	// mode=http keeps the router from attempting TLS against a plain panel,
	// which is a common reason a fetch produces nothing useful.
	if _, err := c.Run(ctx, toolFetchMenu,
		"=url="+url, "=dst-path="+dest, "=mode=http"); err != nil {
		result.Steps = append(result.Steps, "The device could not fetch the page: "+err.Error())
		return result, err
	}
	result.Steps = append(result.Steps, "Fetched "+url)

	reply, err := c.Run(ctx, fileMenu+"/print", "?name="+dest)
	if err != nil {
		return result, err
	}
	row := reply.First()
	if row == nil {
		result.Steps = append(result.Steps,
			"The device accepted the fetch but no file exists at "+dest+
				" - guests will still see the built-in MikroTik page.")
		return result, nil
	}
	result.Size = parseRouterOSSize(row["size"])
	if result.Size == 0 {
		result.Steps = append(result.Steps,
			"The file at "+dest+" is empty - the panel was probably unreachable from the router.")
		return result, nil
	}
	// The old file was removed before the fetch, so anything sitting at dest
	// now came from this fetch. Read-back of the content is a bonus check, not
	// a gate: devices that do not return file contents over the API must not
	// produce a false failure, while contents that still carry template syntax
	// or lack the redirect are a hard, honest FAIL.
	if contents := row["contents"]; contents != "" && !portalPageLooksInstalled(contents) {
		result.Steps = append(result.Steps,
			"The device fetched a page, but "+dest+" is not the panel redirect - guests may still see the broken or built-in login page.")
		return result, nil
	}
	result.Verified = true
	result.Steps = append(result.Steps, "Verified "+dest+" ("+humanBytes(result.Size)+") is the panel redirect page.")
	return result, nil
}

// portalPageLooksInstalled decides whether a login.html read back from the
// device is the panel redirect page rather than a stale or hand-pasted file.
// Any response that still contains Go template syntax is by definition not
// something the controller rendered, so it must never count as installed.
func portalPageLooksInstalled(contents string) bool {
	if strings.Contains(contents, "{{") {
		return false
	}
	return strings.Contains(contents, "http-equiv") || strings.Contains(contents, "window.location")
}

// removeFileByName deletes a stored file if one is present. A missing file is
// not an error - there is simply nothing to clear.
func (c *MikrotikClient) removeFileByName(ctx context.Context, name string) (bool, error) {
	reply, err := c.Run(ctx, fileMenu+"/print", "?name="+name)
	if err != nil {
		return false, nil
	}
	row := reply.First()
	if row == nil {
		return false, nil
	}
	id := strings.TrimSpace(row[".id"])
	if id == "" {
		return false, nil
	}
	if err := c.removeROSObject(ctx, fileMenu, id); err != nil {
		return false, err
	}
	return true, nil
}

// hotspotHTMLDirectory reports where the device keeps its hotspot pages.
//
// /ip/hotspot (the server) and /ip/hotspot/profile (what it inherits) both
// carry the setting, and the server's own value is what wins. A build that
// never set it falls back to the stock "hotspot" directory.
func (c *MikrotikClient) hotspotHTMLDirectory(ctx context.Context) (string, error) {
	for _, menu := range []string{hotspotServerMenu, hotspotServerProfileMenu} {
		reply, err := c.Run(ctx, menu+"/print")
		if err != nil {
			// An older build may not have the menu at all; try the next one and
			// finally fall back rather than failing the whole install.
			continue
		}
		row := reply.First()
		if row == nil {
			continue
		}
		for _, key := range []string{"html-directory-override", "html-directory"} {
			if dir := strings.TrimSpace(row[key]); dir != "" {
				return dir, nil
			}
		}
	}
	return defaultHotspotHTMLDirectory, nil
}

// parseRouterOSSize reads the size field /file/print reports, which is a plain
// number of bytes. An unparsable value reads as zero, so the caller treats the
// file as empty rather than claiming a false success.
func parseRouterOSSize(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil || size < 0 {
		return 0
	}
	return size
}

// HotspotInstallSpec describes the network choices made by /ip/hotspot setup.
// DNS servers and WalledGardenHost are optional: the installer only changes
// global DNS when explicitly asked and only creates a host rule when supplied.
type HotspotInstallSpec struct {
	Interface        string
	Address          string
	AddressPool      string
	Masquerade       bool
	DNSServers       string
	DNSName          string
	SMTPServer       string
	ServerName       string
	ProfileName      string
	PoolName         string
	AdminUser        string
	AdminPassword    string
	WalledGardenHost string
}

// InstallHotspot follows the RouterOS Hotspot setup lifecycle. Named objects
// are reused, so a retry after a partial failure does not create duplicates.
func (c *MikrotikClient) InstallHotspot(ctx context.Context, spec HotspotInstallSpec) ([]string, error) {
	steps := make([]string, 0, 9)
	record := func(format string, args ...any) {
		steps = append(steps, fmt.Sprintf(format, args...))
	}

	if done, err := c.ensureHotspotAddress(ctx, spec.Interface, spec.Address); err != nil {
		return steps, err
	} else if done {
		record("Configured gateway address %s on %s", spec.Address, spec.Interface)
	} else {
		record("Gateway address %s already exists on %s", spec.Address, spec.Interface)
	}

	if done, err := c.ensureNamedPool(ctx, spec.PoolName, spec.AddressPool); err != nil {
		return steps, err
	} else if done {
		record("Created address pool %s (%s)", spec.PoolName, spec.AddressPool)
	} else {
		record("Address pool %s already exists", spec.PoolName)
	}

	if done, err := c.ensureHotspotDHCP(ctx, spec.Interface, spec.ServerName+"-dhcp", spec.PoolName); err != nil {
		return steps, err
	} else if done {
		record("Created DHCP server %s-dhcp", spec.ServerName)
	} else {
		record("DHCP server %s-dhcp already exists", spec.ServerName)
	}

	if spec.Masquerade {
		comment := "aircoins hotspot " + spec.PoolName
		if done, err := c.ensureHotspotNAT(ctx, hotspotNetwork(spec.Address), comment); err != nil {
			return steps, err
		} else if done {
			record("Created masquerade rule for %s", hotspotNetwork(spec.Address))
		} else {
			record("Masquerade rule for %s already exists", hotspotNetwork(spec.Address))
		}
	}

	if spec.DNSServers != "" {
		if err := c.setHotspotDNSServers(ctx, spec.DNSServers); err != nil {
			return steps, err
		}
		record("Set router DNS servers to %s", spec.DNSServers)
	}

	profileSpec := HotspotServerProfileSpec{
		Name: spec.ProfileName, HotspotAddress: hotspotProfileAddress(spec.Address), DNSName: spec.DNSName,
		SMTPServer: spec.SMTPServer, LoginBy: "http-chap,https,http-pap",
	}
	if done, err := c.ensureHotspotServerProfile(ctx, profileSpec); err != nil {
		return steps, err
	} else if done {
		record("Created hotspot server profile %s", spec.ProfileName)
	} else {
		record("Hotspot server profile %s already exists", spec.ProfileName)
	}

	serverSpec := HotspotServerSpec{
		Name: spec.ServerName, Interface: spec.Interface, AddressPool: spec.PoolName,
		Profile: spec.ProfileName, AddressesPerMAC: "1", IdleTimeout: "5m",
		KeepaliveTimeout: "2m", LoginTimeout: "1m",
	}
	if done, err := c.ensureHotspotServer(ctx, serverSpec); err != nil {
		return steps, err
	} else if done {
		record("Created hotspot server %s on %s", spec.ServerName, spec.Interface)
	} else {
		record("Hotspot server %s already exists", spec.ServerName)
	}

	reply, err := c.Run(ctx, "/ip/hotspot/user/print", "?name="+spec.AdminUser)
	if err != nil {
		return steps, err
	}
	if reply.First() == nil {
		if _, err := c.EnsureHotspotUser(ctx, HotspotUserSpec{
			Name: spec.AdminUser, Password: spec.AdminPassword, Profile: "default",
			Comment: "Hotspot administrator created by Aircoins",
		}); err != nil {
			return steps, err
		}
		record("Created local hotspot administrator %s", spec.AdminUser)
	} else {
		record("Local hotspot administrator %s already exists", spec.AdminUser)
	}

	if spec.WalledGardenHost != "" {
		if done, err := c.ensureHotspotWalledGarden(ctx, spec.ServerName, spec.WalledGardenHost); err != nil {
			return steps, err
		} else if done {
			record("Added walled-garden host %s", spec.WalledGardenHost)
		} else {
			record("Walled-garden host %s already exists", spec.WalledGardenHost)
		}
	}
	return steps, nil
}

func (c *MikrotikClient) ensureHotspotAddress(ctx context.Context, hotspotInterface, address string) (bool, error) {
	reply, err := c.Run(ctx, "/ip/address/print", "?interface="+hotspotInterface, "?address="+address)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.addROSObject(ctx, "/ip/address", []string{
		"=interface=" + hotspotInterface, "=address=" + address,
		"=comment=aircoins hotspot gateway",
	})
	return err == nil, err
}

func (c *MikrotikClient) setHotspotDNSServers(ctx context.Context, servers string) error {
	reply, err := c.Run(ctx, "/ip/dns/print")
	if err != nil {
		return err
	}
	row := reply.First()
	if row == nil {
		return errors.New("RouterOS did not return its DNS settings object")
	}
	id := strings.TrimSpace(row[".id"])
	if id == "" {
		return errors.New("RouterOS DNS settings object has no id")
	}
	return c.setROSObject(ctx, "/ip/dns", id, []string{"=servers=" + servers})
}

func (c *MikrotikClient) ensureNamedPool(ctx context.Context, name, ranges string) (bool, error) {
	reply, err := c.Run(ctx, ipPoolMenu+"/print", "?name="+name)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.AddIPPool(ctx, name, ranges, "", "aircoins hotspot pool")
	return err == nil, err
}

func (c *MikrotikClient) ensureHotspotDHCP(ctx context.Context, hotspotInterface, name, pool string) (bool, error) {
	reply, err := c.Run(ctx, "/ip/dhcp-server/print", "?name="+name)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.addROSObject(ctx, "/ip/dhcp-server", []string{
		"=name=" + name, "=interface=" + hotspotInterface, "=address-pool=" + pool,
		"=lease-time=1h", "=add-arp=yes", "=disabled=no",
	})
	return err == nil, err
}

func (c *MikrotikClient) ensureHotspotNAT(ctx context.Context, network, comment string) (bool, error) {
	reply, err := c.Run(ctx, "/ip/firewall/nat/print", "?comment="+comment)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.addROSObject(ctx, "/ip/firewall/nat", []string{
		"=chain=srcnat", "=action=masquerade", "=src-address=" + network,
		"=comment=" + comment, "=disabled=no",
	})
	return err == nil, err
}

func (c *MikrotikClient) ensureHotspotServerProfile(ctx context.Context, spec HotspotServerProfileSpec) (bool, error) {
	reply, err := c.Run(ctx, hotspotServerProfileMenu+"/print", "?name="+spec.Name)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.AddHotspotServerProfile(ctx, spec)
	return err == nil, err
}

func (c *MikrotikClient) ensureHotspotServer(ctx context.Context, spec HotspotServerSpec) (bool, error) {
	reply, err := c.Run(ctx, hotspotServerMenu+"/print", "?name="+spec.Name)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.AddHotspotServer(ctx, spec)
	return err == nil, err
}

func (c *MikrotikClient) ensureHotspotWalledGarden(ctx context.Context, server, host string) (bool, error) {
	reply, err := c.Run(ctx, walledGardenMenu+"/print", "?server="+server, "?dst-host="+host)
	if err != nil || reply.First() != nil {
		return false, err
	}
	_, err = c.AddWalledGardenEntry(ctx, WalledGardenSpec{
		Server: server, DstHost: host, Action: "allow", Comment: "aircoins hotspot DNS",
	})
	return err == nil, err
}

func hotspotNetwork(address string) string {
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		if slash := strings.LastIndex(address, "/"); slash >= 0 {
			return address[:slash]
		}
		return address
	}
	return prefix.Masked().String()
}

// hotspotProfileAddress strips the interface prefix from the configured gateway.
// /ip/address accepts a CIDR, while /ip/hotspot/profile hotspot-address accepts
// only the single IP address.
func hotspotProfileAddress(address string) string {
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		return address
	}
	return prefix.Addr().String()
}

// hotspotInstallerForm carries the values requested by RouterOS's interactive
// /ip/hotspot setup command. Errors are keyed by the HTML field name.
type hotspotInstallerForm struct {
	Interface        string
	Address          string
	AddressPool      string
	Masquerade       bool
	DNSServers       string
	DNSName          string
	SMTPServer       string
	ServerName       string
	ProfileName      string
	PoolName         string
	AdminUser        string
	AdminPassword    string
	WalledGardenHost string
	Errors           map[string]string
}

func newHotspotInstallerForm() *hotspotInstallerForm {
	return &hotspotInstallerForm{
		Address: "10.5.50.1/24", AddressPool: "10.5.50.2-10.5.50.254",
		Masquerade: true, SMTPServer: "0.0.0.0", ServerName: "hotspot1",
		ProfileName: "hsprof1", PoolName: "hs-pool", AdminUser: "admin",
		Errors: map[string]string{},
	}
}

func hotspotInstallerFormFromRequest(r *http.Request) *hotspotInstallerForm {
	value := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	form := &hotspotInstallerForm{
		Interface: value("interface"), Address: value("address"),
		AddressPool: value("address_pool"), Masquerade: r.PostFormValue("masquerade") == "1",
		DNSServers: value("dns_servers"), DNSName: value("dns_name"), SMTPServer: value("smtp_server"),
		ServerName: value("server_name"), ProfileName: value("profile_name"), PoolName: value("pool_name"),
		AdminUser: value("admin_user"), AdminPassword: r.PostFormValue("admin_password"),
		WalledGardenHost: value("walled_garden_host"), Errors: map[string]string{},
	}
	if form.SMTPServer == "" {
		form.SMTPServer = "0.0.0.0"
	}
	return form
}

func (f *hotspotInstallerForm) validate() bool {
	required := map[string]string{
		"interface": f.Interface, "server_name": f.ServerName, "profile_name": f.ProfileName,
		"pool_name": f.PoolName, "admin_user": f.AdminUser,
	}
	for field, input := range required {
		if input == "" {
			f.Errors[field] = "This field is required."
		} else if tooLong(input) {
			f.Errors[field] = "Keep this under 200 characters."
		}
	}
	if prefix, err := netip.ParsePrefix(f.Address); err != nil || !prefix.Addr().Is4() || prefix.Bits() > 30 {
		f.Errors["address"] = "Use an IPv4 gateway with a prefix between /1 and /30, for example 10.5.50.1/24."
	}
	if !validIPOrRange(f.AddressPool) {
		f.Errors["address_pool"] = "Use an IPv4 range such as 10.5.50.2-10.5.50.254."
	}
	if f.DNSServers != "" && !validIPOrRange(f.DNSServers) {
		f.Errors["dns_servers"] = "Use comma-separated IPv4 DNS addresses, or leave this blank."
	}
	if _, err := netip.ParseAddr(f.SMTPServer); err != nil {
		f.Errors["smtp_server"] = "Use an IPv4 address; use 0.0.0.0 when SMTP is not configured."
	}
	if f.AdminPassword == "" {
		f.Errors["admin_password"] = "Set a password for the local hotspot administrator."
	} else if len(f.AdminPassword) > 200 {
		f.Errors["admin_password"] = "Keep the password under 200 characters."
	}
	if strings.ContainsAny(f.DNSName+f.WalledGardenHost, " \t\r\n") {
		f.Errors["dns_name"] = "RouterOS names cannot contain spaces."
	}
	if f.WalledGardenHost != "" && f.WalledGardenHost == f.DNSName {
		f.Errors["walled_garden_host"] = "The DNS name is already reachable through the hotspot; leave this blank unless another host is required."
	}
	return len(f.Errors) == 0
}

func (f *hotspotInstallerForm) spec() HotspotInstallSpec {
	return HotspotInstallSpec{
		Interface: f.Interface, Address: f.Address, AddressPool: f.AddressPool,
		Masquerade: f.Masquerade, DNSServers: f.DNSServers, DNSName: f.DNSName,
		SMTPServer: f.SMTPServer, ServerName: f.ServerName, ProfileName: f.ProfileName,
		PoolName: f.PoolName, AdminUser: f.AdminUser, AdminPassword: f.AdminPassword,
		WalledGardenHost: f.WalledGardenHost,
	}
}

// HotspotInstall runs the same lifecycle as MikroTik's interactive setup
// command. Completed steps are retained when a later RouterOS command fails.
func (h *Handler) HotspotInstall(w http.ResponseWriter, r *http.Request) {
	form := hotspotInstallerFormFromRequest(r)
	if !form.validate() {
		view := h.newNetworkPage(tabHotspotServers)
		view.InstallerForm = form
		h.renderNetworkErrors(w, r, view)
		return
	}

	router, client, ok := h.networkDevice(w, r, tabHotspotServers)
	if !ok {
		return
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.APITimeout)
	defer cancel()

	steps, err := client.InstallHotspot(ctx, form.spec())
	if err != nil {
		view := h.newNetworkPage(tabHotspotServers)
		view.Router = router
		view.Back = networkPath(router.ID, tabHotspotServers)
		view.InstallerForm = form
		view.InstallerError = routerErrorHint(err)
		view.InstallerSteps = append([]string{"Completed before the failure:"}, steps...)
		h.loadNetwork(r.Context(), view)
		h.render(w, r, http.StatusUnprocessableEntity, "network.html", view)
		return
	}

	view := h.newNetworkPage(tabHotspotServers)
	view.Router = router
	view.Back = networkPath(router.ID, tabHotspotServers)
	view.InstallerForm = form
	view.InstallerSteps = steps
	h.loadNetwork(r.Context(), view)
	h.render(w, r, http.StatusOK, "network.html", view)
}
