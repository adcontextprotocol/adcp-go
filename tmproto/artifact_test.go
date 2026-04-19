package tmproto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactRef_AllTypes_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		refType ArtifactRefType
		value   string
	}{
		{"url", ArtifactRefTypeURL, "https://example.com/article/123"},
		{"url_hash", ArtifactRefTypeURLHash, "bXlfaGFzaA=="},
		{"eidr", ArtifactRefTypeEIDR, "10.5240/7791-8534-2C23-9030-8107-4"},
		{"gracenote", ArtifactRefTypeGracenote, "SH032541890000"},
		{"isrc", ArtifactRefTypeISRC, "USRC17607839"},
		{"gtin", ArtifactRefTypeGTIN, "00012345678905"},
		{"rss_guid", ArtifactRefTypeRSSGUID, "episode-abc-123"},
		{"isbn", ArtifactRefTypeISBN, "978-0-123456-78-9"},
		{"custom", ArtifactRefTypeCustom, "publisher:post:42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := ArtifactRef{Type: tc.refType, Value: tc.value}
			data, err := json.Marshal(ref)
			require.NoError(t, err)
			assert.Contains(t, string(data), `"type":"`+string(tc.refType)+`"`)
			assert.Contains(t, string(data), `"value":"`+tc.value+`"`)

			var got ArtifactRef
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, tc.refType, got.Type)
			assert.Equal(t, tc.value, got.Value)
		})
	}
}

func TestArtifact_AssetUnion_RoundTrip(t *testing.T) {
	art := &Artifact{
		PropertyRID: "01890000-0000-7000-8000-000000000001",
		ArtifactID:  "article_42",
		VariantID:   "en",
		URL:         "https://example.com/article/42",
		Assets: Assets{
			&TextAsset{Role: "title", Content: "How to Make Pasta", ContentFormat: "text/plain", Language: "en"},
			&TextAsset{Role: "heading", Content: "Ingredients", HeadingLevel: 2},
			&ImageAsset{URL: "https://cdn.example.com/pasta.jpg", AltText: "fresh pasta", Width: 1200, Height: 800},
			&VideoAsset{URL: "https://cdn.example.com/pasta.mp4", DurationMs: 120000, Transcript: "First, boil water...", TranscriptSource: "closed_captions"},
			&AudioAsset{URL: "https://cdn.example.com/pasta.mp3", DurationMs: 60000, Transcript: "Welcome to the podcast"},
		},
	}

	data, err := json.Marshal(art)
	require.NoError(t, err)

	var got Artifact
	require.NoError(t, json.Unmarshal(data, &got))

	require.Len(t, got.Assets, 5)

	text, ok := got.Assets[0].(*TextAsset)
	require.True(t, ok, "asset[0] should be *TextAsset")
	assert.Equal(t, "title", text.Role)
	assert.Equal(t, "How to Make Pasta", text.Content)

	heading, ok := got.Assets[1].(*TextAsset)
	require.True(t, ok)
	assert.Equal(t, 2, heading.HeadingLevel)

	img, ok := got.Assets[2].(*ImageAsset)
	require.True(t, ok, "asset[2] should be *ImageAsset")
	assert.Equal(t, 1200, img.Width)

	vid, ok := got.Assets[3].(*VideoAsset)
	require.True(t, ok, "asset[3] should be *VideoAsset")
	assert.Equal(t, 120000, vid.DurationMs)
	assert.Equal(t, "closed_captions", vid.TranscriptSource)

	aud, ok := got.Assets[4].(*AudioAsset)
	require.True(t, ok, "asset[4] should be *AudioAsset")
	assert.Equal(t, 60000, aud.DurationMs)
}

