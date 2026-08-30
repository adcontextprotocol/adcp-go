package adcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
)

// Outcome constants for RefinementResult.
const (
	OutcomeRevised   = "revised"
	OutcomePartial   = "partial"
	OutcomeFinalized = "finalized"
	OutcomeUnable    = "unable"
)

const (
	// ProtocolMaxRefinements is the maximum number of proposal operations in one call.
	ProtocolMaxRefinements = 25
	// ProtocolMaxAlternatives is the maximum number of alternatives for one refinement.
	ProtocolMaxAlternatives = 10
)

// NewRefineProposalsRequest builds and validates a proposal refinement call
// against the seller capability advertised by get_adcp_capabilities.
func NewRefineProposalsRequest(idempotencyKey string, refinements []ProposalRefinement, capability *RefinementCapability) (*RefineProposalsRequest, error) {
	req := &RefineProposalsRequest{IdempotencyKey: idempotencyKey, Refinements: refinements}
	if err := ValidateRefineProposalsRequest(req, capability); err != nil {
		return nil, err
	}
	return req, nil
}

// ValidateRefineProposalsRequest rejects invalid or unsupported work before a
// seller mutates proposal state. Semantic ask text is always allowed; typed
// dimensions must have been declared in proposal_refinement capabilities.
func ValidateRefineProposalsRequest(req *RefineProposalsRequest, capability *RefinementCapability) error {
	if req == nil {
		return NewError("INVALID_REQUEST", ErrorOptions{Message: "refine_proposals request is required"})
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return NewError("MISSING_FIELD", ErrorOptions{Message: "idempotency_key is required", Field: "idempotency_key"})
	}
	if len(req.Refinements) == 0 || len(req.Refinements) > ProtocolMaxRefinements {
		return NewError("VALIDATION_ERROR", ErrorOptions{Message: "refinements must contain 1 to 25 entries", Field: "refinements"})
	}

	supported := map[string]bool{}
	if capability != nil {
		for _, dimension := range capability.SupportedDimensions {
			supported[dimension] = true
		}
	}
	seen := make(map[string]bool, len(req.Refinements))
	finalizeBatch := req.Refinements[0].Action == "finalize"
	for i := range req.Refinements {
		refinement := &req.Refinements[i]
		field := fmt.Sprintf("refinements[%d]", i)
		if strings.TrimSpace(refinement.ProposalID) == "" {
			return NewError("MISSING_FIELD", ErrorOptions{Message: "proposal_id is required", Field: field + ".proposal_id"})
		}
		if seen[refinement.ProposalID] {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "proposal_id must be unique within the batch", Field: field + ".proposal_id"})
		}
		seen[refinement.ProposalID] = true
		if refinement.Action != "revise" && refinement.Action != "finalize" {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "action must be revise or finalize", Field: field + ".action"})
		}
		if (refinement.Action == "finalize") != finalizeBatch {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "finalize operations cannot be mixed with revisions", Field: field + ".action"})
		}
		if refinement.Action == "finalize" {
			if refinement.ChangeKind != "" || refinement.Constraints != nil || len(refinement.ProductChanges) > 0 || refinement.Alternatives != nil || refinement.Ask != "" || refinement.Criteria != nil {
				return NewError("VALIDATION_ERROR", ErrorOptions{Message: "finalize cannot change proposal terms", Field: field})
			}
			continue
		}
		if refinement.ChangeKind != "" && refinement.ChangeKind != "amendment" && refinement.ChangeKind != "cancellation" {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "change_kind must be amendment or cancellation", Field: field + ".change_kind"})
		}
		if refinement.Constraints == nil && len(refinement.ProductChanges) == 0 && refinement.Alternatives == nil && strings.TrimSpace(refinement.Ask) == "" && refinement.Criteria == nil && refinement.ChangeKind != "cancellation" {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "revise requires at least one requested change", Field: field})
		}
		if err := validateRefinementValues(refinement, field); err != nil {
			return err
		}
		for _, dimension := range requestedDimensions(refinement) {
			if capability != nil && !supported[dimension] {
				return NewError("UNSUPPORTED_FEATURE", ErrorOptions{
					Message: "unsupported proposal refinement dimension", Field: field + "." + dimension,
					Details: map[string]any{"unsupported_dimension": dimension, "supported_dimensions": capability.SupportedDimensions},
				})
			}
		}
		if refinement.Alternatives != nil {
			limit := ProtocolMaxAlternatives
			if capability != nil && capability.MaxAlternatives > 0 && capability.MaxAlternatives < limit {
				limit = capability.MaxAlternatives
			}
			if refinement.Alternatives.Count < 2 || refinement.Alternatives.Count > limit {
				return NewError("VALIDATION_ERROR", ErrorOptions{Message: fmt.Sprintf("alternatives.count must be between 2 and %d", limit), Field: field + ".alternatives.count"})
			}
		}
	}
	return nil
}

