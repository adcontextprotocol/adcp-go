package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// media-buy/get-media-buys-response.json requires confirmed_at on every media
// buy and types it ["string","null"]: the seller has not committed yet in
// deferred or manual approval flows. ConfirmedAt stays a string, so empty
// means "not confirmed" and MediaBuyData.MarshalJSON encodes it as null
// (adcontextprotocol/adcp-go#611).

const mediaBuyItemSchema = "media-buy/get-media-buys-response.json#/properties/media_buys/items"

func unconfirmedMediaBuy() MediaBuyData {
	return MediaBuyData{
		MediaBuyID:  "mb-1",
		Status:      "pending_start",
		Revision:    1,
		Currency:    "USD",
		TotalBudget: 100,
		Packages:    []PackageStatus{{Package: Package{PackageID: "pkg-1"}}},
	}
}

// encodeMediaBuy encodes v and decodes the result into a generic object, so a
// test can tell an absent key from a null one.
func encodeMediaBuy(t *testing.T, v any) (map[string]any, []byte) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))

	return wire, raw
}

func TestMediaBuyDataEmptyConfirmedAtEncodesNull(t *testing.T) {
	buy := unconfirmedMediaBuy()
	wire, raw := encodeMediaBuy(t, buy)

	value, present := wire["confirmed_at"]
	require.True(t, present, "confirmed_at is required and must not be omitted: %s", raw)
	assert.Nil(t, value, "an empty ConfirmedAt must encode as null")
	assertSchemaValid(t, mediaBuyItemSchema, raw)

	t.Run("through a pointer and a slice", func(t *testing.T) {
		for name, v := range map[string]any{"pointer": &buy, "slice": []MediaBuyData{buy}} {
			raw, err := json.Marshal(v)
			require.NoError(t, err, name)
			assert.Contains(t, string(raw), `"confirmed_at":null`, name)
		}
	})

	t.Run("inside a get_media_buys response", func(t *testing.T) {
		assertEncodingSchemaValid(t, "media-buy/get-media-buys-response.json", &GetMediaBuysResponse{
			Status:    TaskStatusCompleted,
			MediaBuys: []MediaBuyData{buy},
		})
		result, _, err := MediaBuysResponse([]MediaBuyData{buy}, false)
		require.NoError(t, err)
		assertEncodingSchemaValid(t, "media-buy/get-media-buys-response.json", result.StructuredContent)
	})
}

func TestMediaBuyDataConfirmedAtEncodesTheTimestamp(t *testing.T) {
	buy := unconfirmedMediaBuy()
	buy.ConfirmedAt = "2026-06-01T00:00:00Z"
	wire, raw := encodeMediaBuy(t, buy)

	assert.Equal(t, "2026-06-01T00:00:00Z", wire["confirmed_at"])
	assertSchemaValid(t, mediaBuyItemSchema, raw)
}

func TestMediaBuyDataDecodesNullConfirmedAt(t *testing.T) {
	const raw = `{"media_buy_id":"mb-1","status":"pending_start","revision":1,"currency":"USD","total_budget":100,"confirmed_at":null,"packages":[{"package_id":"pkg-1"}]}`
	assertSchemaValid(t, mediaBuyItemSchema, []byte(raw))

	var buy MediaBuyData
	require.NoError(t, json.Unmarshal([]byte(raw), &buy))
	assert.Empty(t, buy.ConfirmedAt)

	t.Run("round trip keeps null", func(t *testing.T) {
		wire, out := encodeMediaBuy(t, buy)
		assert.Contains(t, wire, "confirmed_at")
		assert.Nil(t, wire["confirmed_at"])
		assertSchemaValid(t, mediaBuyItemSchema, out)
	})
}

func TestMediaBuyDataValidActionsKeepTheirWireShape(t *testing.T) {
	cases := []struct {
		name    string
		actions []string
		want    any
		present bool
	}{
		{"nil is omitted", nil, nil, false},
		{"empty stays an empty array", []string{}, []any{}, true},
		{"values are listed", []string{"pause", "cancel"}, []any{"pause", "cancel"}, true},
	}
	for _, tc := range cases {
		for _, confirmedAt := range []string{"", "2026-06-01T00:00:00Z"} {
			t.Run(tc.name+" confirmed_at="+confirmedAt, func(t *testing.T) {
				buy := unconfirmedMediaBuy()
				buy.ConfirmedAt = confirmedAt
				buy.ValidActions = tc.actions
				wire, raw := encodeMediaBuy(t, buy)

				got, present := wire["valid_actions"]
				assert.Equal(t, tc.present, present, string(raw))
				assert.Equal(t, tc.want, got)
				assert.Contains(t, wire, "confirmed_at")
				assertSchemaValid(t, mediaBuyItemSchema, raw)
			})
		}
	}
}

// Ext marshals as its own object and cannot shadow the typed fields, with or
// without a confirmation time.
func TestMediaBuyDataExtCannotOverwriteConfirmedAt(t *testing.T) {
	buy := unconfirmedMediaBuy()
	buy.Ext = map[string]any{"confirmed_at": "2030-01-01T00:00:00Z"}
	wire, raw := encodeMediaBuy(t, buy)

	assert.Nil(t, wire["confirmed_at"])
	assert.Equal(t, map[string]any{"confirmed_at": "2030-01-01T00:00:00Z"}, wire["ext"])
	assertSchemaValid(t, mediaBuyItemSchema, raw)
}
