package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// mustParseURL parses a URL for a table-driven request in a test.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}

// networkStub answers the four menus the Network page's pool and VLAN sections
// read and write, so the device layer is exercised over real HTTP.
func networkStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var puts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "aircoins" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/system/identity":
			_, _ = w.Write([]byte(`{"name":"Tolosa"}`))
		case r.URL.Path == "/rest/system/resource":
			_, _ = w.Write([]byte(`{"version":"7.16.2","board-name":"CHR","cpu":"ARM","free-memory":134217728,"total-memory":268435456}`))
		case r.URL.Path == "/rest/ip/pool" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"dhcp_pool1","ranges":"10.0.0.10-10.0.0.200","next-pool":"","comment":"lan"}]`))
		case r.URL.Path == "/rest/ip/pool" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			_, _ = w.Write([]byte(`{".id":"*1","name":"hotspot_pool","ranges":"10.5.50.10-10.5.50.200"}`))
		case r.URL.Path == "/rest/interface/bridge/vlan" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"bridge":"bridge1","vlan-ids":"100-120","tagged":"ether2","untagged":"ether3","current-tagged":"ether2","disabled":false}]`))
		case r.URL.Path == "/rest/interface/bridge/vlan" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			_, _ = w.Write([]byte(`{".id":"*2","bridge":"bridge1","vlan-ids":"100-120"}`))
		case r.URL.Path == "/rest/interface/bridge" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"bridge1"},{"name":"bridge-hotspot"}]`))
		case r.URL.Path == "/rest/interface/bridge/print" && r.Method == http.MethodPost:
			// A print carrying .proplist travels in the universal command form.
			_, _ = w.Write([]byte(`[{"name":"bridge1"},{"name":"bridge-hotspot"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &puts
}

// networkTestClient wires a client to the stub without the real dialer.
func networkTestClient(t *testing.T, server *httptest.Server) *MikrotikClient {
	t.Helper()
	client := restTestClient(t, &restStub{server: server})
	return client
}

func TestIPPoolDeviceLayer(t *testing.T) {
	server, puts := networkStub(t)
	client := networkTestClient(t, server)
	ctx := context.Background()

	pools, err := client.IPPools(ctx)
	if err != nil {
		t.Fatalf("IPPools: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "dhcp_pool1" || pools[0].Ranges != "10.0.0.10-10.0.0.200" {
		t.Fatalf("unexpected pools: %#v", pools)
	}

	id, err := client.AddIPPool(ctx, "hotspot_pool", "10.5.50.10-10.5.50.200", "", "guest")
	if err != nil {
		t.Fatalf("AddIPPool: %v", err)
	}
	if id != "*1" {
		t.Fatalf("AddIPPool id = %q, want *1", id)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected one PUT body, got %d", len(*puts))
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte((*puts)[0]), &fields); err != nil {
		t.Fatalf("PUT body %q is not an object: %v", (*puts)[0], err)
	}
	if fields["ranges"] != "10.5.50.10-10.5.50.200" || fields["name"] != "hotspot_pool" {
		t.Fatalf("unexpected PUT fields: %#v", fields)
	}
	if fields["comment"] != "guest" {
		t.Fatalf("comment = %q, want guest", fields["comment"])
	}
}

func TestBridgeVLANDeviceLayer(t *testing.T) {
	server, puts := networkStub(t)
	client := networkTestClient(t, server)
	ctx := context.Background()

	entries, err := client.BridgeVLANs(ctx)
	if err != nil {
		t.Fatalf("BridgeVLANs: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %#v", entries)
	}
	entry := entries[0]
	if entry.VLANIDs != "100-120" || entry.Bridge != "bridge1" || entry.Disabled {
		t.Fatalf("unexpected entry: %#v", entry)
	}

	bridges, err := client.Bridges(ctx)
	if err != nil {
		t.Fatalf("Bridges: %v", err)
	}
	if len(bridges) != 2 || bridges[0] != "bridge1" {
		t.Fatalf("unexpected bridges: %#v", bridges)
	}

	id, err := client.AddBridgeVLAN(ctx, "bridge1", "100-120", "ether2", "ether3")
	if err != nil {
		t.Fatalf("AddBridgeVLAN: %v", err)
	}
	if !strings.Contains(id, "*") {
		t.Fatalf("AddBridgeVLAN id = %q, want the id the device reported", id)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected one PUT body, got %d", len(*puts))
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte((*puts)[0]), &fields); err != nil {
		t.Fatalf("PUT body %q is not an object: %v", (*puts)[0], err)
	}
	if fields["vlan-ids"] != "100-120" || fields["bridge"] != "bridge1" ||
		fields["tagged"] != "ether2" || fields["untagged"] != "ether3" {
		t.Fatalf("unexpected PUT fields: %#v", fields)
	}
}

func TestValidVLANIDSpec(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"100", true},
		{"100-120", true},
		{"1,100,4094", true},
		{"100-120,200-210", true},
		{"10-10", true},
		{"", false},
		{"0", false},
		{"4095", false},
		{"120-100", false},
		{"100-", false},
		{"-120", false},
		{"abc", false},
		{"100-abc", false},
		{"100,abc", false},
	}
	for _, tc := range cases {
		if got := validVLANIDSpec(tc.value); got != tc.want {
			t.Errorf("validVLANIDSpec(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestIPPoolFormValidate(t *testing.T) {
	valid := ipPoolForm{Name: "hotspot_pool", Ranges: "10.0.0.10-10.0.0.200"}
	if !valid.validate() {
		t.Fatalf("expected a valid pool form, got errors: %#v", valid.Errors)
	}

	noName := ipPoolForm{Ranges: "10.0.0.10-10.0.0.200"}
	if noName.validate() {
		t.Fatal("pool without a name should fail")
	}
	if noName.Errors["name"] == "" {
		t.Error("missing name error")
	}

	badRanges := ipPoolForm{Name: "p", Ranges: "not-an-address"}
	if badRanges.validate() {
		t.Fatal("pool with a bad range should fail")
	}
	if badRanges.Errors["ranges"] == "" {
		t.Error("missing ranges error")
	}

	empty := ipPoolForm{}
	if empty.validate() {
		t.Fatal("empty pool form should fail")
	}
}

func TestBridgeVLANFormValidate(t *testing.T) {
	valid := bridgeVLANForm{Bridge: "bridge1", VLANIDs: "100-120", Tagged: "ether2"}
	if !valid.validate() {
		t.Fatalf("expected a valid vlan form, got errors: %#v", valid.Errors)
	}

	noBridge := bridgeVLANForm{VLANIDs: "100", Tagged: "ether2"}
	if noBridge.validate() {
		t.Fatal("vlan without a bridge should fail")
	}

	badIDs := bridgeVLANForm{Bridge: "bridge1", VLANIDs: "5000", Tagged: "ether2"}
	if badIDs.validate() {
		t.Fatal("vlan with id 5000 should fail")
	}
	if badIDs.Errors["vlan_ids"] == "" {
		t.Error("missing vlan_ids error")
	}

	noPorts := bridgeVLANForm{Bridge: "bridge1", VLANIDs: "100"}
	if noPorts.validate() {
		t.Fatal("vlan without any port should fail")
	}
	if noPorts.Errors["tagged"] == "" {
		t.Error("missing tagged error")
	}
}

func TestNetworkTabsIncludePoolsAndVLANs(t *testing.T) {
	if tabFromRequest(&http.Request{URL: mustParseURL(t, "/network/1?tab=pools")}) != tabPools {
		t.Error("tab=pools should select the pools section")
	}
	if tabFromRequest(&http.Request{URL: mustParseURL(t, "/network/1?tab=vlans")}) != tabVLANs {
		t.Error("tab=vlans should select the vlans section")
	}
	if tabFromRequest(&http.Request{URL: mustParseURL(t, "/network/1?tab=nope")}) != tabHotspotServers {
		t.Error("an unknown tab should fall back to the hotspot servers")
	}

	view := (&Handler{}).newNetworkPage(tabPools)
	view.PoolRows = []IPPool{{ID: "*1"}}
	if view.Count(tabPools) != 1 {
		t.Errorf("pools count = %d, want 1", view.Count(tabPools))
	}
	view.VLANs = []BridgeVLAN{{ID: "*2"}}
	if view.Count(tabVLANs) != 1 {
		t.Errorf("vlans count = %d, want 1", view.Count(tabVLANs))
	}
}

func TestMissingNames(t *testing.T) {
	available := []string{"ether1", "bridge-lan"}
	tests := []struct {
		name   string
		wanted []string
		want   string
	}{
		{"all present", []string{"ether1", "bridge-lan"}, ""},
		{"one missing", []string{"ghost0"}, "ghost0"},
		{"order kept", []string{"wlan9", "ether1", "vlan10"}, "wlan9,vlan10"},
		{"nothing wanted", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(missingNames(tc.wanted, available), ",")
			if got != tc.want {
				t.Errorf("missingNames(%v) = %q, want %q", tc.wanted, got, tc.want)
			}
		})
	}
}

// The interface field is free text, so whatever the browser posts is
// normalised here: stray spaces and empty entries must not survive to the
// device, where "ether1, ether2" would be one unmatchable name.
func TestHotspotServerFormNormalizesInterfaceList(t *testing.T) {
	form := url.Values{
		"name":      {"hs1"},
		"interface": {" ether1 , bridge-lan ,, "},
	}
	r := httptest.NewRequest(http.MethodPost, "/network/1/servers", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	got := hotspotServerFormFromRequest(r).Interface
	if got != "ether1,bridge-lan" {
		t.Errorf("Interface = %q, want %q", got, "ether1,bridge-lan")
	}

	// A field that holds nothing but separators must collapse to empty, so the
	// existing required-interface validation fires instead of the device.
	blank := url.Values{"name": {"hs1"}, "interface": {" , "}}
	r = httptest.NewRequest(http.MethodPost, "/network/1/servers", strings.NewReader(blank.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := hotspotServerFormFromRequest(r).Interface; got != "" {
		t.Errorf("Interface = %q, want empty", got)
	}
	if (hotspotServerFormFromRequest(r)).validate(true) {
		t.Error("a separator-only interface list should fail create validation")
	}
}

// A device that will not list its interfaces must not block the write: the
// check is an early warning, not a gate the transport can fail.
func TestCheckServerInterfacesAllowsWriteWhenListUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/system/identity" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"Tolosa"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
	}))
	t.Cleanup(server.Close)

	client := networkTestClient(t, server)
	defer client.Close()

	h := &Handler{log: slog.New(slog.DiscardHandler)}
	form := &hotspotServerForm{Interface: "ether1", Errors: map[string]string{}}
	if !h.checkServerInterfaces(context.Background(), client, form) {
		t.Fatal("checkServerInterfaces should not block the write when the list read fails")
	}
	if len(form.Errors) != 0 {
		t.Errorf("unexpected field errors: %v", form.Errors)
	}
}

// A name the device does not have must be reported against the field, naming
// both the offender and what the device offers - the message RouterOS would
// never have spelled out.
func TestCheckServerInterfacesReportsUnknownNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/system/identity":
			_, _ = w.Write([]byte(`{"name":"Tolosa"}`))
		case r.URL.Path == "/rest/interface/print":
			_, _ = w.Write([]byte(`[{"name":"ether1"},{"name":"bridge-lan"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)

	client := networkTestClient(t, server)
	defer client.Close()

	h := &Handler{log: slog.New(slog.DiscardHandler)}
	form := &hotspotServerForm{Interface: "ether1,ghost0", Errors: map[string]string{}}
	if h.checkServerInterfaces(context.Background(), client, form) {
		t.Fatal("checkServerInterfaces should refuse a write naming an interface the device lacks")
	}
	msg := form.Errors["interface"]
	if !strings.Contains(msg, "ghost0") {
		t.Errorf("field error %q does not name the missing interface", msg)
	}
	if !strings.Contains(msg, "ether1, bridge-lan") {
		t.Errorf("field error %q does not list the available interfaces", msg)
	}

	// Everything the device has: no objection.
	ok := &hotspotServerForm{Interface: "ether1,bridge-lan", Errors: map[string]string{}}
	if !h.checkServerInterfaces(context.Background(), client, ok) {
		t.Error("known interfaces should pass the check")
	}
	if len(ok.Errors) != 0 {
		t.Errorf("unexpected field errors: %v", ok.Errors)
	}
}
