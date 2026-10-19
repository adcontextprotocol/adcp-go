package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreativeInputRevisionAndLocalizationRoundTrip verifies CreativeInput
// carries revision_id and localization on the wire (issue #567). The
// sync-creatives-request schema adds both fields to the creative-asset item:
// revision_id is the buyer-assigned identity for the exact input-content
// state, and localization is the sync-only materialized-localization
// mutation. Before this change the typed SyncCreatives hook could neither
// send nor echo either field, even though CreativeResult carries both.
func TestCreativeInputRevisionAndLocalizationRoundTrip(t *testing.T) {
	// Wire-shaped payload per creative/sync-creatives-request.json.
	const payload = `{
		"creative_id": "cr-1",
		"revision_id": "rev-7",
		"name": "Summer Sale",
		"assets": {"image": {"asset_type": "image", "url": "https://cdn.example.com/summer.jpg"}},
		"localization": {
			"default_locale_variant_id": "loc_en_us",
			"variants": [{"locale_variant_id": "loc_en_us", "locale": "en-US", "role": "source"}]
		}
	}`

	var got CreativeInput
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	assert.Equal(t, "cr-1", got.CreativeID)
	assert.Equal(t, "rev-7", got.RevisionID)
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