func validateRefinementValues(refinement *ProposalRefinement, field string) error {
	if c := refinement.Constraints; c != nil {
		if c.TotalBudget == nil && c.CPM == nil && c.Impressions == nil && c.Flight == nil {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "constraints must not be empty", Field: field + ".constraints"})
		}
		if b := c.TotalBudget; b != nil {
			if len(b.Currency) != 3 || (b.Min == nil && b.Max == nil) || (b.Min != nil && *b.Min < 0) || (b.Max != nil && *b.Max < 0) || (b.Min != nil && b.Max != nil && *b.Min > *b.Max) {
				return NewError("VALIDATION_ERROR", ErrorOptions{Message: "invalid total_budget constraint", Field: field + ".constraints.total_budget"})
			}
		}
		if c.CPM != nil && (c.CPM.Max <= 0 || len(c.CPM.Currency) != 3) {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "invalid cpm constraint", Field: field + ".constraints.cpm"})
		}
		if c.Impressions != nil && c.Impressions.Min <= 0 {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "invalid impressions constraint", Field: field + ".constraints.impressions"})
		}
		if flight := c.Flight; flight != nil {
			if flight.StartNoLaterThan == "" && flight.EndNoEarlierThan == "" {
				return NewError("VALIDATION_ERROR", ErrorOptions{Message: "flight requires at least one bound", Field: field + ".constraints.flight"})
			}
			if flight.StartNoLaterThan != "" {
				if _, err := time.Parse(time.RFC3339, flight.StartNoLaterThan); err != nil {
					return NewError("VALIDATION_ERROR", ErrorOptions{Message: "invalid flight start bound", Field: field + ".constraints.flight.start_no_later_than"})
				}
			}
			if flight.EndNoEarlierThan != "" {
				if _, err := time.Parse(time.RFC3339, flight.EndNoEarlierThan); err != nil {
					return NewError("VALIDATION_ERROR", ErrorOptions{Message: "invalid flight end bound", Field: field + ".constraints.flight.end_no_earlier_than"})
				}
			}
		}
	}
	for productID, action := range refinement.ProductChanges {
		if strings.TrimSpace(productID) == "" || (action != "include" && action != "omit") {
			return NewError("VALIDATION_ERROR", ErrorOptions{Message: "product_changes values must be include or omit", Field: field + ".product_changes"})
		}
	}
	return nil
}

func requestedDimensions(refinement *ProposalRefinement) []string {
	dimensions := make([]string, 0, 7)
	if refinement.Constraints != nil {
		if refinement.Constraints.TotalBudget != nil {
			dimensions = append(dimensions, "total_budget")
		}
		if refinement.Constraints.CPM != nil {
			dimensions = append(dimensions, "cpm")
		}
		if refinement.Constraints.Impressions != nil {
			dimensions = append(dimensions, "impressions")
		}
		if refinement.Constraints.Flight != nil {
			dimensions = append(dimensions, "flight")
		}
	}
	if len(refinement.ProductChanges) > 0 {
		dimensions = append(dimensions, "product_changes")
	}
	if refinement.Alternatives != nil {
		dimensions = append(dimensions, "alternatives")
	}
	if refinement.Criteria != nil {
		dimensions = append(dimensions, "criteria")
	}
	return dimensions
}

