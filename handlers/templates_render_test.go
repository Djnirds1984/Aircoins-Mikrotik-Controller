package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// renderPage executes a page template the way Handler.render does, so a view
// field that does not exist - or a helper called with an argument of the wrong
// type - fails here instead of in the operator's browser, where it surfaces as
// HTTP 500 "template error". TestTemplatesParse only proves the files compile.
func renderPage(t *testing.T, name string, data pageRenderer) string {
	t.Helper()
	tmpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse %s: %v", TemplatePattern, err)
	}
	buf := new(bytes.Buffer)
	if err := tmpl.ExecuteTemplate(buf, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

// sampleTime is a fixed instant so the rendered labels stay deterministic.
var sampleTime = time.Date(2026, 9, 24, 9, 30, 0, 0, time.UTC)

func samplePage(title, nav string) page {
	return page{
		Title:      title,
		Nav:        nav,
		CSRFToken:  "csrf-token",
		PortalName: "Aircoins Hotspot",
		Version:    "1.2.3",
		Year:       2026,
		Back:       "/routers/1",
	}
}

func sampleRouter() database.Router {
	seen := sampleTime
	return database.Router{
		ID:            1,
		Name:          "OrangePi-Lab",
		Host:          "192.168.254.1",
		Port:          8728,
		Username:      "aircoins",
		Password:      "labsecret",
		UseTLS:        true,
		VerifyTLS:     true,
		Location:      "lab shelf",
		PortalTag:     "hq",
		DefaultPortal: true,
		Notes:         "installed 2026-09-21",
		Transport:     database.TransportAuto,
		RestPort:      443,
		LastTransport: database.TransportRESTSsl,
		LastStatus:    database.RouterStatusOnline,
		LastLatencyMS: 12,
		LastSeenAt:    &seen,
		CreatedAt:     sampleTime,
		UpdatedAt:     sampleTime,
	}
}

func sampleVoucher(status database.VoucherStatus) database.Voucher {
	routerID := int64(1)
	pushed := sampleTime
	expires := sampleTime.Add(24 * time.Hour)
	return database.Voucher{
		ID:              7,
		Code:            "AIR-2X4Q-9BNM",
		Batch:           "2026-09-24-A",
		RouterID:        &routerID,
		RouterName:      "OrangePi-Lab",
		Profile:         "default",
		DurationMinutes: 60,
		DataLimitMB:     500,
		DeviceLimit:     2,
		PriceCents:      1500,
		Status:          status,
		Uses:            1,
		MaxUses:         2,
		Note:            "front desk",
		CreatedAt:       sampleTime,
		PushedAt:        &pushed,
		ExpiresAt:       &expires,
	}
}

func sampleSession(ended bool) database.Session {
	session := database.Session{
		ID:         3,
		RouterID:   1,
		RouterName: "OrangePi-Lab",
		SessionKey: "*1",
		Username:   "AIR-2X4Q-9BNM",
		Address:    "10.5.50.42",
		MACAddress: "AA:BB:CC:DD:EE:FF",
		LoginBy:    "cookie",
		Server:     "hotspot1",
		Uptime:     "1h2m3s",
		BytesIn:    5_000_000,
		BytesOut:   1_500_000,
		StartedAt:  sampleTime.Add(-time.Hour),
		LastSeenAt: sampleTime,
	}
	if ended {
		end := sampleTime
		session.EndedAt = &end
		session.EndReason = "user-request"
	}
	return session
}

// sampleRouterPage fills every section of the device manager: cached sessions,
// the shared voucher table and the live device blocks.
func sampleRouterPage(live bool) *routerPage {
	router := sampleRouter()
	view := &routerPage{
		page:        samplePage(router.Name, "routers"),
		Router:      router,
		Form:        routerFormFromRouter(router),
		VoucherForm: newVoucherForm(router.ID),
		Live:        live,
	}
	view.Vouchers = []database.Voucher{
		sampleVoucher(database.VoucherUnused),
		sampleVoucher(database.VoucherDisabled),
	}
	view.VoucherStats = database.VoucherStats{
		Total: 2, Unused: 1, Disabled: 1,
		Pushed: 1, BilledCents: 1500, FaceValueCents: 3000,
	}
	view.CachedClients = []database.Session{sampleSession(false), sampleSession(true)}
	view.Warnings = []string{"IP bindings unavailable: the router refused the command"}
	view.Info = DeviceInfo{
		Identity: "MikroTik-Oran", Version: "7.16.2", BoardName: "hEX",
		Architecture: "arm", Uptime: "1w2d3h4m", CPULoad: 7,
		FreeMemory: 34_000_000, TotalMemory: 128_000_000,
	}
	view.Clients = []HotspotActive{{
		ID: "*1", User: "guest-tablet", Address: "10.5.50.42",
		MACAddress: "AA:BB:CC:DD:EE:FF", Uptime: "12m", LoginBy: "cookie",
		Server: "hotspot1", Comment: "walk-in", BytesIn: 1_000_000, BytesOut: 250_000,
	}}
	view.Bindings = []IPBinding{{
		ID: "*2", MACAddress: "AA:BB:CC:DD:EE:01", Address: "10.5.50.9",
		Type: "blocked", Comment: "unpaid guest",
	}}
	view.Profiles = []HotspotProfile{{
		ID: "*3", Name: "default", SharedUsers: 1, RateLimit: "2M/2M", SessionTime: "1h",
	}}
	view.Sync = database.SyncResult{Tracked: 3, Open: 2, Closed: 1}
	return view
}

// TestRouterDetailTemplateRendersOffline is the regression test for a router
// that cannot be reached - the state every device is in right after it is
// registered. The cached-data branch used to abort with "wrong type for value;
// expected int64; got int" because humanCount was called with len().
func TestRouterDetailTemplateRendersOffline(t *testing.T) {
	router := sampleRouter()
	view := &routerPage{
		page:        samplePage(router.Name, "routers"),
		Router:      router,
		Form:        routerFormFromRouter(router),
		VoucherForm: newVoucherForm(router.ID),
		LiveError:   "the request was cancelled or timed out at 192.168.254.1:8728 (dial)",
		LiveHint:    "the router took too long to answer",
	}
	body := renderPage(t, "router.html", view)
	for _, want := range []string{
		"Showing locally cached data",
		"No cached sessions for this device",
		"No vouchers for this router yet",
		"the request was cancelled or timed out",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("offline router page is missing %q", want)
		}
	}
}

