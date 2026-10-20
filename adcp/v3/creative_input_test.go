package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validLocalization is a request-shaped core/creative-localization.json
// object (source / target_variants / default_locale_variant_id /
// unmatched_locale_action) — not the response readback shape, whose
// variants[]/role form belongs to creative-localization-readback.json.
const validLocalization = `{
	"source": {"locale_variant_id": "loc_en_us", "locale": "en-US"},
	"target_variants": [{
		"locale_variant_id": "loc_es_mx",
		"locale": "es-MX",
		"assets": {"headline": {"asset_type": "text", "content": "Oferta de verano"}}
	}],
	"default_locale_variant_id": "loc_en_us",
	"unmatched_locale_action": "serve_default"
}`

// TestCreativeInputRevisionAndLocalizationRoundTrip verifies CreativeInput
// carries revision_id and localization on the wire (issue #567). The
// sync-creatives-request schema adds both fields to the creative-asset item:
// revision_id is the buyer-assigned identity for the exact input-content
// state, and localization is the sync-only materialized-localization
// mutation. Before this change the typed SyncCreatives hook could neither
// receive nor send either field.
func TestCreativeInputRevisionAndLocalizationRoundTrip(t *testing.T) {
	// Schema-valid request item per creative/sync-creatives-request.json:
	// the item carries a format_id, the image asset carries width/height,
	// and localization uses the request shape.
	payload := `{
		"creative_id": "cr-1",
		"revision_id": "rev-7",
		"format_id": {"agent_url": "https://formats.example.com", "id": "display_static"},
		"name": "Summer Sale",
		"assets": {"image": {"asset_type": "image", "url": "https://cdn.example.com/summer.jpg", "width": 300, "height": 250}},
		"localization": ` + validLocalization + `
	}`

	var got CreativeInput
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	assert.Equal(t, "cr-1", got.CreativeID)
	assert.Equal(t, "rev-7", got.RevisionID)
	require.NotNil(t, got.FormatID)
	assert.Equal(t, "display_static", got.FormatID.ID)
	require.NotNil(t, got.Localization, "localization must not be dropped on decode")

	// Re-marshal and confirm the whole document survives unchanged —
	// value survival, not just key presence, for the localization object.
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var wantMap, gotMap map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &wantMap))
	require.NoError(t, json.Unmarshal(raw, &gotMap))
	assert.Equal(t, wantMap, gotMap, "CreativeInput must round-trip unchanged")

	// Both fields are omitted when unset, so existing buyers' payloads are
	// byte-identical to before.
	plain, err := json.Marshal(CreativeInput{CreativeID: "cr-2"})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "revision_id")
	assert.NotContains(t, string(plain), "localization")
}

// TestCreativeInputLocalizationThreeStates pins the three wire states of
// localization: omission preserves the existing localization, an object
// replaces it, and an explicit null removes it. Plain encoding/json
// decoding would collapse null to nil — the same as omission — losing the
// removal request; UnmarshalJSON decodes it to the ClearField() sentinel,
// which re-encodes as null.
func TestCreativeInputLocalizationThreeStates(t *testing.T) {
	base := `"creative_id": "cr-1", "revision_id": "rev-7"`

	t.Run("omitted stays omitted", func(t *testing.T) {
		var got CreativeInput
		require.NoError(t, json.Unmarshal([]byte(`{`+base+`}`), &got))
		assert.Nil(t, got.Localization)
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "localization")
	})

	t.Run("null decodes to ClearField and re-encodes as null", func(t *testing.T) {
		var got CreativeInput
		require.NoError(t, json.Unmarshal([]byte(`{`+base+`, "localization": null}`), &got))
		// Note: the sentinel is a typed nil pointer, so compare it
		// directly rather than asserting NotNil.
		assert.Equal(t, ClearField(), got.Localization,
			"explicit null must not collapse to omission; sellers detect a removal request via in.Localization == ClearField()")
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		var gotMap map[string]any
		require.NoError(t, json.Unmarshal(raw, &gotMap))
		v, present := gotMap["localization"]
		assert.True(t, present, "null state must survive re-encoding")
		assert.Nil(t, v)
	})

	t.Run("object round-trips as an object", func(t *testing.T) {
		var got CreativeInput
		payload := `{` + base + `, "localization": ` + validLocalization + `}`
		require.NoError(t, json.Unmarshal([]byte(payload), &got))
		require.NotNil(t, got.Localization)
		assert.NotEqual(t, ClearField(), got.Localization)
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		var wantMap, gotMap map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &wantMap))
		require.NoError(t, json.Unmarshal(raw, &gotMap))
		assert.Equal(t, wantMap, gotMap)
	})

	t.Run("buyer-side ClearField encodes as null", func(t *testing.T) {
		raw, err := json.Marshal(CreativeInput{CreativeID: "cr-1", Localization: ClearField()})
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"localization":null`)
	})
}
