package tmproto

// ArtifactRefType identifies the kind of public content identifier carried in
// an ArtifactRef. The rung on the disclosure ladder between full-content
// (Artifact) and classifier-only (ContextSignals): the publisher shares a
// public handle the buyer can resolve independently.
type ArtifactRefType string

const (
	// ArtifactRefTypeURL is the canonical content URL. MUST NOT contain
	// user-specific path segments, query parameters, or fragments.
	ArtifactRefTypeURL ArtifactRefType = "url"
	// ArtifactRefTypeURLHash is a Blake3 hash of the canonicalized URL,
	// base64-encoded. Canonicalization: strip scheme, strip www./m./amp.
	// prefixes, lowercase, strip trailing slash, strip query + fragment.
	ArtifactRefTypeURLHash   ArtifactRefType = "url_hash"
	ArtifactRefTypeEIDR      ArtifactRefType = "eidr"      // EIDR DOI (film/TV)
	ArtifactRefTypeGracenote ArtifactRefType = "gracenote" // Gracenote TMS ID
	ArtifactRefTypeISRC      ArtifactRefType = "isrc"      // music recordings
	ArtifactRefTypeGTIN      ArtifactRefType = "gtin"      // UPC/EAN/ISBN-13
	ArtifactRefTypeRSSGUID   ArtifactRefType = "rss_guid"  // podcast episode GUID
	ArtifactRefTypeISBN      ArtifactRefType = "isbn"
	ArtifactRefTypeCustom    ArtifactRefType = "custom"
)

// ArtifactRef is a public identifier for content adjacent to an ad opportunity.
// Each ref identifies content via a public scheme the buyer can resolve without
// private registry sync. Both fields are required by the schema.
type ArtifactRef struct {
	Type  ArtifactRefType `json:"type"`
	Value string          `json:"value"`
}
