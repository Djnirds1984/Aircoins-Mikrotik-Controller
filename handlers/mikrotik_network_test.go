package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Two names that render identically must fold together, so a refusal caused by
// invisible bytes (a zero-width character pasted into the field, a soft hyphen
// in the device's name, a stray non-breaking space) can be pointed out instead
// of looking impossible.
func TestVisibleName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ether1", "ether1"},
		{" Bridge1-HS ", "bridge1-hs"},
		{"bridge1\u200b-HS", "bridge1-hs"},
		{"bridge1\u00ad-HS", "bridge1-hs"},
		{"bridge1\u00a0-HS", "bridge1-hs"},
		{"bridge1\u200bHS", "bridge1hs"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := visibleName(tc.in); got != tc.want {
			t.Errorf("visibleName(%q) = %q, want %q", tc.in, got, tc.want)
		}
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

// The controller must recognise the device's own refusal sentence and nothing
// else: no sentinel tags this condition, so the match runs on the decorated
// error text. Returning the property name keeps the door open for other
// properties to be explained the same way later.
func TestRefusedValueProperty(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"journal sentence", errors.New("input does not match any value of interface Bad Request at remote.oxapsph.com:10775 (/ip/hotspot/add)"), "interface"},
		{"other property", errors.New("failure: input does not match any value of address-pool"), "address-pool"},
		{"separator variant", errors.New("input does not match any value of: interface"), "interface"},
		{"unknown parameter", errors.New("unknown parameter comment Bad Request at host (/ip/hotspot/add)"), ""},
		{"generic device failure", errors.New("failure: cannot apply this configuration"), ""},
		{"nil", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusedValueProperty(tc.err); got != tc.want {
				t.Errorf("refusedValueProperty(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestInterfaceRefusalText(t *testing.T) {
	// The list is evidence under the device's verdict: the refused value is
	// quoted, static interfaces are listed, ephemeral sessions only counted.
	got := interfaceRefusalText("ghost0", []string{"ether1", "bridge-lan", "<pppoe-a>", "<pppoe-b>"})
	for _, want := range []string{
		`The device refused interface "ghost0".`,
		"ether1, bridge-lan",
		"omitted 2 dynamic session interfaces",
		"Not among the names the device reports: ghost0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "<pppoe-a>") {
		t.Errorf("session interfaces must not be spelled out: %s", got)
	}

	// The device lists something that only LOOKS like what was sent - the
	// classic case of a zero-width character or a soft hyphen arriving with a
	// copy-paste. Picking the entry from the list is the fix.
	got = interfaceRefusalText("bridge1-HS", []string{"ether1", "bridge1\u200b-HS"})
	if !strings.Contains(got, "pick it from the list") {
		t.Errorf("a look-alike name should be called out: %s", got)
	}
	if strings.Contains(got, "Not among the names") {
		t.Errorf("a look-alike name is not absent: %s", got)
	}

	// Byte-for-byte present, yet refused: the snapshot is stale.
	got = interfaceRefusalText("bridge1-HS", []string{"ether1", "bridge1-HS"})
	if !strings.Contains(got, "reload the page") {
		t.Errorf("an exact match should point at a stale list: %s", got)
	}

	// A multi-interface field is judged entry by entry, the way the write was.
	got = interfaceRefusalText("ether1,ghost0", []string{"ether1", "bridge-lan"})
	if !strings.Contains(got, "Not among the names the device reports: ghost0") {
		t.Errorf("the absent entry should be named: %s", got)
	}
	if strings.Contains(got, "Not among the names the device reports: ether1") {
		t.Errorf("the present entry must not be listed as absent: %s", got)
	}

	// Nothing read: the refusal still stands alone.
	if got := interfaceRefusalText("bridge1-HS", nil); got != `The device refused interface "bridge1-HS".` {
		t.Errorf("empty-list text = %q", got)
	}

	// A long list is capped so the field error stays readable.
	long := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		long = append(long, fmt.Sprintf("iface%02d", i))
	}
	got = interfaceRefusalText("nope", long)
	if !strings.Contains(got, "…") || strings.Contains(got, "iface21") {
		t.Errorf("list of %d not capped at 20: %s", len(long), got)
	}
}

// Only a refusal the DEVICE made becomes a field error - and it becomes one
// even when the interface list cannot be read, because the refusal is the
// established fact and the list is merely evidence. An unrelated device
// failure is never relabelled.
func TestExplainInterfaceRefusal(t *testing.T) {
	refusal := errors.New("input does not match any value of interface Bad Request at host (/ip/hotspot/add)")

	t.Run("readable list", func(t *testing.T) {
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
		form := &hotspotServerForm{Interface: "ghost0", Errors: map[string]string{}}
		if !h.explainInterfaceRefusal(context.Background(), client, form, refusal) {
			t.Fatal("a device interface refusal must become a field error")
		}
		msg := form.Errors["interface"]
		for _, want := range []string{"ghost0", "ether1, bridge-lan"} {
			if !strings.Contains(msg, want) {
				t.Errorf("field error %q is missing %q", msg, want)
			}
		}
	})

	t.Run("unreadable list", func(t *testing.T) {
		// networkStub404s /rest/interface/print: the listing is lost, the
		// explanation is not - the write already happened, and dropping to the
		// cryptic flash would hide a fact the controller holds.
		server, _ := networkStub(t)
		client := networkTestClient(t, server)
		defer client.Close()

		h := &Handler{log: slog.New(slog.DiscardHandler)}
		form := &hotspotServerForm{Interface: "ghost0", Errors: map[string]string{}}
		if !h.explainInterfaceRefusal(context.Background(), client, form, refusal) {
			t.Fatal("an unreadable list must not swallow the device's refusal")
		}
		msg := form.Errors["interface"]
		if !strings.Contains(msg, `refused interface "ghost0"`) {
			t.Errorf("refusal-only text missing: %q", msg)
		}
		if strings.Contains(msg, "Interfaces this router reports") {
			t.Errorf("no list was read, none may be claimed: %q", msg)
		}
	})

	t.Run("other failures are not rewritten", func(t *testing.T) {
		server, _ := networkStub(t)
		client := networkTestClient(t, server)
		defer client.Close()

		h := &Handler{log: slog.New(slog.DiscardHandler)}
		form := &hotspotServerForm{Interface: "ether1", Errors: map[string]string{}}
		if h.explainInterfaceRefusal(context.Background(), client, form,
			errors.New("failure: cannot apply this configuration")) {
			t.Error("an unrelated failure must be flashed unchanged, not relabelled")
		}
		if len(form.Errors) != 0 {
			t.Errorf("unexpected field errors: %v", form.Errors)
		}
	})
}
