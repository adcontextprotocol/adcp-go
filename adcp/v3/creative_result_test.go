package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreativeResultSpecFieldsRoundTrip verifies CreativeResult marshals and
// unmarshals every field of the sync-creatives-response.json per-creative item
// (issue #365: 8 fields were missing, rejection_reason was non-spec).
// The v3 schema also defines revision_id (absent from the v1 schema).
func TestCreativeResultSpecFieldsRoundTrip(t *testing.T) {
	// Wire-shaped payload per static/schemas/source/creative/sync-creatives-response.json.
	const payload = `{
		"creative_id": "cr-1",
		"revision_id": "rev-7",
		"account": {"account_id": "acct-1", "name": "Acme", "status": "active"},
		"action": "updated",
		"status": "approved",
		"platform_id": "gam-123",
		"changes": ["name", "format_id"],
		"errors": [],
		"warnings": ["low resolution asset"],
		"preview_url": "https://example.com/preview/cr-1",
		"expires_at": "2026-12-31T00:00:00Z",
		"assigned_to": ["pkg-1", "pkg-2"],
		"assignment_errors": {"pkg-3": "format not supported"}
	}`

	var got CreativeResult
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	assert.Equal(t, "cr-1", got.CreativeID)
	assert.Equal(t, "rev-7", got.RevisionID)
	require.NotNil(t, got.Account)
	assert.Equal(t, "acct-1", got.Account.AccountID)
	assert.Equal(t, "updated", got.Action)
	assert.Equal(t, "approved", got.Status)
	assert.Equal(t, "gam-123", got.PlatformID)
	assert.Equal(t, []string{"name", "format_id"}, got.Changes)
	assert.Equal(t, []string{"low resolution asset"}, got.Warnings)
	assert.Equal(t, "https://example.com/preview/cr-1", got.PreviewURL)
	assert.Equal(t, "2026-12-31T00:00:00Z", got.ExpiresAt)
	assert.Equal(t, []string{"pkg-1", "pkg-2"}, got.AssignedTo)
	assert.Equal(t, map[string]string{"pkg-3": "format not supported"}, got.AssignmentErrors)

	// Re-marshal and confirm every wire name round-trips.
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, key := range []string{
		"creative_id", "revision_id", "account", "action", "status", "platform_id",
		"changes", "warnings", "preview_url", "expires_at",
		"assigned_to", "assignment_errors",
	} {
		assert.Contains(t, m, key, "wire field %q missing after round-trip", key)
	}

	// Legacy rejection_reason still parses (deprecated, not removed).
	var legacy CreativeResult
	require.NoError(t, json.Unmarshal(
		[]byte(`{"creative_id":"cr-9","action":"failed","rejection_reason":"bad asset"}`),
		&legacy,
	))
	assert.Equal(t, "bad asset", legacy.RejectionReason)

	// Deprecated field is omitted when empty.
	plain, err := json.Marshal(CreativeResult{CreativeID: "cr-2", Action: "created"})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "rejection_reason")
}

// TestCreativeResultLocalizationRoundTrip verifies a localized creative
// result (schema: localization is required on the item whenever the request
// carried a non-null localization object) survives decode-then-encode
// unchanged, comparing the whole JSON document.
func TestCreativeResultLocalizationRoundTrip(t *testing.T) {
	const payload = `{"creative_id":"summer_image_localized","action":"created","status":"pending_review","localization":{"default_locale_variant_id":"loc_en_us","unmatched_locale_action":"do_not_serve","locale_matching":"rfc4647_lookup","variants":[{"locale_variant_id":"loc_en_us","locale":"en-US","role":"source","assets":{"image":{"asset_type":"image","url":"https://cdn.example.com/summer-en.jpg","width":1080,"height":1080}}}]}}`

	var got CreativeResult
	require.NoError(t, json.Unmarshal([]byte(payload), &got))
	require.NotNil(t, got.Localization, "localization must not be dropped on decode")

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var wantMap, gotMap map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &wantMap))
	require.NoError(t, json.Unmarshal(raw, &gotMap))
	assert.Equal(t, wantMap, gotMap, "localized CreativeResult must round-trip unchanged")
}
