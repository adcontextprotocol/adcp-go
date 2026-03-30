# Spec: `adcp-go/tmp` SDK Updates

## Goal

Update the `tmp` package types to match the AdCP 3.0 TMP schema changes, and
add request builders, join logic, and KV mapping functions. This makes the
package ready for Prebid Server integration.

## Type updates needed

The current `types.go` needs to align with the latest TMP schemas from
adcp PR #1734:

### `ContextMatchRequest`

1. **`PropertyRID`**: Change from `uint64` to `string` (UUID v7 format). Make it
   the primary required field.
2. **`PropertyID`**: Make optional (for logging only).
3. **`Artifacts`**: Change from `[]string` to `[]Artifact` (typed objects).
4. **`URLHash`**: Remove (now an artifact type).
5. **`AvailablePkgs`**: Remove. Replace with optional `PackageIDs []string`.
   Providers use synced package metadata — not sent per request.
6. **`ContextSignals`**: Add as an optional field.

```go
type ContextMatchRequest struct {
    ProtocolVersion string          `json:"protocol_version,omitempty"`
    RequestID       string          `json:"request_id"`
    PropertyRID     string          `json:"property_rid"`           // UUID v7, required
    PropertyID      string          `json:"property_id,omitempty"`  // human-readable, optional
    PropertyType    PropertyType    `json:"property_type"`
    PlacementID     string          `json:"placement_id"`
    Artifacts       []Artifact      `json:"artifacts,omitempty"`
    Geo             *Geo            `json:"geo,omitempty"`
    ContextSignals  *ContextSignals `json:"context_signals,omitempty"`
    PackageIDs      []string        `json:"package_ids,omitempty"`
    Signature       string          `json:"signature,omitempty"`
}
```

### New types

```go
type Artifact struct {
    Type  ArtifactType `json:"type"`
    Value string       `json:"value"`
}

type ArtifactType string

const (
    ArtifactURL       ArtifactType = "url"
    ArtifactURLHash   ArtifactType = "url_hash"
    ArtifactEIDR      ArtifactType = "eidr"
    ArtifactGracenote ArtifactType = "gracenote"
    ArtifactISRC      ArtifactType = "isrc"
    ArtifactGTIN      ArtifactType = "gtin"
    ArtifactRSSGUID   ArtifactType = "rss_guid"
    ArtifactISBN      ArtifactType = "isbn"
    ArtifactCustom    ArtifactType = "custom"
)

type ContextSignals struct {
    Topics          []string `json:"topics,omitempty"`
    TaxonomySource  string   `json:"taxonomy_source,omitempty"`
    TaxonomyID      int      `json:"taxonomy_id,omitempty"`
    Sentiment       string   `json:"sentiment,omitempty"`
    Keywords        []string `json:"keywords,omitempty"`
    Language        string   `json:"language,omitempty"`
    ContentPolicies []string `json:"content_policies,omitempty"`
    Summary         string   `json:"summary,omitempty"`
    Embedding       string   `json:"embedding,omitempty"`
    EmbeddingModel  string   `json:"embedding_model,omitempty"`
    EmbeddingDims   int      `json:"embedding_dims,omitempty"`
}

type Consent struct {
    GDPR       *bool  `json:"gdpr,omitempty"`
    TCFConsent string `json:"tcf_consent,omitempty"`
    GPP        string `json:"gpp,omitempty"`
    USPrivacy  string `json:"us_privacy,omitempty"`
}
```

### `IdentityMatchRequest`

Add `Consent` field:

```go
type IdentityMatchRequest struct {
    ProtocolVersion string   `json:"protocol_version,omitempty"`
    RequestID       string   `json:"request_id"`
    UserToken       string   `json:"user_token"`
    UIDType         UIDType  `json:"uid_type,omitempty"`
    Consent         *Consent `json:"consent,omitempty"`
    PackageIDs      []string `json:"package_ids"`
}
```

### `AvailablePackage`

Update description — synced at media buy time, not sent per request:

```go
// AvailablePackage represents package metadata synced to providers at media buy
// creation time. Providers cache this data and use it when evaluating context
// match requests. Not sent per request.
type AvailablePackage struct {
    PackageID  string    `json:"package_id"`
    MediaBuyID string    `json:"media_buy_id"`
    FormatIDs  []any     `json:"format_ids,omitempty"` // format-id objects
    Catalogs   []Catalog `json:"catalogs,omitempty"`
}
```

## New functions

### File: `builders.go`

