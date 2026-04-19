package tmproto

import (
	"fmt"
	"regexp"
)

// Schema-driven limits used by the Validate methods below. These mirror the
// JSON Schema constraints in adcp/schemas/tmp/context-match-request.json and
// adcp/schemas/content-standards/artifact.json. Named constants so callers can
// mirror them in their own pre-checks.
const (
	// ContextSignals.
	MaxTopics          = 50
	MaxKeywords        = 50
	MaxKeywordLength   = 100
	MaxContentPolicies = 20
	MaxSummaryLength   = 500
	MinEmbeddingDims   = 64
	MaxEmbeddingDims   = 2048

	// ArtifactRef.
	MaxArtifactRefs = 20

	// Artifact.
	MaxTextContentLength = 100000
	MaxTranscriptLength  = 200000
	MinHeadingLevel      = 1
	MaxHeadingLevel      = 6
)

var (
	bcp47TwoLetter = regexp.MustCompile(`^[a-z]{2}$`)
	validSentiment = map[string]struct{}{
		"positive": {}, "negative": {}, "neutral": {}, "mixed": {},
	}
	validArtifactRefType = map[ArtifactRefType]struct{}{
		ArtifactRefTypeURL: {}, ArtifactRefTypeURLHash: {}, ArtifactRefTypeEIDR: {},
		ArtifactRefTypeGracenote: {}, ArtifactRefTypeISRC: {}, ArtifactRefTypeGTIN: {},
		ArtifactRefTypeRSSGUID: {}, ArtifactRefTypeISBN: {}, ArtifactRefTypeCustom: {},
	}
	validAssetAccessMethod = map[AssetAccessMethod]struct{}{
		AssetAccessMethodBearerToken: {}, AssetAccessMethodServiceAccount: {}, AssetAccessMethodSignedURL: {},
	}
)

// Validate checks field-level and cross-field schema constraints on a
// ContextSignals. Returns the first violation found. Callers running an
// untrusted payload through the SDK SHOULD call Validate before using it —
// schema-invalid data is not rejected at unmarshal time.
func (c *ContextSignals) Validate() error {
	if c == nil {
		return nil
	}
	if len(c.Topics) > MaxTopics {
		return fmt.Errorf("context_signals.topics: %d items exceeds max %d", len(c.Topics), MaxTopics)
	}
	if len(c.Keywords) > MaxKeywords {
		return fmt.Errorf("context_signals.keywords: %d items exceeds max %d", len(c.Keywords), MaxKeywords)
	}
	for i, k := range c.Keywords {
		if len(k) > MaxKeywordLength {
			return fmt.Errorf("context_signals.keywords[%d]: %d chars exceeds max %d", i, len(k), MaxKeywordLength)
		}
	}
	if len(c.ContentPolicies) > MaxContentPolicies {
		return fmt.Errorf("context_signals.content_policies: %d items exceeds max %d", len(c.ContentPolicies), MaxContentPolicies)
	}
	if len(c.Summary) > MaxSummaryLength {
		return fmt.Errorf("context_signals.summary: %d chars exceeds max %d", len(c.Summary), MaxSummaryLength)
	}
	if c.Language != "" && !bcp47TwoLetter.MatchString(c.Language) {
		return fmt.Errorf("context_signals.language: %q does not match pattern ^[a-z]{2}$", c.Language)
	}
	if c.Sentiment != "" {
		if _, ok := validSentiment[c.Sentiment]; !ok {
			return fmt.Errorf("context_signals.sentiment: %q not in [positive, negative, neutral, mixed]", c.Sentiment)
		}
	}
	// Embedding triad: all three must be set together.
	if c.Embedding != "" || c.EmbeddingModel != "" || c.EmbeddingDims != 0 {
		if c.Embedding == "" || c.EmbeddingModel == "" || c.EmbeddingDims == 0 {
			return fmt.Errorf("context_signals: embedding, embedding_model, embedding_dims must all be set together")
		}
		if c.EmbeddingDims < MinEmbeddingDims || c.EmbeddingDims > MaxEmbeddingDims {
			return fmt.Errorf("context_signals.embedding_dims: %d outside [%d, %d]", c.EmbeddingDims, MinEmbeddingDims, MaxEmbeddingDims)
		}
	}
	return nil
}

