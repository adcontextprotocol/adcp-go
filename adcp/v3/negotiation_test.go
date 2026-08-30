package adcp

import (
	"encoding/json"
	"testing"
	"time"
)

func TestComputeTermsDigest(t *testing.T) {
	terms := json.RawMessage(`{"price":42,"currency":"USD"}`)
	digest, err := ComputeTermsDigest(terms)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) < 10 || digest[:7] != "sha256:" {
		t.Fatalf("unexpected digest format: %s", digest)
	}
}

func TestVerifyTermsDigest(t *testing.T) {
	terms := json.RawMessage(`{"price":42,"currency":"USD"}`)
	digest, err := ComputeTermsDigest(terms)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyTermsDigest(digest, terms) {
		t.Fatal("valid digest should verify")
	}
	if VerifyTermsDigest("sha256:AAAA_wrong", terms) {
		t.Fatal("wrong digest should not verify")
	}
}

func TestDigestDeterministic(t *testing.T) {
	// Key order shouldn't matter per JCS
	a := json.RawMessage(`{"b":2,"a":1}`)
	b := json.RawMessage(`{"a":1,"b":2}`)
	da, _ := ComputeTermsDigest(a)
	db, _ := ComputeTermsDigest(b)
	if da != db {
		t.Fatalf("JCS digests should match regardless of key order: %s vs %s", da, db)
	}
}

func TestStampSuccessor(t *testing.T) {
	proposal := map[string]any{
		"commercial_terms": map[string]any{"total_budget": 10000},
	}
	if err := StampSuccessor(proposal, "parent-1"); err != nil {
		t.Fatal(err)
	}
	if proposal["parent_proposal_id"] != "parent-1" {
		t.Fatal("parent_proposal_id not set")
	}
	if proposal["proposal_status"] != "draft" {
		t.Fatal("proposal_status should default to draft")
	}
	digest, ok := proposal["terms_digest"].(string)
	if !ok || len(digest) < 7 || digest[:7] != "sha256:" {
		t.Fatalf("terms_digest not computed: %v", proposal["terms_digest"])
	}
}

func TestStampSuccessorPreservesStatus(t *testing.T) {
	proposal := map[string]any{
		"proposal_status":  "committed",
		"commercial_terms": map[string]any{"purchases": []any{}},
	}
	if err := StampSuccessor(proposal, "parent-1"); err != nil {
		t.Fatal(err)
	}
	if proposal["proposal_status"] != "committed" {
		t.Fatal("should preserve existing proposal_status")
	}
}

func TestStampSuccessorRequiresCommercialTerms(t *testing.T) {
	if err := StampSuccessor(map[string]any{}, "parent-1"); err == nil {
		t.Fatal("missing commercial_terms should fail closed")
	}
}