// TestRouterDetailTemplateRendersWithData covers the populated branches: cached
// sessions (a time.Time column handed to sinceLabel), the shared voucher table
// and the live device sections.
func TestRouterDetailTemplateRendersWithData(t *testing.T) {
	cases := map[string]*routerPage{
		"offline with cached data": sampleRouterPage(false),
		"live device":              sampleRouterPage(true),
	}
	for name, view := range cases {
		t.Run(name, func(t *testing.T) {
			body := renderPage(t, "router.html", view)
			for _, want := range []string{"AIR-2X4Q-9BNM", "OrangePi-Lab", "Edit router", "Vouchers bound to"} {
				if !strings.Contains(body, want) {
					t.Errorf("router page is missing %q", want)
				}
			}
		})
	}
}

// TestPageTemplatesRender executes the remaining pages with populated data so a
// renamed view field or a changed helper signature cannot reach production.
func TestPageTemplatesRender(t *testing.T) {
	stats := database.Stats{
		RoutersTotal:   2,
		RoutersOnline:  1,
		RoutersOffline: 1,
		SessionsOpen:   3,
		Vouchers: database.VoucherStats{
			Total: 5, Unused: 2, Active: 1, Used: 1, Expired: 1,
			Pushed: 3, BilledCents: 1500, FaceValueCents: 7500,
		},
	}
	summaries := []routerSummary{{Router: sampleRouter(), OpenSessions: 3, Vouchers: 5}}
	vouchers := []database.Voucher{
		sampleVoucher(database.VoucherUnused),
		sampleVoucher(database.VoucherActive),
		sampleVoucher(database.VoucherDisabled),
	}
	sessions := []database.Session{sampleSession(false), sampleSession(true)}
	routers := []database.Router{sampleRouter()}

	invalidForm := newRouterForm()
	invalidForm.Name = "x"
	invalidForm.Errors["name"] = "Give the router a name so it can be identified later."
	invalidForm.Errors["host"] = "Enter the router IP address or hostname reachable on the API port."

	loginPortal := &portalPage{
		page:        samplePage("Hotspot sign in", ""),
		Portal:      portalRequest{MAC: "AA:BB:CC:DD:EE:FF", IP: "10.5.50.42", LinkOrig: "http://example.com/", ServerName: "hotspot1"},
		RouterName:  "OrangePi-Lab",
		RouterKnown: true,
		FormError:   "That voucher code is not valid.",
		Notice:      "Ask the front desk for a code.",
	}
	hotspotVoucher := sampleVoucher(database.VoucherActive)
	onlinePortal := &portalPage{
		page:        samplePage("Hotspot sign in", ""),
		Portal:      portalRequest{MAC: "AA:BB:CC:DD:EE:FF", IP: "10.5.50.42", Username: "AIR-2X4Q-9BNM"},
		RouterName:  "OrangePi-Lab",
		RouterKnown: true,
		Success:     true,
		Voucher:     &hotspotVoucher,
		RedirectTo:  "http://example.com/",
	}

	cases := []struct {
		name string
		tmpl string
		data pageRenderer
	}{
		{"dashboard", "dashboard.html", &dashboardPage{
			page: samplePage("Dashboard", "dashboard"), Stats: stats, Routers: summaries,
			Sessions: sessions, Vouchers: vouchers, Batches: []string{"2026-09-24-A"},
		}},
		{"dashboard empty", "dashboard.html", &dashboardPage{page: samplePage("Dashboard", "dashboard")}},
		{"router inventory", "routers.html", &routersPage{
			page: samplePage("Routers", "routers"), Form: newRouterForm(),
			Routers: summaries, Stats: stats,
		}},
		{"router detail", "router.html", routerPageForTransport(database.TransportAuto)},
		{"router detail rest-ssl", "router.html", routerPageForTransport(database.TransportRESTSsl)},
		{"router detail rest", "router.html", routerPageForTransport(database.TransportREST)},
		{"router detail api", "router.html", routerPageForTransport(database.TransportAPI)},
		{"router detail api-ssl", "router.html", routerPageForTransport(database.TransportAPISSL)},

		{"router inventory", "routers.html", &routersPage{
			page: samplePage("Routers", "routers"), Form: newRouterForm(),
			Routers: summaries, Stats: stats,
		}},
		{"router inventory errors", "routers.html", &routersPage{
			page: samplePage("Routers", "routers"), Form: invalidForm,
		}},
		{"session history", "sessions.html", &sessionsPage{
			page: samplePage("Sessions", "sessions"), Sessions: sessions, Routers: routers,
			Filter:  sessionFilter{RouterID: 1, Status: "open", Query: "guest", MAC: "AA:BB"},
			Total:   42,
			PageNo:  2,
			Pages:   5,
			PrevURL: "/sessions?page=1",
			NextURL: "/sessions?page=3",
		}},
		{"vouchers", "vouchers.html", &vouchersPage{
			page: samplePage("Vouchers", "vouchers"), Vouchers: vouchers, Routers: routers,
			Batches: []string{"2026-09-24-A"}, Stats: stats.Vouchers,
			Filter:  voucherFilter{Status: "unused", RouterID: 1, Batch: "2026-09-24-A", Query: "AIR"},
			Form:    newVoucherForm(0),
			Total:   5,
			PageNo:  1,
			Pages:   3,
			NextURL: "/vouchers?page=2",
		}},
		{"portal login", "portal.html", loginPortal},
		{"portal success", "portal.html", onlinePortal},
		{"error", "error.html", &errorPage{
			page:    samplePage("Router not found", ""),
			Message: "That router is not in the inventory any more.",
			Detail:  "database: not found",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			renderPage(t, tc.tmpl, tc.data)
		})
	}
}

