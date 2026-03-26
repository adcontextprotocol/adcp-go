package bench

// TMP types — minimal for benchmarking. Matches the JSON schemas.

type TMPContextRequest struct {
	ProtocolVersion string         `json:"protocol_version,omitempty"`
	RequestID       string         `json:"request_id"`
	PropertyID      string         `json:"property_id"`
	PropertyType    string         `json:"property_type"`
	PlacementID     string         `json:"placement_id"`
	Artifacts       []string       `json:"artifacts,omitempty"`
	AvailablePkgs   []TMPPackage   `json:"available_packages"`
}

type TMPPackage struct {
	PackageID  string   `json:"package_id"`
	MediaBuyID string   `json:"media_buy_id"`
	FormatIDs  []string `json:"format_ids,omitempty"`
}

type TMPContextResponse struct {
	RequestID string      `json:"request_id"`
	Offers    []TMPOffer  `json:"offers"`
	Signals   *TMPSignals `json:"signals,omitempty"`
}

type TMPOffer struct {
	PackageID string            `json:"package_id"`
	Summary   string            `json:"summary,omitempty"`
	Macros    map[string]string `json:"macros,omitempty"`
}

type TMPSignals struct {
	Segments    []string    `json:"segments,omitempty"`
	TargetingKVs []TMPKV   `json:"targeting_kvs,omitempty"`
}

type TMPKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type TMPIdentityRequest struct {
	ProtocolVersion string   `json:"protocol_version,omitempty"`
	RequestID       string   `json:"request_id"`
	UserToken       string   `json:"user_token"`
	UIDType         string   `json:"uid_type,omitempty"`
	PackageIDs      []string `json:"package_ids"`
}

type TMPIdentityResponse struct {
	RequestID   string            `json:"request_id"`
	Eligibility []TMPEligibility  `json:"eligibility"`
}

type TMPEligibility struct {
	PackageID   string   `json:"package_id"`
	Eligible    bool     `json:"eligible"`
	IntentScore *float64 `json:"intent_score,omitempty"`
}