// VerifyRefineProposalsResponse verifies ordering, outcome structure,
// immutable lineage, digests, alternatives, hard constraints, and hold expiry.
func VerifyRefineProposalsResponse(req *RefineProposalsRequest, data *RefineProposalsData, now time.Time) error {
	if req == nil || data == nil {
		return fmt.Errorf("request and response are required")
	}
	if len(req.Refinements) == 0 {
		return fmt.Errorf("request has no refinements")
	}
	if data.Status == "submitted" {
		if data.TaskID == "" {
			return fmt.Errorf("submitted response is missing task_id")
		}
		if len(data.Results) != 0 {
			return fmt.Errorf("submitted response cannot contain results")
		}
		return nil
	}
	if data.Status != "" && data.Status != "completed" {
		return fmt.Errorf("response has invalid status %q", data.Status)
	}
	if len(data.Results) != len(req.Refinements) {
		return fmt.Errorf("result count %d does not match refinement count %d", len(data.Results), len(req.Refinements))
	}
	finalizeBatch := len(req.Refinements) > 0 && req.Refinements[0].Action == "finalize"
	finalizedCount := 0
	for i := range data.Results {
		if data.Results[i].Outcome == OutcomeFinalized {
			finalizedCount++
		}
	}
	if finalizeBatch && finalizedCount > 0 && finalizedCount != len(data.Results) {
		return fmt.Errorf("finalize batch mixes committed and failed results; holds are not atomic")
	}
	seenProposalIDs := map[string]bool{}
	for i := range data.Results {
		result := &data.Results[i]
		refinement := &req.Refinements[i]
		if result.SourceProposalID != refinement.ProposalID {
			return fmt.Errorf("result %d source_proposal_id does not preserve request order", i)
		}
		requestedConstraints := map[string]bool{}
		if refinement.Constraints != nil {
			for _, dimension := range requestedDimensions(refinement) {
				if dimension == "total_budget" || dimension == "cpm" || dimension == "impressions" || dimension == "flight" {
					requestedConstraints[dimension] = true
				}
			}
		}
		for _, key := range result.UnsatisfiedConstraints {
			if !requestedConstraints[key] {
				return fmt.Errorf("result %d reports an unrequested unsatisfied constraint", i)
			}
		}
		for productID, action := range result.UnsatisfiedProductChanges {
			if refinement.ProductChanges[productID] != action {
				return fmt.Errorf("result %d reports an unrequested product change", i)
			}
		}
		if (len(result.UnsatisfiedConstraints) > 0 || len(result.UnsatisfiedProductChanges) > 0) && result.ReasonCode != "constraint_unsatisfiable" {
			return fmt.Errorf("result %d must use constraint_unsatisfiable precedence", i)
		}
		if finalizeBatch {
			if result.Outcome == OutcomeUnable {
				if result.Proposal != nil || len(result.Proposals) != 0 || result.ReasonCode == "" || result.Reason == "" {
					return fmt.Errorf("finalize failure result %d is structurally invalid", i)
				}
				continue
			}
			if result.Outcome != OutcomeFinalized || result.Proposal == nil || len(result.Proposals) != 0 || result.ReasonCode != "" || result.Reason != "" || len(result.UnsatisfiedConstraints) != 0 || len(result.UnsatisfiedProductChanges) != 0 {
				return fmt.Errorf("finalize batch result %d has an invalid outcome", i)
			}
			if err := verifySuccessor(*result.Proposal, refinement.ProposalID, "committed"); err != nil {
				return fmt.Errorf("result %d: %w", i, err)
			}
			if err := recordSuccessorID(result.Proposal.ProposalID, refinement.ProposalID, seenProposalIDs); err != nil {
				return fmt.Errorf("result %d: %w", i, err)
			}
			expiresAt, err := time.Parse(time.RFC3339, result.Proposal.ExpiresAt)
			if err != nil || !expiresAt.After(now) {
				return fmt.Errorf("result %d committed hold is expired or has invalid expires_at", i)
			}
			continue
		}
		if result.Outcome == OutcomeFinalized {
			return fmt.Errorf("revision result %d cannot be finalized", i)
		}
		if result.Outcome == OutcomeUnable {
			if result.Proposal != nil || len(result.Proposals) != 0 || result.ReasonCode == "" || result.Reason == "" {
				return fmt.Errorf("unable result %d is structurally invalid", i)
			}
			continue
		}
		if result.Outcome != OutcomeRevised && result.Outcome != OutcomePartial {
			return fmt.Errorf("result %d has unknown outcome", i)
		}
		if len(result.Proposals) == 0 {
			return fmt.Errorf("result %d has no successor proposals", i)
		}
		if result.Proposal != nil {
			return fmt.Errorf("revision result %d cannot contain singular proposal", i)
		}
		expected := 1
		if refinement.Alternatives != nil {
			expected = refinement.Alternatives.Count
		}
		if result.Outcome == OutcomeRevised && len(result.Proposals) != expected {
			return fmt.Errorf("revised result %d returned %d proposals, expected %d", i, len(result.Proposals), expected)
		}
		if result.Outcome == OutcomeRevised && (result.ReasonCode != "" || result.Reason != "" || len(result.UnsatisfiedConstraints) != 0 || len(result.UnsatisfiedProductChanges) != 0) {
			return fmt.Errorf("revised result %d cannot contain failure details", i)
		}
		if result.Outcome == OutcomePartial && (result.ReasonCode == "" || result.Reason == "") {
			return fmt.Errorf("partial result %d is missing reason", i)
		}
		if result.Outcome == OutcomePartial && len(result.Proposals) > expected {
			return fmt.Errorf("partial result %d returned more proposals than requested", i)
		}
		seenDigests := map[string]bool{}
		unsatisfied := map[string]bool{}
		for _, key := range result.UnsatisfiedConstraints {
			unsatisfied[key] = true
		}
		for j := range result.Proposals {
			proposal := &result.Proposals[j]
			if err := verifySuccessor(*proposal, refinement.ProposalID, "draft"); err != nil {
				return fmt.Errorf("result %d proposal %d: %w", i, j, err)
			}
			if err := recordSuccessorID(proposal.ProposalID, refinement.ProposalID, seenProposalIDs); err != nil {
				return fmt.Errorf("result %d proposal %d: %w", i, j, err)
			}
			if seenDigests[proposal.TermsDigest] {
				return fmt.Errorf("result %d contains duplicate commercial terms", i)
			}
			seenDigests[proposal.TermsDigest] = true
			if err := verifySatisfiedDimensions(refinement, proposal, unsatisfied, result.UnsatisfiedProductChanges); err != nil {
				return fmt.Errorf("result %d proposal %d: %w", i, j, err)
			}
		}
	}
	return nil
}

