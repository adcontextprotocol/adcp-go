package adcp

import (
	"encoding/json"
	"testing"
)

func TestGetProductsRefinementItemsAreTyped(t *testing.T) {
	raw := []byte(`{
		"buying_mode": "refine",
		"refine": [
			{"scope": "request", "ask": "more video"},
			{"scope": "product", "product_id": "prod-1", "action": "include"},
			{"scope": "proposal", "proposal_id": "prop-1", "action": "finalize"}
		]
	}`)

	var req GetProductsRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal get products request: %v", err)
	}
	if got := len(req.Refine); got != 3 {
		t.Fatalf("len(req.Refine) = %d, want 3", got)
	}
	if req.Refine[1].ProductID != "prod-1" {
		t.Fatalf("product refinement = %#v", req.Refine[1])
	}
	if req.Refine[2].ProposalID != "prop-1" {
		t.Fatalf("proposal refinement = %#v", req.Refine[2])
	}

	resp := GetProductsResponse{
		Status: TaskStatusCompleted,
		RefinementApplied: []GetProductsRefinementAppliedItem{
			{Scope: "request", Status: "applied"},
			{Scope: "product", ProductID: "prod-1", Status: "partial", Notes: "changed format"},
			{Scope: "proposal", ProposalID: "prop-1", Status: "applied"},
		},
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal get products response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode marshaled response: %v", err)
	}
	items, ok := decoded["refinement_applied"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("refinement_applied = %#v, want 3 items", decoded["refinement_applied"])
	}
	product, ok := items[1].(map[string]any)
	if !ok {
		t.Fatalf("product refinement item = %#v", items[1])
	}
	if product["product_id"] != "prod-1" || product["status"] != "partial" {
		t.Fatalf("product refinement item = %#v", product)
	}
}
