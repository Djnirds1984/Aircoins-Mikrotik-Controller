package handlers

import (
	"context"
	"errors"
	"fmt"

	"net/http"
	"net/netip"
	"strings"
)

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
		Name: spec.ProfileName, HotspotAddress: spec.Address, DNSName: spec.DNSName,
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
