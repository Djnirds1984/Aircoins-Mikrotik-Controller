package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// zeroTierListNetworksSample is the shape `zerotier-cli -j listnetworks`
// returns on the panel host (ZeroTier 1.16.x). The addresses of a joined
// network live in "assignedAddresses" and the tunnel device in
// "portDeviceName"; there is no "ipAddress" field in this output.
const zeroTierListNetworksSample = `[{
  "allowDNS": false,
  "allowDefault": false,
  "allowGlobal": false,
  "allowManaged": true,
  "assignedAddresses": ["10.242.137.78/16"],
  "bridge": false,
  "broadcastEnabled": true,
  "dhcp": false,
  "id": "e4da7455b23ac67d",
  "mac": "a2:7e:9b:1d:2f:44",
  "mtu": 2800,
  "multicastSubscriptions": [],
  "name": "cityconnect",
  "netconfRevision": 12,
  "nwid": "e4da7455b23ac67d",
  "portDeviceName": "zt6a2xynq4",
  "portError": 0,
  "routes": [],
  "status": "OK",
  "type": "PUBLIC"
}]`

func TestParseHostZeroTierNetworksReadsInterfaceAndAddresses(t *testing.T) {
	networks, err := parseHostZeroTierNetworks([]byte(zeroTierListNetworksSample))
	if err != nil {
		t.Fatalf("parse listnetworks: %v", err)
	}
	if len(networks) != 1 {
		t.Fatalf("expected 1 network, got %d", len(networks))
	}
	got := networks[0]
	if got.ID != "e4da7455b23ac67d" || got.Name != "cityconnect" || got.Type != "PUBLIC" || got.Status != "OK" {
		t.Errorf("unexpected network row: %+v", got)
	}
	if got.Interface != "zt6a2xynq4" {
		t.Errorf("interface = %q, want %q", got.Interface, "zt6a2xynq4")
	}
	if len(got.IPs) != 1 || got.IPs[0] != "10.242.137.78/16" {
		t.Errorf("IPs = %v, want [10.242.137.78/16]", got.IPs)
	}
}

func TestParseHostZeroTierNetworksHandlesAlternateKeys(t *testing.T) {
	// Some builds report the network id as "nwid" only and the addresses under
	// "ipAddress"/"ipAssignments" instead of "assignedAddresses".
	rows := `[
      {"nwid":"805c2e21c0000001","name":"lab","type":"PRIVATE","status":"REQUESTING_CONFIGURATION","ipAddress":"10.147.17.5/24"},
      {"id":"805c2e21c0000002","assignedAddresses":["10.147.17.6/24"],"ipAssignments":["10.147.17.6/24","fd56:5799:d8f6:788e::1/88"]}
    ]`
	networks, err := parseHostZeroTierNetworks([]byte(rows))
	if err != nil {
		t.Fatalf("parse listnetworks: %v", err)
	}
	if len(networks) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(networks))
	}
	if networks[0].ID != "805c2e21c0000001" {
		t.Errorf("id = %q, want the nwid fallback", networks[0].ID)
	}
	if len(networks[0].IPs) != 1 || networks[0].IPs[0] != "10.147.17.5/24" {
		t.Errorf("IPs = %v, want [10.147.17.5/24]", networks[0].IPs)
	}
	if networks[0].Interface != "" {
		t.Errorf("interface = %q, want empty when the build omits portDeviceName", networks[0].Interface)
	}
	want := []string{"10.147.17.6/24", "fd56:5799:d8f6:788e::1/88"}
	if len(networks[1].IPs) != len(want) {
		t.Fatalf("IPs = %v, want %v (duplicates removed)", networks[1].IPs, want)
	}
	for i := range want {
		if networks[1].IPs[i] != want[i] {
			t.Errorf("IPs[%d] = %q, want %q", i, networks[1].IPs[i], want[i])
		}
	}
}

func TestParseHostZeroTierNetworksRejectsGarbage(t *testing.T) {
	if _, err := parseHostZeroTierNetworks([]byte("200 listnetworks <nwid> <name>")); err == nil {
		t.Error("expected a parse error for non-JSON output")
	}
	networks, err := parseHostZeroTierNetworks([]byte("[]"))
	if err != nil {
		t.Fatalf("empty network list should not fail: %v", err)
	}
	if len(networks) != 0 {
		t.Errorf("expected no networks, got %d", len(networks))
	}
}

func TestZeroTierAddressesNormalisesValues(t *testing.T) {
	addresses := zeroTierAddresses(
		json.RawMessage(`["10.0.0.1/24"," 10.0.0.1/24 ",""]`),
		json.RawMessage(`"10.0.0.2/24"`),
		nil,
		json.RawMessage(`null`),
	)
	want := []string{"10.0.0.1/24", "10.0.0.2/24"}
	if len(addresses) != len(want) {
		t.Fatalf("addresses = %v, want %v", addresses, want)
	}
	for i := range want {
		if addresses[i] != want[i] {
			t.Errorf("addresses[%d] = %q, want %q", i, addresses[i], want[i])
		}
	}
	if got := zeroTierAddresses(json.RawMessage(`[]`)); len(got) != 0 {
		t.Errorf("empty array should yield no addresses, got %v", got)
	}
}

func TestHostInterfaceAddressesWithoutDeviceName(t *testing.T) {
	// Without a device name the helper must not shell out at all, so the test
	// stays valid on platforms without iproute2 (for example Windows).
	for _, name := range []string{"", "   "} {
		if got := hostInterfaceAddresses(context.Background(), name); len(got) != 0 {
			t.Errorf("hostInterfaceAddresses(%q) = %v, want none", name, got)
		}
	}
}

func TestToolsTemplateShowsInterfaceAndMissingAddress(t *testing.T) {
	view := &hostToolsPage{page: samplePage("Tools", "tools"), ZeroTier: hostZeroTierStatus{
		OSName: "Armbian 26.8.1", OSID: "armbian", Architecture: "armv7l",
		SupportedOS: true, Installed: true, Running: true, Online: true,
		NodeID: "7520a29c29", Version: "1.16.2",
		Networks: []hostZeroTierNetwork{
			{ID: "e4da7455b23ac67d", Name: "cityconnect", Type: "PUBLIC", Status: "OK",
				Interface: "zt6a2xynq4", IPs: []string{"10.242.137.78/16"}},
			{ID: "805c2e21c0000001", Name: "lab", Type: "PRIVATE", Status: "REQUESTING_CONFIGURATION"},
		},
	}}
	body := renderPage(t, "tools.html", view)
	for _, want := range []string{
		`cityconnect`, `e4da7455b23ac67d`, `zt6a2xynq4`, `10.242.137.78/16`,
		`REQUESTING_CONFIGURATION`, `no IP assigned yet`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tools page is missing %q", want)
		}
	}
}
