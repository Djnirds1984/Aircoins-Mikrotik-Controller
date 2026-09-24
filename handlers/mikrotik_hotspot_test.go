package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hotspotServerStub mimics the device behind remote.oxapsph.com:10775: the
// RouterOS build there rejects the "comment" property on /ip/hotspot with
// HTTP 400 "unknown parameter comment" (journalctl, Sep 24 06:39). Writes
// without a comment are accepted.
func hotspotServerStub(t *testing.T) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var puts, patches []string
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
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			answerHotspotWrite(w, string(raw))
		case strings.HasPrefix(r.URL.Path, "/rest/ip/hotspot/") && r.Method == http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			patches = append(patches, string(raw))
			answerHotspotWrite(w, string(raw))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &puts, &patches
}

// answerHotspotWrite rejects a body carrying "comment" exactly the way the
// device does and accepts anything else.
func answerHotspotWrite(w http.ResponseWriter, body string) {
	if strings.Contains(body, `"comment"`) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":400,"message":"Bad Request","detail":"unknown parameter comment"}`))
		return
	}
	_, _ = w.Write([]byte(`{"name":"hs1","interface":"ether1",".id":"*1"}`))
}

func TestAddHotspotServerDropsUnknownComment(t *testing.T) {
	server, puts, _ := hotspotServerStub(t)
	client := restTestClient(t, &restStub{server: server})
	defer client.Close()

	id, err := client.AddHotspotServer(context.Background(), HotspotServerSpec{
		Name:      "hs1",
		Interface: "ether1",
		Comment:   "",
	})
	if err != nil {
		t.Fatalf("AddHotspotServer: %v", err)
	}
	if id != "*1" {
		t.Fatalf("id = %q, want *1", id)
	}
	if len(*puts) != 2 {
		t.Fatalf("PUT count = %d, want 2 (reject then retry)", len(*puts))
	}
	if !strings.Contains((*puts)[0], `"comment"`) {
		t.Fatalf("first PUT should carry the comment the device rejects: %s", (*puts)[0])
	}
	if strings.Contains((*puts)[1], `"comment"`) {
		t.Fatalf("retry must drop the comment: %s", (*puts)[1])
	}
	if !strings.Contains((*puts)[1], `"name":"hs1"`) || !strings.Contains((*puts)[1], `"interface":"ether1"`) {
		t.Fatalf("retry lost other properties: %s", (*puts)[1])
	}
}

func TestSetHotspotServerDropsUnknownComment(t *testing.T) {
	server, _, patches := hotspotServerStub(t)
	client := restTestClient(t, &restStub{server: server})
	defer client.Close()

	// A non-empty comment travels the same road as a blank one: w.text always
	// sends the property, and the retry has to strip it regardless of value.
	if err := client.SetHotspotServer(context.Background(), "*1", HotspotServerSpec{
		Name:      "hs1",
		Interface: "ether1",
		Comment:   "front desk",
	}); err != nil {
		t.Fatalf("SetHotspotServer: %v", err)
	}
	if len(*patches) != 2 {
		t.Fatalf("PATCH count = %d, want 2 (reject then retry)", len(*patches))
	}
	if !strings.Contains((*patches)[0], `"comment"`) {
		t.Fatalf("first PATCH should carry the comment: %s", (*patches)[0])
	}
	if strings.Contains((*patches)[1], `"comment"`) {
		t.Fatalf("retry must drop the comment: %s", (*patches)[1])
	}
	if !strings.Contains((*patches)[1], `"name":"hs1"`) {
		t.Fatalf("retry lost other properties: %s", (*patches)[1])
	}
}

// TestHotspotServerCreateSurfacesDeviceErrorOnce pins two things: an error
// that is not an "unknown parameter" rejection must not be retried, and it
// must be decorated with the command exactly once (the transport already
// decorates; classify used to add a second copy).
func TestHotspotServerCreateSurfacesDeviceErrorOnce(t *testing.T) {
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
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":400,"message":"Bad Request","detail":"failure: cannot apply this configuration"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	client := restTestClient(t, &restStub{server: server})
	defer client.Close()

	_, err := client.AddHotspotServer(context.Background(), HotspotServerSpec{
		Name:      "hs1",
		Interface: "ether1",
	})
	if err == nil {
		t.Fatal("AddHotspotServer unexpectedly succeeded")
	}
	if len(puts) != 1 {
		t.Fatalf("PUT count = %d, want 1 (no blind retry)", len(puts))
	}
	if !errors.Is(err, ErrRouterDevice) {
		t.Fatalf("error sentinel = %v, want ErrRouterDevice", err)
	}
	if !strings.Contains(err.Error(), "failure: cannot apply this configuration") {
		t.Fatalf("error lost the device detail: %v", err)
	}
	if strings.Count(err.Error(), "(/ip/hotspot/add)") != 1 {
		t.Fatalf("error should name the command exactly once: %v", err)
	}
}

func TestStripUnknownParameter(t *testing.T) {
	args := []string{"=name=hs1", "=interface=ether1", "=comment=", "=disabled=no"}
	tests := []struct {
		message  string
		wantName string
		wantLen  int
	}{
		// The verbatim journalctl line, endpoint decoration included.
		{message: "unknown parameter comment Bad Request at remote.oxapsph.com:10775 (/ip/hotspot/add) at remote.oxapsph.com:10775 (/ip/hotspot/add)", wantName: "comment", wantLen: 3},
		{message: "unknown parameter comment", wantName: "comment", wantLen: 3},
		{message: "unknown parameter: \"comment\" rejected", wantName: "comment", wantLen: 3},
		// Unrelated failures must not strip anything.
		{message: "failure: cannot apply this configuration", wantName: "", wantLen: 4},
		// A property this request never sent: no strip, original error stands.
		{message: "unknown parameter vlan-tagging not supported", wantName: "", wantLen: 4},
	}
	for _, tc := range tests {
		stripped, name, ok := stripUnknownParameter(args, errors.New(tc.message))
		if name != tc.wantName {
			t.Errorf("%q: name = %q, want %q", tc.message, name, tc.wantName)
		}
		if len(stripped) != tc.wantLen {
			t.Errorf("%q: len(stripped) = %d, want %d", tc.message, len(stripped), tc.wantLen)
		}
		if ok != (len(stripped) < len(args)) {
			t.Errorf("%q: ok = %v, but stripping happened = %v", tc.message, ok, len(stripped) < len(args))
		}
	}
}