func TestArtifact_UnknownAssetType_Passthrough(t *testing.T) {
	// Forward-compat: unknown asset types decode to UnknownAsset and re-marshal
	// as the original bytes. Lets older SDKs pass through newer publisher payloads.
	raw := `{"property_rid":"p","artifact_id":"a","assets":[{"type":"hologram","content":"???","extra":42}]}`
	var got Artifact
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	require.Len(t, got.Assets, 1)

	u, ok := got.Assets[0].(*UnknownAsset)
	require.True(t, ok, "asset[0] should be *UnknownAsset")
	assert.Equal(t, AssetType("hologram"), u.Type)

	// Re-marshal preserves the original bytes verbatim.
	data, err := json.Marshal(&got)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"hologram"`)
	assert.Contains(t, string(data), `"extra":42`, "unknown fields preserved on re-marshal")
}

func TestArtifact_MissingAssetType_Error(t *testing.T) {
	raw := `{
		"property_rid": "p", "artifact_id": "a",
		"assets": [{"content": "no type field"}]
	}`
	var got Artifact
	err := json.Unmarshal([]byte(raw), &got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing type discriminator")
}

func TestAssetAccess_AllMethods_RoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		access AssetAccess
	}{
		{"bearer_token", AssetAccess{Method: AssetAccessMethodBearerToken, Token: "ya29.xxx"}},
		{"service_account_gcp", AssetAccess{Method: AssetAccessMethodServiceAccount, Provider: "gcp", Credentials: map[string]any{"client_email": "sa@project.iam.gserviceaccount.com"}}},
		{"signed_url", AssetAccess{Method: AssetAccessMethodSignedURL}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.access)
			require.NoError(t, err)

			var got AssetAccess
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, tc.access.Method, got.Method)
			assert.Equal(t, tc.access.Token, got.Token)
			assert.Equal(t, tc.access.Provider, got.Provider)
		})
	}
}

func TestContextMatchRequest_FullDisclosureLadder(t *testing.T) {
	// Exercise all three disclosure rungs together — artifact (high),
	// artifact_refs (public), context_signals (classifier-only). The schema
	// allows combining them.
	req := &ContextMatchRequest{
		Type:         "context_match_request",
		RequestID:    "ctx-ladder-001",
		PropertyRID:  "01890000-0000-7000-8000-000000000001",
		PropertyType: PropertyTypeWebsite,
		PlacementID:  "article-hero",
		Artifact: &Artifact{
			PropertyRID: "01890000-0000-7000-8000-000000000001",
			ArtifactID:  "article_42",
			Assets: Assets{
				&TextAsset{Role: "title", Content: "Pasta 101"},
			},
		},
		ArtifactRefs: []ArtifactRef{
			{Type: ArtifactRefTypeURL, Value: "https://example.com/article/42"},
		},
		ContextSignals: &ContextSignals{
			Topics:     []string{"632"},
			TaxonomyID: 7,
			Summary:    "Article about making pasta",
		},
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)
	s := string(data)

	// Wire format: all three rungs present with the expected field names.
	for _, want := range []string{`"artifact":`, `"artifact_refs":`, `"context_signals":`} {
		assert.Contains(t, s, want, "missing field %s", want)
	}

	var got ContextMatchRequest
	require.NoError(t, json.Unmarshal(data, &got))

	require.NotNil(t, got.Artifact)
	assert.Equal(t, "article_42", got.Artifact.ArtifactID)
	require.Len(t, got.Artifact.Assets, 1)
	text, ok := got.Artifact.Assets[0].(*TextAsset)
	require.True(t, ok)
	assert.Equal(t, "Pasta 101", text.Content)

	require.Len(t, got.ArtifactRefs, 1)
	assert.Equal(t, ArtifactRefTypeURL, got.ArtifactRefs[0].Type)

	require.NotNil(t, got.ContextSignals)
	assert.Equal(t, []string{"632"}, got.ContextSignals.Topics)
}

func TestArtifact_ExtensibleMetadata_RoundTrip(t *testing.T) {
	// Metadata/Identifiers use additionalProperties: true in the schema —
	// arbitrary platform-specific fields must round-trip without loss.
	art := &Artifact{
		PropertyRID: "p",
		ArtifactID:  "a",
		Assets:      Assets{&TextAsset{Content: "hi"}},
		Metadata: map[string]any{
			"canonical":          "https://example.com/a",
			"author":             "Alice",
			"publisher_specific": "platform-only-value",
			"open_graph":         map[string]any{"og:title": "Hi"},
		},
		Identifiers: map[string]any{
			"apple_podcast_id": "1234567890",
			"custom_tenant_id": "tenant-abc",
		},
	}

	data, err := json.Marshal(art)
	require.NoError(t, err)
	assert.Contains(t, string(data), "publisher_specific", "additional metadata must be preserved")
	assert.Contains(t, string(data), "custom_tenant_id", "additional identifiers must be preserved")

	var got Artifact
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "platform-only-value", got.Metadata["publisher_specific"])
	assert.Equal(t, "tenant-abc", got.Identifiers["custom_tenant_id"])
}

// Ensures AssetType constants match the schema's const values exactly.
// These are the over-the-wire discriminator values — silent renames break interop.
func TestAssetType_WireValues(t *testing.T) {
	cases := map[AssetType]string{
		AssetTypeText:  "text",
		AssetTypeImage: "image",
		AssetTypeVideo: "video",
		AssetTypeAudio: "audio",
	}
	for constant, wire := range cases {
		assert.Equal(t, wire, string(constant))
	}
}

// Sanity: the Go identifiers for ArtifactRefType constants must serialize to
// the lowercase schema values, not the Go names.
func TestArtifactRefType_WireValues(t *testing.T) {
	data, err := json.Marshal(ArtifactRefTypeURLHash)
	require.NoError(t, err)
	assert.Equal(t, `"url_hash"`, strings.TrimSpace(string(data)))
}
