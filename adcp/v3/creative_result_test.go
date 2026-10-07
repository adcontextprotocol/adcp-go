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
// The v3 package pins AdCP 3.2.0-rc.3, which also defines revision_id.
func TestCreativeResultSpecFieldsRoundTrip(t *testing.T) {
	// Wire-shaped payload per static/schemas/source/creative/sync-creatives-response.json.
	const payload = `{
		"creative_id": "cr-1",
		"revision_id": "rev-7",
		"account": {"account_id": "acct-1", "name": "Acme", "status": "active"},
		"action": "updated",
		"status": "active",
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
	assert.Equal(t, "active", got.Status)
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
