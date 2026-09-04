package tmproto

import (
	"encoding/json"
	"fmt"
)

// MaxAssets bounds the number of elements decoded from an Artifact's assets
// array. Matches the schema's maxItems: 200. Exposed so callers can mirror it
// in their HTTP body-size limits if they want symmetric defense.
const MaxAssets = 200

// AssetType discriminates entries in Artifact.Assets.
type AssetType string

const (
	AssetTypeText  AssetType = "text"
	AssetTypeImage AssetType = "image"
	AssetTypeVideo AssetType = "video"
	AssetTypeAudio AssetType = "audio"
)

// Asset is one element of Artifact.Assets. Concrete implementations:
// *TextAsset, *ImageAsset, *VideoAsset, *AudioAsset, *UnknownAsset.
// The discriminator on the wire comes from the implementer's AssetTag method,
// never from a user-settable field.
type Asset interface {
	AssetTag() AssetType
}

// TextAsset is a text block (paragraph, heading, caption, etc.).
// Content is publisher-supplied and MUST be treated as untrusted input when
// passed to LLM-based evaluation.
type TextAsset struct {
	Role          string          `json:"role,omitempty"`
	Content       string          `json:"content"`
	ContentFormat string          `json:"content_format,omitempty"` // text/plain (default), text/markdown, text/html, application/json
	Language      string          `json:"language,omitempty"`       // BCP 47 language tag
	HeadingLevel  int             `json:"heading_level,omitempty"`  // only for role=heading
	Provenance    json.RawMessage `json:"provenance,omitempty"`
}

// AssetTag implements Asset.
func (*TextAsset) AssetTag() AssetType { return AssetTypeText }

// MarshalJSON writes the "type" discriminator from AssetTag so it can't be
// forged via a user-set struct field.
func (a *TextAsset) MarshalJSON() ([]byte, error) {
	type alias TextAsset
	return json.Marshal(struct {
		Type AssetType `json:"type"`
		*alias
	}{AssetTypeText, (*alias)(a)})
}

