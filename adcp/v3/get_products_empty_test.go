package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const getProductsResponseSchema = "media-buy/get-products-response.json"

// getProductsWire encodes v and returns the raw JSON and its top-level object.
func getProductsWire(t *testing.T, v any) ([]byte, map[string]json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wire))

	return raw, wire
}

// TestGetProductsResponseEmptyProducts pins the rule in
// media-buy/get-products-response.json: a standard response carries products[]
// even when it is empty; an unchanged confirmation and a rejected response
// must not carry it, and a failed one need not (the same under AdCP 3.3 rc.3).
func TestGetProductsResponseEmptyProducts(t *testing.T) {
	cases := []struct {
		name        string
		resp        GetProductsResponse
		wantProduct string // the exact products member, empty when it must be absent
	}{
		{
			name:        "standard, empty list",
			resp:        GetProductsResponse{Status: TaskStatusCompleted, CacheScope: "public", Products: []Product{}},
			wantProduct: `[]`,
		},
		{
			name:        "standard, nil list",
			resp:        GetProductsResponse{Status: TaskStatusCompleted, CacheScope: "public"},
			wantProduct: `[]`,
		},
		{
			name:        "standard, submitted",
			resp:        GetProductsResponse{Status: TaskStatusSubmitted, CacheScope: "public"},
			wantProduct: `[]`,
		},
		{
			name: "unchanged confirmation, empty list",
			resp: GetProductsResponse{
				Status: TaskStatusCompleted, Unchanged: Ptr(true), WholesaleFeedVersion: "feed-1",
				CacheScope: "public", Products: []Product{},
			},
		},
		{
			name: "unchanged confirmation, nil list",
			resp: GetProductsResponse{
				Status: TaskStatusCompleted, Unchanged: Ptr(true), WholesaleFeedVersion: "feed-1", CacheScope: "public",
			},
		},
		{
			name: "failed, no products",
			resp: GetProductsResponse{
				Status: TaskStatusFailed,
				Errors: []AdcpError{{"code": "INTERNAL_ERROR", "message": "upstream unavailable"}},
			},
		},
		{
			name: "failed, empty list",
			resp: GetProductsResponse{
				Status: TaskStatusFailed, Products: []Product{},
				Errors: []AdcpError{{"code": "INTERNAL_ERROR", "message": "upstream unavailable"}},
			},
		},
		{
			name: "rejected, empty list",
			resp: GetProductsResponse{Status: TaskStatusRejected, Reason: "No inventory matches this brand.", Products: []Product{}},
		},
		{
			name: "rejected, nil list",
			resp: GetProductsResponse{Status: TaskStatusRejected, Reason: "No inventory matches this brand."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, wire := getProductsWire(t, tc.resp)
			got, present := wire["products"]
			if tc.wantProduct == "" {
				assert.False(t, present, "products must be absent: %s", raw)
			} else {
				require.True(t, present, "products must be present: %s", raw)
				assert.JSONEq(t, tc.wantProduct, string(got))
			}
			assertSchemaValid(t, getProductsResponseSchema, raw)

			// Encoding through a pointer and inside a struct takes the same path.
			ptrRaw, err := json.Marshal(&tc.resp)
			require.NoError(t, err)
			assert.JSONEq(t, string(raw), string(ptrRaw))
			embedded, err := json.Marshal(struct{ R GetProductsResponse }{tc.resp})
			require.NoError(t, err)
			assert.JSONEq(t, `{"R":`+string(raw)+`}`, string(embedded))
		})
	}
}

// TestGetProductsResponseNonEmptyProductsUnchanged: a non-empty list encodes as
// it did before the rule, whatever the status, so no data is dropped silently.
func TestGetProductsResponseNonEmptyProductsUnchanged(t *testing.T) {
	products := []Product{{ProductID: "p1", Name: "Premium display"}}
	for _, tc := range []struct {
		name string
		resp GetProductsResponse
	}{
		{"standard", GetProductsResponse{Status: TaskStatusCompleted, CacheScope: "public", Products: products}},
		{"unchanged", GetProductsResponse{Status: TaskStatusCompleted, Unchanged: Ptr(true), Products: products}},
		{"rejected", GetProductsResponse{Status: TaskStatusRejected, Products: products}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, wire := getProductsWire(t, tc.resp)
			var got []Product
			require.NoError(t, json.Unmarshal(wire["products"], &got))
			require.Len(t, got, 1)
			assert.Equal(t, "p1", got[0].ProductID)
		})
	}
}

func TestGetProductsResponseEmptyProductsRoundTrip(t *testing.T) {
	raw, err := json.Marshal(GetProductsResponse{Status: TaskStatusCompleted, CacheScope: "public"})
	require.NoError(t, err)

	var decoded GetProductsResponse
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, TaskStatusCompleted, decoded.Status)
	assert.Empty(t, decoded.Products)

	again, err := json.Marshal(decoded)
	require.NoError(t, err)
	assert.JSONEq(t, string(raw), string(again))
}

// TestProductsDataEmptyProducts: the hand-written seller builder type follows
// the same rule; a nil list used to encode as `"products":null`.
func TestProductsDataEmptyProducts(t *testing.T) {
	cases := []struct {
		name        string
		data        ProductsData
		wantProduct string
	}{
		{"standard, nil list", ProductsData{Status: "completed", CacheScope: "public"}, `[]`},
		{"standard, empty list", ProductsData{Status: "completed", CacheScope: "public", Products: []Product{}}, `[]`},
		{"unchanged, empty list", ProductsData{Status: "completed", Unchanged: Ptr(true), WholesaleFeedVersion: "feed-1", CacheScope: "public", Products: []Product{}}, ""},
		{"unchanged, nil list", ProductsData{Status: "completed", Unchanged: Ptr(true), WholesaleFeedVersion: "feed-1", CacheScope: "public"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, wire := getProductsWire(t, tc.data)
			got, present := wire["products"]
			if tc.wantProduct == "" {
				assert.False(t, present, "products must be absent: %s", raw)
			} else {
				require.True(t, present, "products must be present: %s", raw)
				assert.JSONEq(t, tc.wantProduct, string(got))
			}
			assertSchemaValid(t, getProductsResponseSchema, raw)
		})
	}

	t.Run("non-empty list", func(t *testing.T) {
		_, wire := getProductsWire(t, ProductsData{Status: "completed", Products: []Product{{ProductID: "p1", Name: "n"}}})
		var got []Product
		require.NoError(t, json.Unmarshal(wire["products"], &got))
		require.Len(t, got, 1)
	})
}

// TestProductsResponseBuilderEmptyList: the builder a seller handler returns
// through emits a schema-valid response for an empty or nil catalog.
func TestProductsResponseBuilderEmptyList(t *testing.T) {
	for name, data := range map[string]*ProductsData{
		"nil list":   {CacheScope: "public"},
		"empty list": {CacheScope: "public", Products: []Product{}},
	} {
		t.Run(name, func(t *testing.T) {
			result, out, err := ProductsResponse(data)
			require.NoError(t, err)

			wire := structuredContentMap(t, result)
			assert.Equal(t, []any{}, wire["products"])
			assert.Equal(t, "completed", wire["status"])
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			assertSchemaValid(t, getProductsResponseSchema, raw)
		})
	}
}