// Validate checks field-level constraints on an ArtifactRef.
func (r *ArtifactRef) Validate() error {
	if r == nil {
		return nil
	}
	if _, ok := validArtifactRefType[r.Type]; !ok {
		return fmt.Errorf("artifact_ref.type: %q not a known ArtifactRefType", r.Type)
	}
	if r.Value == "" {
		return fmt.Errorf("artifact_ref.value: required")
	}
	return nil
}

// Validate checks field-level and cross-field constraints on an AssetAccess.
// Returns an error if the method is unknown, missing, or if required fields
// for the method are absent.
func (a *AssetAccess) Validate() error {
	if a == nil {
		return nil
	}
	if a.Method == "" {
		return fmt.Errorf("asset_access.method: required")
	}
	if _, ok := validAssetAccessMethod[a.Method]; !ok {
		return fmt.Errorf("asset_access.method: %q not a known AssetAccessMethod", a.Method)
	}
	switch a.Method {
	case AssetAccessMethodBearerToken:
		if a.Token == "" {
			return fmt.Errorf("asset_access: token required for method=bearer_token")
		}
	case AssetAccessMethodServiceAccount:
		if a.Provider == "" {
			return fmt.Errorf("asset_access: provider required for method=service_account")
		}
		if a.Provider != "gcp" && a.Provider != "aws" {
			return fmt.Errorf("asset_access.provider: %q not in [gcp, aws]", a.Provider)
		}
	}
	return nil
}

// Validate checks TextAsset field-level constraints.
func (a *TextAsset) Validate() error {
	if a == nil {
		return nil
	}
	if a.Content == "" {
		return fmt.Errorf("text_asset.content: required")
	}
	if len(a.Content) > MaxTextContentLength {
		return fmt.Errorf("text_asset.content: %d chars exceeds max %d", len(a.Content), MaxTextContentLength)
	}
	if a.HeadingLevel != 0 && (a.HeadingLevel < MinHeadingLevel || a.HeadingLevel > MaxHeadingLevel) {
		return fmt.Errorf("text_asset.heading_level: %d outside [%d, %d]", a.HeadingLevel, MinHeadingLevel, MaxHeadingLevel)
	}
	return nil
}

// Validate checks ImageAsset field-level constraints.
func (a *ImageAsset) Validate() error {
	if a == nil {
		return nil
	}
	if a.URL == "" {
		return fmt.Errorf("image_asset.url: required")
	}
	return a.Access.Validate()
}

// Validate checks VideoAsset field-level constraints.
func (a *VideoAsset) Validate() error {
	if a == nil {
		return nil
	}
	if a.URL == "" {
		return fmt.Errorf("video_asset.url: required")
	}
	if len(a.Transcript) > MaxTranscriptLength {
		return fmt.Errorf("video_asset.transcript: %d chars exceeds max %d", len(a.Transcript), MaxTranscriptLength)
	}
	return a.Access.Validate()
}

// Validate checks AudioAsset field-level constraints.
func (a *AudioAsset) Validate() error {
	if a == nil {
		return nil
	}
	if a.URL == "" {
		return fmt.Errorf("audio_asset.url: required")
	}
	if len(a.Transcript) > MaxTranscriptLength {
		return fmt.Errorf("audio_asset.transcript: %d chars exceeds max %d", len(a.Transcript), MaxTranscriptLength)
	}
	return a.Access.Validate()
}

// Validate checks Artifact-level constraints and recurses into each asset.
// Unknown assets (from forward-compat passthrough) are skipped.
func (a *Artifact) Validate() error {
	if a == nil {
		return nil
	}
	if a.PropertyRID == "" {
		return fmt.Errorf("artifact.property_rid: required")
	}
	if a.ArtifactID == "" {
		return fmt.Errorf("artifact.artifact_id: required")
	}
	if len(a.Assets) > MaxAssets {
		return fmt.Errorf("artifact.assets: %d items exceeds max %d", len(a.Assets), MaxAssets)
	}
	for i, asset := range a.Assets {
		if v, ok := asset.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("artifact.assets[%d]: %w", i, err)
			}
		}
	}
	return nil
}