// ImageAsset is an image asset with its URL and optional display metadata.
//
// URL is publisher-supplied and MUST be validated with ValidateFetchableURL
// before a buyer agent fetches it — see the MUST-validate-before-fetch
// contract on the Artifact doc comment.
type ImageAsset struct {
	URL        string          `json:"url"`
	Access     *AssetAccess    `json:"access,omitempty"`
	AltText    string          `json:"alt_text,omitempty"`
	Caption    string          `json:"caption,omitempty"`
	Width      int             `json:"width,omitempty"`
	Height     int             `json:"height,omitempty"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
}

// AssetTag implements Asset.
func (*ImageAsset) AssetTag() AssetType { return AssetTypeImage }

// MarshalJSON writes the "type" discriminator from AssetTag.
func (a *ImageAsset) MarshalJSON() ([]byte, error) {
	type alias ImageAsset
	return json.Marshal(struct {
		Type AssetType `json:"type"`
		*alias
	}{AssetTypeImage, (*alias)(a)})
}

// VideoAsset is a video asset with its URL and optional transcript/metadata.
// Transcript is publisher-supplied and MUST be treated as untrusted input.
//
// URL and ThumbnailURL are publisher-supplied and MUST be validated with
// ValidateFetchableURL before a buyer agent fetches either — see the
// MUST-validate-before-fetch contract on the Artifact doc comment.
type VideoAsset struct {
	URL              string          `json:"url"`
	Access           *AssetAccess    `json:"access,omitempty"`
	DurationMs       int             `json:"duration_ms,omitempty"`
	Transcript       string          `json:"transcript,omitempty"`
	TranscriptFormat string          `json:"transcript_format,omitempty"` // text/plain (default), text/markdown, application/json
	TranscriptSource string          `json:"transcript_source,omitempty"` // original_script, subtitles, closed_captions, dub, generated
	ThumbnailURL     string          `json:"thumbnail_url,omitempty"`
	Provenance       json.RawMessage `json:"provenance,omitempty"`
}

// AssetTag implements Asset.
func (*VideoAsset) AssetTag() AssetType { return AssetTypeVideo }

// MarshalJSON writes the "type" discriminator from AssetTag.
func (a *VideoAsset) MarshalJSON() ([]byte, error) {
	type alias VideoAsset
	return json.Marshal(struct {
		Type AssetType `json:"type"`
		*alias
	}{AssetTypeVideo, (*alias)(a)})
}

// AudioAsset is an audio asset with its URL and optional transcript/metadata.
// Transcript is publisher-supplied and MUST be treated as untrusted input.
//
// URL is publisher-supplied and MUST be validated with ValidateFetchableURL
// before a buyer agent fetches it — see the MUST-validate-before-fetch
// contract on the Artifact doc comment.
type AudioAsset struct {
	URL              string          `json:"url"`
	Access           *AssetAccess    `json:"access,omitempty"`
	DurationMs       int             `json:"duration_ms,omitempty"`
	Transcript       string          `json:"transcript,omitempty"`
	TranscriptFormat string          `json:"transcript_format,omitempty"` // text/plain (default), text/markdown, application/json
	TranscriptSource string          `json:"transcript_source,omitempty"` // original_script, closed_captions, generated
	Provenance       json.RawMessage `json:"provenance,omitempty"`
}

// AssetTag implements Asset.
func (*AudioAsset) AssetTag() AssetType { return AssetTypeAudio }

// MarshalJSON writes the "type" discriminator from AssetTag.
func (a *AudioAsset) MarshalJSON() ([]byte, error) {
	type alias AudioAsset
	return json.Marshal(struct {
		Type AssetType `json:"type"`
		*alias
	}{AssetTypeAudio, (*alias)(a)})
}

// UnknownAsset preserves an asset entry whose type the SDK does not recognize,
// so older SDKs can round-trip newer publisher payloads. Emitted by
// Assets.UnmarshalJSON when the discriminator is non-empty but unknown.
type UnknownAsset struct {
	Type AssetType       // the unrecognized discriminator value
	Raw  json.RawMessage // the original bytes, re-emitted verbatim on marshal
}

// AssetTag implements Asset.
func (u *UnknownAsset) AssetTag() AssetType { return u.Type }

// MarshalJSON returns the original bytes unchanged so routers pass through
// unknown asset types without corrupting them.
func (u *UnknownAsset) MarshalJSON() ([]byte, error) {
	if len(u.Raw) == 0 {
		return nil, fmt.Errorf("UnknownAsset: Raw is empty")
	}
	return u.Raw, nil
}

// Assets is a discriminated-union slice backing Artifact.Assets.
// Marshaling is standard (each concrete type writes its own "type" field);
// unmarshaling dispatches on the "type" discriminator and falls back to
// UnknownAsset for forward compatibility.
type Assets []Asset

// UnmarshalJSON decodes each element by reading the "type" discriminator and
// routing to the matching concrete asset struct. Enforces MaxAssets to bound
// allocation. Returns an error on missing discriminators — passthrough would
// mask malformed payloads. Unknown discriminators fall back to UnknownAsset
// so forward-compatible payloads round-trip cleanly.
func (a *Assets) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) > MaxAssets {
		return fmt.Errorf("assets: %d exceeds MaxAssets (%d)", len(raw), MaxAssets)
	}
	out := make(Assets, len(raw))
	for i, r := range raw {
		var tag struct {
			Type AssetType `json:"type"`
		}
		if err := json.Unmarshal(r, &tag); err != nil {
			return fmt.Errorf("asset[%d]: read type discriminator: %w", i, err)
		}
		var asset Asset
		switch tag.Type {
		case AssetTypeText:
			v := &TextAsset{}
			if err := json.Unmarshal(r, v); err != nil {
				return fmt.Errorf("asset[%d] text: %w", i, err)
			}
			asset = v
		case AssetTypeImage:
			v := &ImageAsset{}
			if err := json.Unmarshal(r, v); err != nil {
				return fmt.Errorf("asset[%d] image: %w", i, err)
			}
			asset = v
		case AssetTypeVideo:
			v := &VideoAsset{}
			if err := json.Unmarshal(r, v); err != nil {
				return fmt.Errorf("asset[%d] video: %w", i, err)
			}
			asset = v
		case AssetTypeAudio:
			v := &AudioAsset{}
			if err := json.Unmarshal(r, v); err != nil {
				return fmt.Errorf("asset[%d] audio: %w", i, err)
			}
			asset = v
		case "":
			return fmt.Errorf("asset[%d]: missing type discriminator", i)
		default:
			// Forward-compat: keep the bytes so the asset survives passthrough.
			asset = &UnknownAsset{Type: tag.Type, Raw: append(json.RawMessage(nil), r...)}
		}
		out[i] = asset
	}
	*a = out
	return nil
}

// AssetAccessMethod identifies the credential scheme for a secured asset URL.
type AssetAccessMethod string

const (
	AssetAccessMethodBearerToken    AssetAccessMethod = "bearer_token"
	AssetAccessMethodServiceAccount AssetAccessMethod = "service_account"
	AssetAccessMethodSignedURL      AssetAccessMethod = "signed_url"
)

// AssetAccess carries authentication for accessing secured asset URLs.
//
// Sensitive: Token and Credentials hold secrets. String() and GoString() are
// overridden to redact so standard %v/%+v logging cannot leak them.
// MarshalJSON emits only the fields that belong to the current Method, so a
// stray Token set on a signed_url variant cannot reach the wire.
//
// Routers MUST strip this field (see Artifact.StripAccess) before fanning out
// a ContextMatchRequest to multiple buyer agents, per the AdCP spec.
//
// Prefer the NewBearerTokenAccess / NewServiceAccountAccess / NewSignedURLAccess
// constructors over literal struct construction.
type AssetAccess struct {
	Method AssetAccessMethod `json:"-"`

	// Token is emitted only when Method == bearer_token.
	Token string `json:"-"`

	// Provider and Credentials are emitted only when Method == service_account.
	// Provider is "gcp" or "aws".
	Provider    string         `json:"-"`
	Credentials map[string]any `json:"-"`
}

// NewBearerTokenAccess constructs an AssetAccess for a bearer token.
func NewBearerTokenAccess(token string) AssetAccess {
	return AssetAccess{Method: AssetAccessMethodBearerToken, Token: token}
}

// NewServiceAccountAccess constructs an AssetAccess for a cloud service
// account. Provider is "gcp" or "aws"; credentials shape is provider-specific.
func NewServiceAccountAccess(provider string, credentials map[string]any) AssetAccess {
	return AssetAccess{Method: AssetAccessMethodServiceAccount, Provider: provider, Credentials: credentials}
}

// NewSignedURLAccess constructs an AssetAccess for a signed URL — credentials
// are embedded in the URL itself, so no additional fields are carried.
func NewSignedURLAccess() AssetAccess {
	return AssetAccess{Method: AssetAccessMethodSignedURL}
}

// MarshalJSON emits only the fields that belong to the current Method.
// Fields set on the wrong variant (e.g., Token when Method=signed_url) are
// silently dropped — preferable to leaking credentials onto the wire.
func (a AssetAccess) MarshalJSON() ([]byte, error) {
	m := map[string]any{"method": a.Method}
	switch a.Method {
	case AssetAccessMethodBearerToken:
		if a.Token != "" {
			m["token"] = a.Token
		}
	case AssetAccessMethodServiceAccount:
		if a.Provider != "" {
			m["provider"] = a.Provider
		}
		if a.Credentials != nil {
			m["credentials"] = a.Credentials
		}
	case AssetAccessMethodSignedURL:
		// No extra fields: credentials embedded in URL.
	default:
		return nil, fmt.Errorf("AssetAccess: unknown method")
	}
	return json.Marshal(m)
}

// UnmarshalJSON decodes the method and only the fields appropriate for it.
// Fields belonging to other variants are ignored even if present on the wire.
func (a *AssetAccess) UnmarshalJSON(data []byte) error {
	var raw struct {
		Method      AssetAccessMethod `json:"method"`
		Token       string            `json:"token,omitempty"`
		Provider    string            `json:"provider,omitempty"`
		Credentials map[string]any    `json:"credentials,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Method {
	case AssetAccessMethodBearerToken:
		*a = AssetAccess{Method: raw.Method, Token: raw.Token}
	case AssetAccessMethodServiceAccount:
		*a = AssetAccess{Method: raw.Method, Provider: raw.Provider, Credentials: raw.Credentials}
	case AssetAccessMethodSignedURL:
		*a = AssetAccess{Method: raw.Method}
	case "":
		return fmt.Errorf("asset_access: missing method")
	default:
		return fmt.Errorf("asset_access: unknown method")
	}
	return nil
}

