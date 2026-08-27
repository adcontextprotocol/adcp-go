package adcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Request types for refine_proposals ---

// RefineProposalsRequest is the input for the refine_proposals tool.
type RefineProposalsRequest struct {
	IdempotencyKey    string               `json:"idempotency_key"`
	Refinements       []ProposalRefinement `json:"refinements"`
	ContextID         string               `json:"context_id,omitempty"`
	Context           any                  `json:"context,omitempty"`
	GovernanceContext string               `json:"governance_context,omitempty"`
	AdcpVersion       string               `json:"adcp_version,omitempty"`
	AdcpMajorVersion  int                  `json:"adcp_major_version,omitempty"`
}

// ProposalRefinement is a single refinement entry (revise or finalize).
type ProposalRefinement struct {
	ProposalID     string                `json:"proposal_id"`
	Action         string                `json:"action,omitempty"`
	ChangeKind     string                `json:"change_kind,omitempty"`
	Ask            string                `json:"ask,omitempty"`
	Criteria       json.RawMessage       `json:"criteria,omitempty"`
	Constraints    *RefinementConstraints `json:"constraints,omitempty"`
	ProductChanges map[string]string      `json:"product_changes,omitempty"`
	Alternatives   *AlternativesRequest  `json:"alternatives,omitempty"`
}

// RefinementConstraints holds typed hard requirements for a refinement.
type RefinementConstraints struct {
	TotalBudget *BudgetConstraint      `json:"total_budget,omitempty"`
	CPM         *CPMConstraint         `json:"cpm,omitempty"`
	Impressions *ImpressionsConstraint `json:"impressions,omitempty"`
	Flight      *FlightConstraint      `json:"flight,omitempty"`
}

// BudgetConstraint is inclusive budget bounds with currency.
type BudgetConstraint struct {
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Currency string   `json:"currency"`
}

// CPMConstraint is a CPM ceiling across all purchases.
type CPMConstraint struct {
	Max      float64 `json:"max"`
	Currency string  `json:"currency"`
}

// ImpressionsConstraint is a minimum summed impression volume.
type ImpressionsConstraint struct {
	Min float64 `json:"min"`
}

// FlightConstraint is flight-window timing bounds.
type FlightConstraint struct {
	StartNoLaterThan  string `json:"start_no_later_than,omitempty"`
	EndNoEarlierThan  string `json:"end_no_earlier_than,omitempty"`
}

// AlternativesRequest asks for multiple draft alternatives.
type AlternativesRequest struct {
	Count int `json:"count"`
}

// --- Response types ---

// RefineProposalsData is the response payload for refine_proposals.
type RefineProposalsData struct {
	Results  []RefinementResult `json:"results,omitempty"`
	Products json.RawMessage    `json:"products,omitempty"`
	Status   string             `json:"status,omitempty"`
	TaskID   string             `json:"task_id,omitempty"`
	Message  string             `json:"message,omitempty"`
	Replayed bool               `json:"replayed,omitempty"`
}

// RefinementResult is a single entry in the refine_proposals response.
// Discriminated on Outcome.
type RefinementResult struct {
	SourceProposalID       string          `json:"source_proposal_id"`
	Outcome                string          `json:"outcome"`
	Proposal               json.RawMessage `json:"proposal,omitempty"`
	Notes                  string          `json:"notes,omitempty"`
	Reason                 string          `json:"reason,omitempty"`
	UnsatisfiedConstraints []string        `json:"unsatisfied_constraints,omitempty"`
	Suggestions            []string        `json:"suggestions,omitempty"`
	TargetingResolution    json.RawMessage `json:"targeting_resolution,omitempty"`
}

// Outcome constants for RefinementResult.
const (
	OutcomeRevised   = "revised"
	OutcomePartial   = "partial"
	OutcomeFinalized = "finalized"
	OutcomeUnable    = "unable"
)

// RefinementCapability declares a seller's refinement capabilities.
type RefinementCapability struct {
	SupportedDimensions []string `json:"supported_dimensions,omitempty"`
	MaxBatchSize        int      `json:"max_batch_size,omitempty"`
	MaxAlternatives     int      `json:"max_alternatives,omitempty"`
	SupportsFinalize    bool     `json:"supports_finalize,omitempty"`
}

// --- Response builder ---

// RefineProposalsResponse builds a refine_proposals MCP response.
func RefineProposalsResponse(data *RefineProposalsData) (*mcp.CallToolResult, any, error) {
	if data == nil {
		return Errorf("INVALID_REQUEST", ErrorOptions{Message: "refine_proposals response is required"})
	}
	out := data
	if data.Status == "" {
		copy := *data
		copy.Status = "completed"
		out = &copy
	}
	return buildResult(fmt.Sprintf("Refined %d proposals", len(data.Results)), out), out, nil
}

// --- Digest utilities ---

// ComputeTermsDigest computes sha256:<base64url> of JCS-canonicalized commercial terms.
func ComputeTermsDigest(commercialTerms json.RawMessage) (string, error) {
	canonical, err := jcs.Transform(commercialTerms)
	if err != nil {
		return "", fmt.Errorf("JCS canonicalization: %w", err)
	}
	hash := sha256.Sum256(canonical)
	encoded := base64.RawURLEncoding.EncodeToString(hash[:])
	return "sha256:" + encoded, nil
}

// VerifyTermsDigest checks that digest matches the computed digest of commercialTerms.
func VerifyTermsDigest(digest string, commercialTerms json.RawMessage) bool {
	expected, err := ComputeTermsDigest(commercialTerms)
	if err != nil {
		return false
	}
	return digest == expected
}

// --- Successor utility ---

// StampSuccessor sets lineage fields on a mutable proposal map.
// Sets parent_proposal_id, proposal_status (draft), and recomputes terms_digest.
func StampSuccessor(proposal map[string]any, sourceProposalID string) error {
	proposal["parent_proposal_id"] = sourceProposalID
	if _, ok := proposal["proposal_status"]; !ok {
		proposal["proposal_status"] = "draft"
	}
	if terms, ok := proposal["commercial_terms"]; ok {
		termsJSON, err := json.Marshal(terms)
		if err != nil {
			return fmt.Errorf("marshal commercial_terms: %w", err)
		}
		digest, err := ComputeTermsDigest(termsJSON)
		if err != nil {
			return err
		}
		proposal["terms_digest"] = digest
	}
	return nil
}
