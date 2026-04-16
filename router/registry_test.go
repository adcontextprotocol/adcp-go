package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_LoadFromData(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website", Domain: "www.oakwood.example.com"},
		{PropertyID: "pub-riverview", PropertyRID: 1002, PropertyType: "ctv_app", Domain: "app.riverview.example"},
		{PropertyID: "pub-pulsefit", PropertyRID: 1003, PropertyType: "mobile_app", Domain: "app.pulsefit.example"},
	}, 42)

	require.Equal(t, 3, reg.Count())
	require.Equal(t, uint64(42), reg.Sequence())
}

func TestRegistry_LookupByID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website", Domain: "www.oakwood.example.com"},
	}, 1)

	p, ok := reg.LookupByID("pub-oakwood")
	require.True(t, ok, "expected to find pub-oakwood")
	assert.Equal(t, uint64(1001), p.PropertyRID)

	_, ok = reg.LookupByID("pub-nonexistent")
	assert.False(t, ok, "should not find nonexistent property")
}

func TestRegistry_LookupByRID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, PropertyType: "website"},
	}, 1)

	p, ok := reg.LookupByRID(1001)
	require.True(t, ok, "expected to find RID 1001")
	assert.Equal(t, "pub-oakwood", p.PropertyID)
}

func TestRegistry_LookupByDomain(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001, Domain: "www.oakwood.example.com"},
	}, 1)

	id, ok := reg.LookupByDomain("www.oakwood.example.com")
	require.True(t, ok, "expected to find domain")
	assert.Equal(t, "pub-oakwood", id)
}

func TestRegistry_PropertyRID(t *testing.T) {
	reg := NewRegistry("", "")
	reg.LoadFromData([]RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1001},
	}, 1)

	assert.Equal(t, uint64(1001), reg.PropertyRID("pub-oakwood"))
	assert.Equal(t, uint64(0), reg.PropertyRID("pub-unknown"))
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

	assert.Equal(t, 2, reg.Count())
	assert.Equal(t, uint64(2), reg.Sequence())
	_, ok := reg.LookupByID("pub-newsite")
	assert.True(t, ok, "expected to find newly added property")
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

	assert.Equal(t, 1, reg.Count())
	_, ok := reg.LookupByID("pub-remove-me")
	assert.False(t, ok, "removed property should not be findable")
	_, ok = reg.LookupByDomain("removeme.example.com")
	assert.False(t, ok, "removed property's domain should not be findable")
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

	require.Equal(t, http.StatusOK, w.Code)

	var snapshot RegistrySnapshot
	_ = json.NewDecoder(w.Body).Decode(&snapshot)

	assert.Equal(t, 2, len(snapshot.Properties))
	assert.Equal(t, uint64(99), snapshot.Sequence)
	assert.Equal(t, "99", w.Header().Get("X-Registry-Sequence"))
}

func TestRegistry_LoadSnapshot_FromRemote(t *testing.T) {
	// Serve a mock registry
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(RegistrySnapshot{
			Sequence: 50,
			Properties: []RegistryProperty{
				{PropertyID: "pub-remote-1", PropertyRID: 2001, PropertyType: "website", Domain: "remote1.example.com"},
				{PropertyID: "pub-remote-2", PropertyRID: 2002, PropertyType: "mobile_app"},
			},
		})
	}))
	defer server.Close()

	reg := NewRegistry(server.URL, "")
	require.NoError(t, reg.LoadSnapshot())

	assert.Equal(t, 2, reg.Count())
	assert.Equal(t, uint64(50), reg.Sequence())
	assert.Equal(t, uint64(2001), reg.PropertyRID("pub-remote-1"))
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
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedRID = req.PropertyRID

		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "ctx-rid",
			"offers":     []any{},
		})
	}))
	defer provider.Close()

	router := testRouter([]ProviderConfig{
		{ID: "test", Endpoint: provider.URL, ContextMatch: true, Timeout: 5e9},
	})
	router.registry = reg

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

	assert.Equal(t, uint64(1001), receivedRID)
}
