package adcp

import (
	"encoding/json"
	"testing"
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
		"proposal_status": "committed",
	}
	if err := StampSuccessor(proposal, "parent-1"); err != nil {
		t.Fatal(err)
	}
	if proposal["proposal_status"] != "committed" {
		t.Fatal("should preserve existing proposal_status")
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
	json1 := `{
		"source_proposal_id": "src-1",
		"outcome": "revised",
		"proposal": {"proposal_id": "new-1", "proposal_status": "draft"}
	}`
	var r RefinementResult
	if err := json.Unmarshal([]byte(json1), &r); err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeRevised {
		t.Fatalf("expected revised, got %s", r.Outcome)
	}
	if r.SourceProposalID != "src-1" {
		t.Fatalf("expected src-1, got %s", r.SourceProposalID)
	}

	json2 := `{
		"source_proposal_id": "src-2",
		"outcome": "unable",
		"reason": "commercially_declined"
	}`
	var r2 RefinementResult
	if err := json.Unmarshal([]byte(json2), &r2); err != nil {
		t.Fatal(err)
	}
	if r2.Outcome != OutcomeUnable {
		t.Fatalf("expected unable, got %s", r2.Outcome)
	}
	if r2.Reason != "commercially_declined" {
		t.Fatalf("expected commercially_declined, got %s", r2.Reason)
	}
}
