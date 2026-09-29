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

// networkStub answers the menus the Network page's pool and VLAN interface
// sections read and write, so the device layer is exercised over real HTTP.
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
		case r.URL.Path == "/rest/interface/vlan" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"vlan100","interface":"bridge1","vlan-id":"100","mtu":"1500","comment":"guest","running":true,"disabled":false}]`))
		case r.URL.Path == "/rest/interface/vlan" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			_, _ = w.Write([]byte(`{".id":"*3","name":"vlan200","interface":"ether5","vlan-id":"200"}`))
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

func TestInterfaceVLANDeviceLayer(t *testing.T) {
	server, puts := networkStub(t)
	client := networkTestClient(t, server)
	ctx := context.Background()

	entries, err := client.InterfaceVLANs(ctx)
	if err != nil {
		t.Fatalf("InterfaceVLANs: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %#v", entries)
	}
	entry := entries[0]
	if entry.Name != "vlan100" || entry.Interface != "bridge1" || entry.VLANID != "100" ||
		entry.MTU != "1500" || !entry.Running || entry.Disabled {
		t.Fatalf("unexpected entry: %#v", entry)
	}

	id, err := client.AddInterfaceVLAN(ctx, "vlan200", "ether5", 200, "1500", "guest")
	if err != nil {
		t.Fatalf("AddInterfaceVLAN: %v", err)
	}
	if !strings.Contains(id, "*") {
		t.Fatalf("AddInterfaceVLAN id = %q, want the id the device reported", id)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected one PUT body, got %d", len(*puts))
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte((*puts)[0]), &fields); err != nil {
		t.Fatalf("PUT body %q is not an object: %v", (*puts)[0], err)
	}
	if fields["name"] != "vlan200" || fields["interface"] != "ether5" ||
		fields["vlan-id"] != "200" || fields["mtu"] != "1500" || fields["comment"] != "guest" {
		t.Fatalf("unexpected PUT fields: %#v", fields)
	}
	// The parent is a physical port here and a bridge above: /interface/vlan
	// takes any interface of the device, so nothing about the request is
	// bridge specific.
	if _, bridge := fields["bridge"]; bridge {
		t.Errorf("interface VLAN unexpectedly sent bridge: %#v", fields)
	}

	// MTU and comment are optional: a blank one is left out so the device
	// default stands.
	if _, err := client.AddInterfaceVLAN(ctx, "vlan300", "bridge1", 300, "", ""); err != nil {
		t.Fatalf("AddInterfaceVLAN without optional fields: %v", err)
	}
	if len(*puts) != 2 {
		t.Fatalf("expected two PUT bodies, got %d", len(*puts))
	}
	fields = nil
	if err := json.Unmarshal([]byte((*puts)[1]), &fields); err != nil {
		t.Fatalf("minimal PUT body %q is not an object: %v", (*puts)[1], err)
	}
	if fields["vlan-id"] != "300" || fields["interface"] != "bridge1" {
		t.Fatalf("unexpected minimal VLAN fields: %#v", fields)
	}
	if _, mtu := fields["mtu"]; mtu {
		t.Errorf("blank MTU unexpectedly sent: %#v", fields)
	}
	if _, comment := fields["comment"]; comment {
		t.Errorf("blank comment unexpectedly sent: %#v", fields)
	}
}

func TestParseVLANID(t *testing.T) {
	cases := []struct {
		value string
		want  int
		ok    bool
	}{
		{"100", 100, true},
		{"1", 1, true},
		{"4094", 4094, true},
		{" 100 ", 100, true},
		{"", 0, false},
		{"0", 0, false},
		{"4095", 0, false},
		// A range is not one VLAN ID: the editor creates one interface at a
		// time, so every ranged form is refused here.
		{"100-120", 0, false},
		{"100,200", 0, false},
		{"abc", 0, false},
		{"100-abc", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseVLANID(tc.value)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseVLANID(%q) = (%d, %v), want (%d, %v)", tc.value, got, ok, tc.want, tc.ok)
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

func TestVLANFormValidate(t *testing.T) {
	valid := vlanForm{Name: "vlan100", Interface: "ether5", VLANID: "100"}
	if !valid.validate() {
		t.Fatalf("expected a valid vlan form, got errors: %#v", valid.Errors)
	}
	if got := valid.vlanIDValue(); got != 100 {
		t.Errorf("vlanIDValue() = %d, want 100", got)
	}

	noName := vlanForm{Interface: "bridge1", VLANID: "100"}
	if noName.validate() {
		t.Fatal("vlan without a name should fail")
	}
	if noName.Errors["name"] == "" {
		t.Error("missing name error")
	}

	noParent := vlanForm{Name: "vlan100", VLANID: "100"}
	if noParent.validate() {
		t.Fatal("vlan without a parent interface should fail")
	}
	if noParent.Errors["interface"] == "" {
		t.Error("missing interface error")
	}

	// The parent may be a bridge or a physical port; both are just interface
	// names, so a physical port has to pass exactly like a bridge does.
	physical := vlanForm{Name: "vlan200", Interface: "sfp-sfpplus1", VLANID: "200"}
	if !physical.validate() {
		t.Fatalf("vlan on a physical port should be accepted, got errors: %#v", physical.Errors)
	}

	ranged := vlanForm{Name: "vlan100", Interface: "bridge1", VLANID: "100-120"}
	if ranged.validate() {
		t.Fatal("a VLAN range should fail")
	}
	if ranged.Errors["vlan_id"] == "" {
		t.Error("missing vlan_id error")
	}

	badID := vlanForm{Name: "vlan5000", Interface: "bridge1", VLANID: "5000"}
	if badID.validate() {
		t.Fatal("vlan with id 5000 should fail")
	}
	if badID.Errors["vlan_id"] == "" {
		t.Error("missing vlan_id error")
	}

	badMTU := vlanForm{Name: "vlan100", Interface: "ether5", VLANID: "100", MTU: "12"}
	if badMTU.validate() {
		t.Fatal("vlan with an MTU below 68 should fail")
	}
	if badMTU.Errors["mtu"] == "" {
		t.Error("missing mtu error")
	}

	// MTU and comment stay optional: the device default is the right answer for
	// an operator who does not care.
	optional := vlanForm{Name: "vlan100", Interface: "bridge1", VLANID: "100", Comment: "guest"}
	if !optional.validate() {
		t.Fatalf("vlan without an MTU should be accepted, got errors: %#v", optional.Errors)
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
	view.VLANs = []InterfaceVLAN{{ID: "*2"}}
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
