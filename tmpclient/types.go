package tmpclient

import (
	"fmt"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TMPError is a structured error from a TMP endpoint.
type TMPError struct {
	StatusCode int
	Code       tmproto.ErrorCode
	Message    string
	RequestID  string
}

func (e *TMPError) Error() string {
	return fmt.Sprintf("tmp %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// ActivateParams configures the full activation flow.
// Privacy is enforced by API design: context fields and identity fields
// are separated and cannot be mixed.
type ActivateParams struct {
	// Context match inputs.
	PropertyID   string
	PropertyType tmproto.PropertyType
	PlacementID  string
	Geo          map[string]any
	ArtifactRefs []tmproto.ArtifactRef
	PackageIDs   []string

	// Seller agent endpoint URL; required on both the context-match and
	// identity-match wire contracts. The provider resolves the active package
	// set it has synced for this seller against it.
	SellerAgentURL string

	// Identity match inputs.
	UserToken string
	UIDType        tmproto.UIDType
	Consent        map[string]any
	Country        string // ISO 3166-1 alpha-2 routing directive for identity match
}

// Activation is a package that passed both context and identity checks.
type Activation struct {
	PackageID  string
	MediaBuyID string
	Offer      tmproto.Offer
}

// ActivateResult is the joined output of parallel context + identity calls.
type ActivateResult struct {
	Activations []Activation
	Signals     map[string]any
	Tmpx        string // HPKE-encrypted exposure token
	Context     *tmproto.ContextMatchResponse
	Identity    *tmproto.IdentityMatchResponse
}