// routerPageForTransport builds the device manager for one connection method, so
// every branch of the transport selector and the connection details list is
// rendered by the suite.
func routerPageForTransport(transport string) *routerPage {
	router := sampleRouter()
	router.Transport = transport
	if transport == database.TransportREST || transport == database.TransportRESTSsl {
		router.LastTransport = transport
		router.RestPort = 0
	}
	return &routerPage{
		page:          samplePage("Router", "routers"),
		Router:        router,
		Live:          true,
		Info:          DeviceInfo{Identity: "Tolosa", Version: "7.16.2", BoardName: "RB5009UG+S+"},
		Clients:       []HotspotActive{{User: "dave", Address: "10.0.0.5", Uptime: "2h", BytesIn: 1024, BytesOut: 2048, LoginBy: "voucher"}},
		CachedClients: []database.Session{{ID: 1, RouterID: 1, Username: "dave", Address: "10.0.0.5", MACAddress: "AA:BB:CC:00:00:05", LoginBy: "voucher", Server: "hotspot1", Uptime: "2h", StartedAt: sampleTime, LastSeenAt: sampleTime}},
		Form:          routerFormFromRouter(router),
		VoucherForm:   newVoucherForm(router.ID),
	}
}

// TestNetworkTemplateRenders covers every tab of the Network page with a row in
// each section, so the editor tables are exercised as well as the empty states.
func TestNetworkTemplateRenders(t *testing.T) {
	handler := &Handler{}
	router := sampleRouter()
	for _, tab := range networkTabs {
		t.Run(string(tab), func(t *testing.T) {
			view := handler.newNetworkPage(tab)
			view.Router = router
			view.Routers = []database.Router{router}
			view.Live = true
			view.Warnings = []string{"Interfaces unavailable: the router refused the command"}
			view.Servers = []HotspotServer{{
				ID: "*1", Name: "hotspot1", Interface: "ether2", AddressPool: "dhcp_pool1",
				Profile: "hsprof1", IdleTimeout: "5m", KeepaliveTimeout: "2m",
				LoginTimeout: "1m", AddressesPerMAC: "1", Comment: "guest wifi",
			}}
			view.ServerProfiles = []HotspotServerProfile{{
				ID: "*2", Name: "hsprof1", HotspotAddress: "10.5.50.1",
				DNSName: "wifi.local", LoginBy: "cookie", RateLimit: "2M/2M",
			}}
			view.UserProfiles = []HotspotProfile{{
				ID: "*3", Name: "default", SharedUsers: 1, RateLimit: "2M/2M", SessionTime: "1h",
			}}
			view.WalledGarden = []WalledGardenEntry{{
				ID: "*4", Server: "hotspot1", DstHost: "example.com", Method: "http",
				Action: "allow", DstAddress: "93.184.216.34", Hits: 12,
			}}
			view.WalledGardenIP = []WalledGardenIPEntry{{
				ID: "*5", Server: "hotspot1", DstAddress: "8.8.8.8", Protocol: "udp",
				DstPort: "53", Action: "accept", Comment: "DNS",
			}}
			view.Interfaces = []InterfaceStats{{
				ID: "*6", Name: "ether2", Type: "ether", MTU: 1500,
				MACAddress: "AA:BB:CC:00:00:02", RxBytes: 10_000_000, TxBytes: 2_000_000,
				RxPackets: 9000, TxPackets: 8000,
			}}
			view.Pools = []string{"dhcp_pool1"}
			view.PoolRows = []IPPool{{
				ID: "*7", Name: "hotspot_pool", Ranges: "10.5.50.10-10.5.50.200",
				NextPool: "", Comment: "guest leases",
			}}
			view.VLANs = []BridgeVLAN{{
				ID: "*8", Bridge: "bridge1", VLANIDs: "100-120",
				Tagged: "ether2", Untagged: "ether3", Current: "ether2",
			}}
			view.Bridges = []string{"bridge1"}
			view.ServerNames = []string{"hotspot1"}
			view.ServerProfileNames = []string{"hsprof1"}
			view.UserProfileNames = []string{"default"}
			renderPage(t, "network.html", view)
		})
	}
}

