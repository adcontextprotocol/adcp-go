package adcp_test

import (
	"fmt"
	"time"

	adcp "github.com/adcontextprotocol/adcp-go/adcp/v3"
)

// This example shows the reusable buyer-side flow around the transport call:
// discover capability, validate before sending, preserve the idempotency key
// for an exact retry, and verify the seller response before accepting it.
func ExampleNewRefineProposalsRequest() {
	capability := &adcp.RefinementCapability{
		SupportedDimensions: []string{"total_budget", "alternatives"},
		MaxAlternatives:     3,
	}
	request, err := adcp.NewRefineProposalsRequest("refine-attempt-1", []adcp.ProposalRefinement{{
		ProposalID: "proposal-1",
		Action:     "revise",
		Constraints: &adcp.RefinementConstraints{TotalBudget: &adcp.BudgetConstraint{
			Currency: "USD",
			Max:      adcp.Float64(25_000),
		}},
		Alternatives: &adcp.AlternativesRequest{Count: 2},
	}}, capability)
	if err != nil {
		panic(err)
	}

	// Send request with the caller's MCP/A2A transport. An exact retry reuses
	// refine-attempt-1; any changed request receives a new idempotency key.
	response := callSeller(request)
	if err := adcp.VerifyRefineProposalsResponse(request, response, time.Now()); err != nil {
		panic(err)
	}
	fmt.Println(len(response.Results))

	// Output: 0
}

func callSeller(*adcp.RefineProposalsRequest) *adcp.RefineProposalsData {
	// Transport is intentionally application-owned in adcp-go.
	return &adcp.RefineProposalsData{Status: "submitted", TaskID: "task-1"}
}