func recordSuccessorID(proposalID, sourceID string, seen map[string]bool) error {
	if strings.TrimSpace(proposalID) == "" {
		return fmt.Errorf("successor is missing proposal_id")
	}
	if proposalID == sourceID {
		return fmt.Errorf("successor must use a fresh proposal_id")
	}
	if seen[proposalID] {
		return fmt.Errorf("duplicate successor proposal_id %q", proposalID)
	}
	seen[proposalID] = true
	return nil
}

func verifySuccessor(proposal CanonicalProposal, sourceID, status string) error {
	if proposal.ParentProposalID != sourceID {
		return fmt.Errorf("parent_proposal_id does not match source proposal")
	}
	if string(proposal.ProposalStatus) != status {
		return fmt.Errorf("proposal_status must be %s", status)
	}
	if _, err := asObject(proposal.CommercialTerms); err != nil {
		return err
	}
	terms, err := json.Marshal(proposal.CommercialTerms)
	if err != nil || !VerifyTermsDigest(proposal.TermsDigest, terms) {
		return fmt.Errorf("terms_digest does not match commercial_terms")
	}
	return nil
}

func verifySatisfiedDimensions(refinement *ProposalRefinement, proposal *CanonicalProposal, unsatisfied map[string]bool, unsatisfiedProducts map[string]string) error {
	terms, err := asObject(proposal.CommercialTerms)
	if err != nil {
		return err
	}
	if c := refinement.Constraints; c != nil {
		if c.TotalBudget != nil && !unsatisfied["total_budget"] {
			budget, ok := objectField(terms, "total_budget")
			if !ok {
				return fmt.Errorf("total_budget constraint cannot be verified")
			}
			amount, amountOK := numberField(budget, "amount")
			currency, currencyOK := stringField(budget, "currency")
			if !amountOK || !currencyOK || currency != c.TotalBudget.Currency || (c.TotalBudget.Min != nil && amount < *c.TotalBudget.Min) || (c.TotalBudget.Max != nil && amount > *c.TotalBudget.Max) {
				return fmt.Errorf("total_budget constraint is not satisfied")
			}
		}
		purchases, _ := terms["purchases"].([]any)
		if c.CPM != nil && !unsatisfied["cpm"] {
			if len(purchases) == 0 {
				return fmt.Errorf("cpm constraint cannot be verified")
			}
			for _, raw := range purchases {
				purchase, _ := raw.(map[string]any)
				pricing, ok := objectField(purchase, "pricing")
				model, mok := stringField(pricing, "pricing_model")
				currency, cok := stringField(pricing, "currency")
				price, pok := numberField(pricing, "fixed_price")
				if !ok || !mok || !cok || !pok || (model != "cpm" && model != "vcpm") || currency != c.CPM.Currency || price > c.CPM.Max {
					return fmt.Errorf("cpm constraint is not satisfied")
				}
			}
		}
		if c.Impressions != nil && !unsatisfied["impressions"] {
			if len(purchases) == 0 {
				return fmt.Errorf("impressions constraint cannot be verified")
			}
			total := 0.0
			for _, raw := range purchases {
				purchase, _ := raw.(map[string]any)
				n, ok := numberField(purchase, "impressions")
				if !ok {
					return fmt.Errorf("impressions constraint cannot be verified")
				}
				total += n
			}
			if total < c.Impressions.Min {
				return fmt.Errorf("impressions constraint is not satisfied")
			}
		}
		if c.Flight != nil && !unsatisfied["flight"] {
			start, sok := stringField(terms, "start_time")
			end, eok := stringField(terms, "end_time")
			if c.Flight.StartNoLaterThan != "" {
				actual, aerr := time.Parse(time.RFC3339, start)
				bound, berr := time.Parse(time.RFC3339, c.Flight.StartNoLaterThan)
				if !sok || start == "asap" || aerr != nil || berr != nil || actual.After(bound) {
					return fmt.Errorf("flight start constraint is not satisfied")
				}
			}
			if c.Flight.EndNoEarlierThan != "" {
				actual, aerr := time.Parse(time.RFC3339, end)
				bound, berr := time.Parse(time.RFC3339, c.Flight.EndNoEarlierThan)
				if !eok || aerr != nil || berr != nil || actual.Before(bound) {
					return fmt.Errorf("flight end constraint is not satisfied")
				}
			}
		}
	}
	if len(refinement.ProductChanges) > 0 {
		purchases, _ := terms["purchases"].([]any)
		present := map[string]bool{}
		for _, raw := range purchases {
			purchase, _ := raw.(map[string]any)
			if id, ok := stringField(purchase, "product_id"); ok {
				present[id] = true
			}
		}
		for id, action := range refinement.ProductChanges {
			if unsatisfiedProducts[id] != "" {
				continue
			}
			if (action == "include" && !present[id]) || (action == "omit" && present[id]) {
				return fmt.Errorf("product change for %s is not satisfied", id)
			}
		}
	}
	return nil
}

func asObject(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode commercial_terms: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, fmt.Errorf("commercial_terms must be an object")
	}
	return out, nil
}
func objectField(value map[string]any, key string) (map[string]any, bool) {
	field, ok := value[key].(map[string]any)
	return field, ok
}
func stringField(value map[string]any, key string) (string, bool) {
	field, ok := value[key].(string)
	return field, ok
}
func numberField(value map[string]any, key string) (float64, bool) {
	switch n := value[key].(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
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
	canonical, err := idempotency.Canonicalize(commercialTerms)
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
	if proposal == nil {
		return fmt.Errorf("proposal is required")
	}
	proposal["parent_proposal_id"] = sourceProposalID
	if _, ok := proposal["proposal_status"]; !ok {
		proposal["proposal_status"] = "draft"
	}
	terms, ok := proposal["commercial_terms"]
	if !ok {
		return fmt.Errorf("commercial_terms is required")
	}
	termsJSON, err := json.Marshal(terms)
	if err != nil {
		return fmt.Errorf("marshal commercial_terms: %w", err)
	}
	digest, err := ComputeTermsDigest(termsJSON)
	if err != nil {
		return err
	}
	proposal["terms_digest"] = digest
	return nil
}
