package tmproto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #3: user-settable Type field is gone, discriminator comes from AssetTag ---

func TestTextAsset_CannotForgeTypeDiscriminator(t *testing.T) {
	// Marshaling must always emit "type":"text" regardless of any caller
	// gymnastics. No exported Type field exists to mis-set.
	data, err := json.Marshal(&TextAsset{Content: "hello"})
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"text"`)
	assert.Contains(t, string(data), `"content":"hello"`)
}

func TestAsset_DiscriminatorWireValues(t *testing.T) {
	// The AssetTag-driven discriminator must match the schema's const values.
	cases := []struct {
		asset Asset
		want  AssetType
	}{
		{&TextAsset{Content: "x"}, AssetTypeText},
		{&ImageAsset{URL: "https://x/i"}, AssetTypeImage},
		{&VideoAsset{URL: "https://x/v"}, AssetTypeVideo},
		{&AudioAsset{URL: "https://x/a"}, AssetTypeAudio},
	}
	for _, tc := range cases {
		t.Run(string(tc.want), func(t *testing.T) {
			data, err := json.Marshal(tc.asset)
			require.NoError(t, err)
			assert.Contains(t, string(data), fmt.Sprintf(`"type":"%s"`, tc.want))
			assert.Equal(t, tc.want, tc.asset.AssetTag())
		})
	}
}

// --- #1 + #2: AssetAccess redaction and variant isolation ---

func TestAssetAccess_Redacts_String(t *testing.T) {
	a := NewBearerTokenAccess("ya29.super-secret-token") // #nosec G101 — fake token exercising redaction
	assert.NotContains(t, a.String(), "super-secret")
	assert.NotContains(t, fmt.Sprintf("%v", a), "super-secret")
	assert.NotContains(t, fmt.Sprintf("%+v", a), "super-secret")
	assert.NotContains(t, fmt.Sprintf("%#v", a), "super-secret")
	assert.Contains(t, a.String(), "AssetAccess{Method:bearer_token")
}

func TestAssetAccess_Redacts_GCPServiceAccount(t *testing.T) {
	a := NewServiceAccountAccess("gcp", map[string]any{
		"client_email": "sa@example.iam.gserviceaccount.com",
		"private_key":  "-----BEGIN PRIVATE KEY-----\nAAAABBBBCCCC\n-----END PRIVATE KEY-----",
	})
	for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
		s := fmt.Sprintf(format, a)
		assert.NotContains(t, s, "AAAABBBBCCCC", "format %s leaked private_key", format)
		assert.NotContains(t, s, "sa@example", "format %s leaked client_email", format)
	}
}

func TestAssetAccess_Redacts_InsideLogging(t *testing.T) {
	// Belt and suspenders: typical calling site log like `log.Printf("req=%+v", req)`
	// with an ImageAsset carrying an AssetAccess. Must not leak the token.
	img := &ImageAsset{
		URL:    "https://example.com/i.jpg",
		Access: &AssetAccess{Method: AssetAccessMethodBearerToken, Token: "ya29.leak-check"}, // #nosec G101 — fake token exercising redaction inside parent struct
	}
	s := fmt.Sprintf("%+v", img)
	assert.NotContains(t, s, "leak-check")
}

func TestAssetAccess_WireFormat_BearerToken(t *testing.T) {
	a := NewBearerTokenAccess("tok")
	data, err := json.Marshal(a)
	require.NoError(t, err)
	assert.JSONEq(t, `{"method":"bearer_token","token":"tok"}`, string(data))
}

func TestAssetAccess_WireFormat_SignedURL_DropsStrayToken(t *testing.T) {
	// Caller mis-constructed: signed_url with a stray Token set. MarshalJSON
	// MUST drop fields that don't belong to the variant — otherwise credentials
	// leak into a response that shouldn't have any.
	a := AssetAccess{
		Method: AssetAccessMethodSignedURL,
		Token:  "stray-token-do-not-emit", // #nosec G101 — fake token; test asserts it does NOT reach the wire
	}
	data, err := json.Marshal(a)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "stray-token-do-not-emit")
	assert.JSONEq(t, `{"method":"signed_url"}`, string(data))
}

func TestAssetAccess_WireFormat_ServiceAccount_DropsStrayToken(t *testing.T) {
	a := AssetAccess{
		Method:   AssetAccessMethodServiceAccount,
		Provider: "gcp",
		Token:    "not-for-this-variant",
	}
	data, err := json.Marshal(a)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "not-for-this-variant")
	assert.Contains(t, string(data), `"provider":"gcp"`)
}

func TestAssetAccess_Unmarshal_RejectsUnknownMethod(t *testing.T) {
	var a AssetAccess
	err := json.Unmarshal([]byte(`{"method":"quantum_key","token":"x"}`), &a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown method")
	assert.NotContains(t, err.Error(), "quantum_key")
}

func TestAssetAccess_Marshal_RejectsUnknownMethodWithoutEcho(t *testing.T) {
	_, err := json.Marshal(AssetAccess{Method: "quantum_key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown method")
	assert.NotContains(t, err.Error(), "quantum_key")
}

func TestAssetAccess_Unmarshal_RejectsMissingMethod(t *testing.T) {
	var a AssetAccess
	err := json.Unmarshal([]byte(`{"token":"x"}`), &a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing method")
}

// --- #4: UnknownAsset forward-compat ---

func TestAssets_UnknownType_Passthrough(t *testing.T) {
	raw := `[{"type":"hologram","content":"???","extra":42},{"type":"text","content":"hi"}]`
	var assets Assets
	require.NoError(t, json.Unmarshal([]byte(raw), &assets))
	require.Len(t, assets, 2)

	u, ok := assets[0].(*UnknownAsset)
	require.True(t, ok)
	assert.Equal(t, AssetType("hologram"), u.Type)

	_, ok = assets[1].(*TextAsset)
	require.True(t, ok)
}

// --- #5: size caps ---

func TestAssets_Unmarshal_RejectsOverLimit(t *testing.T) {
	elems := make([]string, MaxAssets+1)
	for i := range elems {
		elems[i] = `{"type":"text","content":"x"}`
	}
	raw := "[" + strings.Join(elems, ",") + "]"
	var assets Assets
	err := json.Unmarshal([]byte(raw), &assets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds MaxAssets")
}

// --- #7: StripAccess helper ---

func TestArtifact_StripAccess_ZerosAllVariants(t *testing.T) {
	art := &Artifact{
		PropertyRID: "p", ArtifactID: "a",
		Assets: Assets{
			&TextAsset{Content: "t"}, // no access field
			&ImageAsset{URL: "https://x/i", Access: &AssetAccess{Method: AssetAccessMethodBearerToken, Token: "tok-i"}},
			&VideoAsset{URL: "https://x/v", Access: &AssetAccess{Method: AssetAccessMethodServiceAccount, Provider: "gcp"}},
			&AudioAsset{URL: "https://x/a", Access: &AssetAccess{Method: AssetAccessMethodSignedURL}},
		},
	}
	art.StripAccess()

	assert.Nil(t, art.Assets[1].(*ImageAsset).Access)
	assert.Nil(t, art.Assets[2].(*VideoAsset).Access)
	assert.Nil(t, art.Assets[3].(*AudioAsset).Access)

	// Post-strip wire format carries no access-related fields.
	data, err := json.Marshal(art)
	require.NoError(t, err)
	for _, forbid := range []string{`"access"`, `"token"`, `"bearer_token"`, "tok-i"} {
		assert.NotContains(t, string(data), forbid)
	}
}

func TestArtifact_StripAccess_NilSafe(t *testing.T) {
	var a *Artifact
	a.StripAccess() // no panic
}

// --- #6: Validate methods ---

func TestContextSignals_Validate(t *testing.T) {
	cases := []struct {
		name    string
		sig     ContextSignals
		wantErr string
		wantNot string
	}{
		{"empty", ContextSignals{}, "", ""},
		{"valid", ContextSignals{Sentiment: "neutral", Language: "en"}, "", ""},
		{"bad sentiment", ContextSignals{Sentiment: "postive"}, "sentiment", "postive"},
		{"bad language pattern", ContextSignals{Language: "EN"}, "language", "EN"},
		{"summary too long", ContextSignals{Summary: strings.Repeat("x", MaxSummaryLength+1)}, "summary", ""},
		{"too many topics", ContextSignals{Topics: make([]string, MaxTopics+1)}, "topics", ""},
		{"embedding without model", ContextSignals{Embedding: "x", EmbeddingDims: 256}, "set together", ""},
		{"embedding dims too small", ContextSignals{Embedding: "x", EmbeddingModel: "m", EmbeddingDims: 1}, "outside", ""},
		{"embedding dims too large", ContextSignals{Embedding: "x", EmbeddingModel: "m", EmbeddingDims: 9999}, "outside", ""},
		{"valid embedding", ContextSignals{Embedding: "x", EmbeddingModel: "m", EmbeddingDims: 256}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sig.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				if tc.wantNot != "" {
					assert.NotContains(t, err.Error(), tc.wantNot)
				}
			}
		})
	}
}

func TestArtifactRef_Validate(t *testing.T) {
	cases := []struct {
		name    string
		ref     ArtifactRef
		wantErr string
		wantNot string
	}{
		{"valid url", ArtifactRef{Type: ArtifactRefTypeURL, Value: "https://x"}, "", ""},
		{"missing value", ArtifactRef{Type: ArtifactRefTypeURL}, "value", ""},
		{"unknown type", ArtifactRef{Type: "starlink", Value: "x"}, "not a known ArtifactRefType", "starlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				if tc.wantNot != "" {
					assert.NotContains(t, err.Error(), tc.wantNot)
				}
			}
		})
	}
}

func TestAssetAccess_Validate(t *testing.T) {
	cases := []struct {
		name    string
		a       AssetAccess
		wantErr string
		wantNot string
	}{
		{"bearer ok", NewBearerTokenAccess("tok"), "", ""},
		{"bearer missing token", AssetAccess{Method: AssetAccessMethodBearerToken}, "token required", ""},
		{"sa ok gcp", NewServiceAccountAccess("gcp", nil), "", ""},
		{"sa ok aws", NewServiceAccountAccess("aws", nil), "", ""},
		{"sa missing provider", AssetAccess{Method: AssetAccessMethodServiceAccount}, "provider required", ""},
		{"sa bad provider", AssetAccess{Method: AssetAccessMethodServiceAccount, Provider: "azure"}, "provider", "azure"},
		{"signed ok", NewSignedURLAccess(), "", ""},
		{"missing method", AssetAccess{}, "method: required", ""},
		{"unknown method", AssetAccess{Method: "unknown"}, "not a known AssetAccessMethod", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				if tc.wantNot != "" {
					assert.NotContains(t, err.Error(), tc.wantNot)
				}
			}
		})
	}
}

func TestArtifact_Validate_RecursesIntoAssets(t *testing.T) {
	art := &Artifact{
		PropertyRID: "p", ArtifactID: "a",
		Assets: Assets{
			&TextAsset{Content: "hi", HeadingLevel: 99}, // out of [1,6]
		},
	}
	err := art.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "heading_level")
	assert.Contains(t, err.Error(), "assets[0]")
}

func TestTextAsset_Validate(t *testing.T) {
	assert.NoError(t, (&TextAsset{Content: "hi"}).Validate())
	assert.Error(t, (&TextAsset{}).Validate())
	assert.Error(t, (&TextAsset{Content: strings.Repeat("x", MaxTextContentLength+1)}).Validate())
}