// String returns a redacted form. Tokens and credentials are never included so
// %v / %s logging cannot leak secrets.
func (a AssetAccess) String() string { return a.redacted() }

// GoString returns a redacted form. %+v / %#v use this path too.
func (a AssetAccess) GoString() string { return a.redacted() }

func (a AssetAccess) redacted() string {
	return fmt.Sprintf("AssetAccess{Method:%s,<redacted>}", a.Method)
}

// Artifact is content adjacent to an ad placement (article, podcast segment,
// video chapter, social post). An Artifact is a collection of assets plus
// metadata and signals. Used in ContextMatchRequest as the highest-disclosure
// rung of the TMP content ladder.
//
// Publishers MUST NOT include asset access credentials the buyer could use
// outside this request flow — for secured assets, use signed URLs with short
// expiry. Routers MUST call StripAccess before forwarding to multiple buyers.
//
// # URL fields MUST be validated before fetch (SSRF contract)
//
// URL (below) and every asset URL reachable through Assets — ImageAsset.URL,
// VideoAsset.URL, VideoAsset.ThumbnailURL, AudioAsset.URL — are
// publisher-supplied strings with no built-in transport-layer protection.
// A buyer agent (or any code embedding this SDK) that fetches one of these
// URLs during content evaluation is making a server-side request to an
// address chosen entirely by the request sender: without validation, a
// publisher (or an attacker impersonating one) can direct that fetch at
// cloud-metadata endpoints (`http://169.254.169.254/...`), loopback or
// RFC 1918 addresses, `file://`, or any other internal host — a classic SSRF
// primitive.
//
// Calling (Artifact).Validate() — and transitively each asset's Validate()
// — runs ValidateFetchableURL against every one of these fields and rejects
// disallowed schemes, embedded credentials, and known-private/reserved
// hosts. Callers MUST call Validate() (directly, or via
// ValidateContextRequest on the containing request) before trusting any URL
// field here, and MUST still layer a dial-time SSRF guard (DNS-rebind safe;
// see adcp/signing.NewSafeHTTPClient for this SDK's reference
// implementation) at the point where the fetch actually happens —
// ValidateFetchableURL is a synchronous, DNS-free pre-flight check, not a
// substitute for validating the address that gets dialed. See
// ValidateFetchableURL's doc comment for the full rule set and its
// provenance (ported from the AdCP webhook SSRF rules).
type Artifact struct {
	PropertyRID    string `json:"property_rid"`
	ArtifactID     string `json:"artifact_id"`
	VariantID      string `json:"variant_id,omitempty"`
	URL            string `json:"url,omitempty"`
	PublishedTime  string `json:"published_time,omitempty"`   // ISO 8601
	LastUpdateTime string `json:"last_update_time,omitempty"` // ISO 8601
	Assets         Assets `json:"assets"`

	// FormatID optionally references a format definition from the format registry.
	// Shape: /schemas/latest/core/format-id.json. Left as json.RawMessage until
	// the core types package is introduced.
	FormatID json.RawMessage `json:"format_id,omitempty"`

	// Provenance declares how the content was produced (AI involvement,
	// declaring party, etc.). Shape: /schemas/latest/core/provenance.json.
	// Left as json.RawMessage until the core types package is introduced.
	Provenance json.RawMessage `json:"provenance,omitempty"`

	// Metadata carries rich extracted metadata. Known keys: canonical, author,
	// keywords, open_graph, twitter_card, json_ld. Schema allows additional
	// properties — stored as an untyped map for lossless round-trip.
	Metadata map[string]any `json:"metadata,omitempty"`

	// Identifiers carries platform-specific handles. Known keys:
	// apple_podcast_id, spotify_collection_id, podcast_guid, youtube_video_id,
	// rss_url. Schema allows additional properties — untyped for lossless round-trip.
	Identifiers map[string]any `json:"identifiers,omitempty"`
}

// StripAccess zeros the Access field on every asset in the artifact. Routers
// MUST call this (or equivalent) before fanning out a ContextMatchRequest to
// multiple buyer agents, per the AdCP spec — otherwise credentials leak to
// every buyer.
//
// Safe to call on an Artifact whose assets have no Access set; it's a no-op.
func (a *Artifact) StripAccess() {
	if a == nil {
		return
	}
	for _, asset := range a.Assets {
		switch v := asset.(type) {
		case *ImageAsset:
			v.Access = nil
		case *VideoAsset:
			v.Access = nil
		case *AudioAsset:
			v.Access = nil
		}
	}
}
