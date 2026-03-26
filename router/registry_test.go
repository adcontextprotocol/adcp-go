package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistry_LoadFromData(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website", Domain: "www.oakwood.example.com"},
		{PropertyID: "pub-riverview", PropertyRID: 1002, PropertyType: "ctv_app", Domain: "app.riverview.example"},
		{PropertyID: "pub-pulsefit", PropertyRID: 1003, PropertyType: "mobile_app", Domain: "app.pulsefit.example"},
	}, 42)

	if reg.Count() != 3 {
		t.Fatalf("expected 3 properties, got %d", reg.Count())
	}
	if reg.Sequence() != 42 {
		t.Fatalf("expected sequence 42, got %d", reg.Sequence())
	}
}

func TestRegistry_LookupByID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website", Domain: "www.oakwood.example.com"},
	}, 1)

	p, ok := reg.LookupByID("pub-oakwood")
	if !ok {
		t.Fatal("expected to find pub-oakwood")
	}
	if p.PropertyRID != 1001 {
		t.Errorf("expected RID 1001, got %d", p.PropertyRID)
	}

	_, ok = reg.LookupByID("pub-nonexistent")
	if ok {
		t.Error("should not find nonexistent property")
	}
}

func TestRegistry_LookupByRID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website"},
	}, 1)

	p, ok := reg.LookupByRID(1001)
	if !ok {
		t.Fatal("expected to find RID 1001")
	}
	if p.PropertyID != "pub-oakwood" {
		t.Errorf("expected pub-oakwood, got %s", p.PropertyID)
	}
}

func TestRegistry_LookupByDomain(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, Domain: "www.oakwood.example.com"},
	}, 1)

	id, ok := reg.LookupByDomain("www.oakwood.example.com")
	if !ok {
		t.Fatal("expected to find domain")
	}
	if id != "pub-oakwood" {
		t.Errorf("expected pub-oakwood, got %s", id)
	}
}

func TestRegistry_PropertyRID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001},
	}, 1)

	if rid := reg.PropertyRID("pub-oakwood"); rid != 1001 {
		t.Errorf("expected 1001, got %d", rid)
	}
	if rid := reg.PropertyRID("pub-unknown"); rid != 0 {
		t.Errorf("expected 0 for unknown property, got %d", rid)
	}
}

func TestRegistry_ApplyUpdate_Add(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001},
	}, 1)

	reg.ApplyUpdate(&RegistryUpdate{
		Sequence: 2,
		Action:   "add",
		Property: RegistryProperty{PropertyID: "pub-newsite", PropertyRID: 1004, Domain: "newsite.example.com"},
	})

	if reg.Count() != 2 {
		t.Errorf("expected 2 properties after add, got %d", reg.Count())
	}
	if reg.Sequence() != 2 {
		t.Errorf("expected sequence 2, got %d", reg.Sequence())
	}
	if _, ok := reg.LookupByID("pub-newsite"); !ok {
		t.Error("expected to find newly added property")
	}
}

func TestRegistry_ApplyUpdate_Remove(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, Domain: "oakwood.example.com"},
		{PropertyID: "pub-remove-me", PropertyRID: 1002, Domain: "removeme.example.com"},
	}, 1)

	reg.ApplyUpdate(&RegistryUpdate{
		Sequence: 2,
		Action:   "remove",
		Property: RegistryProperty{PropertyID: "pub-remove-me"},
	})

	if reg.Count() != 1 {
		t.Errorf("expected 1 property after remove, got %d", reg.Count())
	}
	if _, ok := reg.LookupByID("pub-remove-me"); ok {
		t.Error("removed property should not be findable")
	}
	if _, ok := reg.LookupByDomain("removeme.example.com"); ok {
		t.Error("removed property's domain should not be findable")
	}
}

func TestRegistry_HandleSnapshot(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website"},
		{PropertyID: "pub-riverview", PropertyRID: 1002, PropertyType: "ctv_app"},
	}, 99)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/registry/snapshot", nil)
	reg.HandleSnapshot(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var snapshot RegistrySnapshot
	json.NewDecoder(w.Body).Decode(&snapshot)

	if len(snapshot.Properties) != 2 {
		t.Errorf("expected 2 properties in snapshot, got %d", len(snapshot.Properties))
	}
	if snapshot.Sequence != 99 {
		t.Errorf("expected sequence 99, got %d", snapshot.Sequence)
	}
	if w.Header().Get("X-Registry-Sequence") != "99" {
		t.Errorf("expected X-Registry-Sequence header, got %s", w.Header().Get("X-Registry-Sequence"))
	}
}

func TestRegistry_LoadSnapshot_FromRemote(t *testing.T) {
	// Serve a mock registry
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(RegistrySnapshot{
			Sequence: 50,
			Properties: []RegistryProperty{
				{PropertyID: "pub-remote-1", PropertyRID: 2001, PropertyType: "website", Domain: "remote1.example.com"},
				{PropertyID: "pub-remote-2", PropertyRID: 2002, PropertyType: "mobile_app"},
			},
		})
	}))
	defer server.Close()

	reg := NewRegistry(server.URL, "")
	if err := reg.LoadSnapshot(); err != nil {
		t.Fatal(err)
	}

	if reg.Count() != 2 {
		t.Errorf("expected 2 properties from remote, got %d", reg.Count())
	}
	if reg.Sequence() != 50 {
		t.Errorf("expected sequence 50, got %d", reg.Sequence())
	}
	if rid := reg.PropertyRID("pub-remote-1"); rid != 2001 {
		t.Errorf("expected RID 2001, got %d", rid)
	}
}

func TestRegistry_RouterEnrichesPropertyRID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website"},
	}, 1)

	// Create a router with registry and a mock provider that echoes property_rid
	var receivedRID uint64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PropertyRID uint64 `json:"property_rid"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		receivedRID = req.PropertyRID

		json.NewEncoder(w).Encode(map[string]interface{}{
			"request_id": "ctx-rid",
			"offers":     []interface{}{},
		})
	}))
	defer provider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "test", Endpoint: provider.URL, ContextMatch: true, Timeout: 5e9},
	}, reg, nil)

	reqBody := `{
		"request_id": "ctx-rid",
		"property_id": "pub-oakwood",
		"property_type": "website",
		"placement_id": "sidebar",
		"available_packages": [{"package_id": "pkg-1", "media_buy_id": "mb-1"}]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", nil)
	req.Body = io.NopCloser(strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	if receivedRID != 1001 {
		t.Errorf("expected provider to receive property_rid 1001, got %d", receivedRID)
	}
}