func TestToolsTemplateShowsHostZeroTierState(t *testing.T) {
	view := &hostToolsPage{page: samplePage("Tools", "tools"), ZeroTier: hostZeroTierStatus{
		OSName: "Debian GNU/Linux 12", OSID: "debian", Architecture: "aarch64",
		SupportedOS: true, Installed: true, Running: true, Online: true,
		NodeID: "b8034f7f60", Version: "1.14.2",
		Networks: []hostZeroTierNetwork{{ID: "e4da7455b23ac67d", Name: "CITYCONNECT", Type: "Public", Status: "OK", IPs: []string{"10.242.137.78/16"}}},
	}}
	body := renderPage(t, "tools.html", view)
	for _, want := range []string{
		`<a href="/tools" class="active">Tools</a>`, `b8034f7f60`, `RUNNING`,
		`CITYCONNECT`, `e4da7455b23ac67d`, `10.242.137.78/16`, `Already installed.`,
		`action="/tools/zerotier/leave"`, `action="/tools/zerotier/join"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("installed tools page is missing %q", want)
		}
	}
}

func TestToolsTemplateShowsInstallButtonOnlyWhenMissing(t *testing.T) {
	view := &hostToolsPage{page: samplePage("Tools", "tools"), ZeroTier: hostZeroTierStatus{
		OSName: "Ubuntu 24.04", OSID: "ubuntu", Architecture: "x86_64", SupportedOS: true, HelperReady: true,
	}}
	body := renderPage(t, "tools.html", view)
	for _, want := range []string{`action="/tools/zerotier/install"`, `>Install ZeroTier</button>`} {
		if !strings.Contains(body, want) {
			t.Errorf("uninstalled tools page is missing %q", want)
		}
	}
	if strings.Contains(body, "Already installed") {
		t.Error("uninstalled tools page claims ZeroTier is installed")
	}
}

func TestHostZeroTierNetworkIDValidation(t *testing.T) {
	for _, value := range []string{"805c2e21c0000001", "ABCDEF0123456789"} {
		if !validHostZeroTierNetworkID(value) {
			t.Errorf("valid network ID rejected: %q", value)
		}
	}
	for _, value := range []string{"", "805c2e21c000000", "805c2e21c00000011", "805c2e21c000000z"} {
		if validHostZeroTierNetworkID(value) {
			t.Errorf("invalid network ID accepted: %q", value)
		}
	}
}

func TestHotspotInstallerListsLiveRouterInterfaces(t *testing.T) {
	view := (&Handler{}).newNetworkPage(tabHotspotServers)
	view.Router = sampleRouter()
	view.Live = true
	view.Interfaces = []InterfaceStats{
		{Name: "ether1", Type: "ether"},
		{Name: "bridge-HS", Type: "bridge"},
	}
	view.InstallerForm.Interface = "bridge-HS"

	body := renderPage(t, "network.html", view)
	for _, want := range []string{
		`<select id="install-interface" name="interface" required>`,
		`<option value="ether1">ether1 (ether)</option>`,
		`<option value="bridge-HS" selected>bridge-HS (bridge)</option>`,
		`Interfaces fetched live from this MikroTik router.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hotspot installer page is missing %q", want)
		}
	}
}