```go
// NewContextMatchRequest creates a ContextMatchRequest with a random request ID.
func NewContextMatchRequest(propertyRID, propertyType, placementID string) *ContextMatchRequest

// NewIdentityMatchRequest creates an IdentityMatchRequest with a random request ID.
func NewIdentityMatchRequest(userToken string, uidType UIDType, packageIDs []string) *IdentityMatchRequest
```

### File: `join.go`

```go
type JoinResult struct {
    Activated []ActivatedOffer
    Skipped   []SkippedOffer
    Signals   *Signals
}

type ActivatedOffer struct {
    Offer       Offer
    Eligibility PackageEligibility
}

type SkippedOffer struct {
    Offer  Offer
    Reason string // "not_eligible" or "no_eligibility_data"
}

// JoinResults intersects context match offers with identity match eligibility.
// Activated offers are sorted by intent_score descending.
func JoinResults(ctx *ContextMatchResponse, id *IdentityMatchResponse) *JoinResult

// ToTargetingKVs flattens a JoinResult to GAM-compatible key-values.
// Keys: adcp_pkg (activated package IDs), adcp_seg (segments), plus any
// targeting_kvs from signals.
func ToTargetingKVs(result *JoinResult) map[string][]string
```

### File: `client/client.go` (updates)

The existing `client/` package (if present) or new:

```go
type Config struct {
    Providers      []ProviderConfig
    DefaultTimeout time.Duration // default 50ms
}

type ProviderConfig struct {
    AgentURL      string
    Endpoint      string
    ContextMatch  bool
    IdentityMatch bool
    TimeoutMs     int
}

type Client struct { /* HTTP/2 connection pool, per-provider timeouts */ }

func New(cfg Config) *Client

// FanOutContext sends to all context_match providers in parallel, merges responses.
func (c *Client) FanOutContext(ctx context.Context, req *tmp.ContextMatchRequest) (*tmp.ContextMatchResponse, error)

// FanOutIdentity sends to all identity_match providers in parallel, merges responses.
func (c *Client) FanOutIdentity(ctx context.Context, req *tmp.IdentityMatchRequest) (*tmp.IdentityMatchResponse, error)
```

Merge rules:
- **Context**: Offers concatenated, segments concatenated, targeting_kvs concatenated
- **Identity**: Conservative merge — eligible only if ALL providers say eligible; max intent_score
- **Timeouts**: Per-provider. Slow providers skipped, not errored.

### File: `sign/sign.go`

```go
// Sign signs a context match request using Ed25519.
// Covers: property_rid, placement_id, sorted package_ids, daily epoch.
func Sign(req *tmp.ContextMatchRequest, key ed25519.PrivateKey) string

// Verify verifies a context match request signature.
func Verify(req *tmp.ContextMatchRequest, sig string, key ed25519.PublicKey) bool
```

## File structure (after changes)

```
tmp/
  types.go              # updated types
  types_test.go         # updated tests
  builders.go           # NewContextMatchRequest, NewIdentityMatchRequest
  builders_test.go
  join.go               # JoinResults, ToTargetingKVs
  join_test.go
  provider.go           # existing provider interface
  urlcanon.go           # existing URL canonicalization
  urlcanon_test.go
  wire.go               # existing wire format helpers
  client/
    client.go           # HTTP/2 fan-out client
    client_test.go
    merge.go            # response merging
    merge_test.go
  sign/
    sign.go             # Ed25519 signing
    sign_test.go
```

## Tests

- **Type round-trip**: Marshal/unmarshal Go structs against JSON schema fixtures
- **Builders**: Validate output has required fields, random request IDs
- **Join**: All eligible, none eligible, partial, missing eligibility, intent_score sorting
- **KVs**: Expected GAM key-value format
- **Client**: Mock HTTP servers, fan-out parallelism, timeout handling, merge correctness
- **Sign**: Known-answer test vectors for Ed25519 signatures

## Usage: Prebid Server module

```go
import (
    "github.com/adcontextprotocol/adcp-go/tmp"
    "github.com/adcontextprotocol/adcp-go/tmp/client"
)

// At startup
tmpc := client.New(client.Config{
    Providers: []client.ProviderConfig{
        {Endpoint: "https://scope3.example.com/tmp", ContextMatch: true, IdentityMatch: true},
    },
    DefaultTimeout: 50 * time.Millisecond,
})

// Per request
req := tmp.NewContextMatchRequest(propertyRID, "website", placementID)
req.Artifacts = []tmp.Artifact{{Type: tmp.ArtifactURL, Value: pageURL}}

res, _ := tmpc.FanOutContext(ctx, req)
result := tmp.JoinResults(res, identityRes)
kvs := tmp.ToTargetingKVs(result)
```