func TestRefinementConstraintsRoundTrip(t *testing.T) {
	c := &RefinementConstraints{
		TotalBudget: &BudgetConstraint{
			Min:      Float64(5000),
			Max:      Float64(10000),
			Currency: "USD",
		},
		CPM: &CPMConstraint{Max: 12.50, Currency: "USD"},
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back RefinementConstraints
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.TotalBudget == nil || *back.TotalBudget.Max != 10000 {
		t.Fatal("budget max not round-tripped")
	}
	if back.CPM == nil || back.CPM.Max != 12.50 {
		t.Fatal("CPM max not round-tripped")
	}
}

func TestProposalRefinementRoundTrip(t *testing.T) {
	r := ProposalRefinement{
		ProposalID: "p-1",
		Action:     "revise",
		Ask:        "lower CPM to $8",
		Constraints: &RefinementConstraints{
			CPM: &CPMConstraint{Max: 8.0, Currency: "USD"},
		},
		Alternatives: &AlternativesRequest{Count: 3},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back ProposalRefinement
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.ProposalID != "p-1" {
		t.Fatal("proposal_id not round-tripped")
	}
	if back.Alternatives == nil || back.Alternatives.Count != 3 {
		t.Fatal("alternatives not round-tripped")
	}
}

func TestRefinementResultParsing(t *testing.T) {
	json1 := `{"source_proposal_id":"src-1","outcome":"unable","reason_code":"commercially_declined","reason":"seller declined the ask"}`
	var r RefinementResult
	if err := json.Unmarshal([]byte(json1), &r); err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeUnable {
		t.Fatalf("expected unable, got %s", r.Outcome)
	}
	if r.SourceProposalID != "src-1" {
		t.Fatalf("expected src-1, got %s", r.SourceProposalID)
	}

	if r.ReasonCode != "commercially_declined" {
		t.Fatalf("expected commercially_declined, got %s", r.ReasonCode)
	}
}

func TestValidateRefineProposalsRequestRejectsUnsupportedAndOversizedWork(t *testing.T) {
	capability := &RefinementCapability{SupportedDimensions: []string{"cpm", "alternatives"}, MaxAlternatives: 3}
	_, err := NewRefineProposalsRequest("idem-1", []ProposalRefinement{{
		ProposalID: "p-1", Action: "revise",
		Constraints:  &RefinementConstraints{CPM: &CPMConstraint{Max: 8, Currency: "USD"}},
		Alternatives: &AlternativesRequest{Count: 4},
	}}, capability)
	if err == nil {
		t.Fatal("seller alternatives ceiling should be enforced")
	}

	_, err = NewRefineProposalsRequest("idem-2", []ProposalRefinement{{
		ProposalID: "p-1", Action: "revise",
		Constraints: &RefinementConstraints{TotalBudget: &BudgetConstraint{Currency: "USD", Max: Float64(1000)}},
	}}, capability)
	if err == nil {
		t.Fatal("undeclared typed dimension should be rejected")
	}

	refinements := make([]ProposalRefinement, ProtocolMaxRefinements+1)
	for i := range refinements {
		refinements[i] = ProposalRefinement{ProposalID: string(rune('a' + i)), Action: "finalize"}
	}
	if _, err := NewRefineProposalsRequest("idem-3", refinements, nil); err == nil {
		t.Fatal("protocol refinement maximum should be enforced")
	}
}

func TestVerifyRefineProposalsResponseChecksConstraintsLineageAndDigests(t *testing.T) {
	req := &RefineProposalsRequest{IdempotencyKey: "idem-1", Refinements: []ProposalRefinement{{
		ProposalID: "source-1", Action: "revise",
		Constraints: &RefinementConstraints{
			TotalBudget: &BudgetConstraint{Currency: "USD", Max: Float64(1000)},
			CPM:         &CPMConstraint{Max: 8, Currency: "USD"},
			Impressions: &ImpressionsConstraint{Min: 900},
			Flight:      &FlightConstraint{StartNoLaterThan: "2026-09-02T00:00:00Z", EndNoEarlierThan: "2026-09-30T00:00:00Z"},
		},
		ProductChanges: map[string]string{"prod-new": "include", "prod-old": "omit"},
		Alternatives:   &AlternativesRequest{Count: 2},
	}}}
	data := &RefineProposalsData{Status: "completed", Results: []RefinementResult{{
		SourceProposalID: "source-1", Outcome: OutcomeRevised,
		Proposals: []CanonicalProposal{
			validNegotiationProposal(t, "successor-1", "source-1", 800, 7),
			validNegotiationProposal(t, "successor-2", "source-1", 900, 6),
		},
	}}}
	if err := VerifyRefineProposalsResponse(req, data, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}

	data.Results[0].Proposals[1].TermsDigest = "sha256:wrong"
	if err := VerifyRefineProposalsResponse(req, data, time.Now()); err == nil {
		t.Fatal("digest mismatch should be rejected")
	}
}

func TestVerifyRefineProposalsResponseRejectsExpiredFinalizeHold(t *testing.T) {
	req := &RefineProposalsRequest{IdempotencyKey: "idem-finalize", Refinements: []ProposalRefinement{{ProposalID: "draft-1", Action: "finalize"}}}
	proposal := validNegotiationProposal(t, "committed-1", "draft-1", 500, 5)
	proposal.ProposalStatus = "committed"
	proposal.ExpiresAt = "2026-08-29T00:00:00Z"
	data := &RefineProposalsData{Status: "completed", Results: []RefinementResult{{SourceProposalID: "draft-1", Outcome: OutcomeFinalized, Proposal: &proposal}}}
	if err := VerifyRefineProposalsResponse(req, data, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("expired committed hold should be rejected")
	}
}

func TestVerifyRefineProposalsResponseRejectsInvalidOutcomeShapes(t *testing.T) {
	req := &RefineProposalsRequest{IdempotencyKey: "idem-1", Refinements: []ProposalRefinement{{ProposalID: "source-1", Action: "revise", Ask: "lower rate"}}}
	proposal := validNegotiationProposal(t, "successor-1", "source-1", 500, 5)

	tests := []struct {
		name   string
		result RefinementResult
	}{
		{name: "revised failure details", result: RefinementResult{SourceProposalID: "source-1", Outcome: OutcomeRevised, Proposals: []CanonicalProposal{proposal}, ReasonCode: "commercially_declined", Reason: "no"}},
		{name: "partial singular proposal", result: RefinementResult{SourceProposalID: "source-1", Outcome: OutcomePartial, Proposal: &proposal, Proposals: []CanonicalProposal{proposal}, ReasonCode: "alternatives_unavailable", Reason: "one available"}},
		{name: "unable proposal", result: RefinementResult{SourceProposalID: "source-1", Outcome: OutcomeUnable, Proposal: &proposal, ReasonCode: "commercially_declined", Reason: "no"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &RefineProposalsData{Status: "completed", Results: []RefinementResult{tt.result}}
			if err := VerifyRefineProposalsResponse(req, data, time.Now()); err == nil {
				t.Fatal("invalid outcome shape should be rejected")
			}
		})
	}
}

func TestVerifyRefineProposalsResponseFailsClosedWithoutPurchases(t *testing.T) {
	req := &RefineProposalsRequest{IdempotencyKey: "idem-1", Refinements: []ProposalRefinement{{
		ProposalID: "source-1", Action: "revise", Constraints: &RefinementConstraints{CPM: &CPMConstraint{Max: 8, Currency: "USD"}},
	}}}
	proposal := validNegotiationProposal(t, "successor-1", "source-1", 500, 5)
	terms := proposal.CommercialTerms.(map[string]any)
	delete(terms, "purchases")
	raw, err := json.Marshal(terms)
	if err != nil {
		t.Fatal(err)
	}
	proposal.TermsDigest, err = ComputeTermsDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	data := &RefineProposalsData{Status: "completed", Results: []RefinementResult{{SourceProposalID: "source-1", Outcome: OutcomeRevised, Proposals: []CanonicalProposal{proposal}}}}
	if err := VerifyRefineProposalsResponse(req, data, time.Now()); err == nil {
		t.Fatal("missing purchases should fail closed")
	}
}

func validNegotiationProposal(t *testing.T, id, parent string, budget, cpm float64) CanonicalProposal {
	t.Helper()
	terms := map[string]any{
		"total_budget": map[string]any{"amount": budget, "currency": "USD"},
		"start_time":   "2026-09-01T00:00:00Z",
		"end_time":     "2026-10-01T00:00:00Z",
		"purchases": []any{map[string]any{
			"product_id": "prod-new", "impressions": 1000.0,
			"pricing": map[string]any{"pricing_model": "cpm", "currency": "USD", "fixed_price": cpm},
		}},
	}
	raw, err := json.Marshal(terms)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ComputeTermsDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return CanonicalProposal{
		ProposalID: id, ProposalKind: "new", ParentProposalID: parent,
		ProposalStatus: "draft", Name: id, CommercialTerms: terms, TermsDigest: digest,
	}
}
