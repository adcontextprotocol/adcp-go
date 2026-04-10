package adcp

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestListScenarios(t *testing.T) {
	store := &TestControllerStore{
		ForceAccountStatus:  func(id, status string) (*StateTransition, error) { return nil, nil },
		ForceMediaBuyStatus: func(id, status, reason string) (*StateTransition, error) { return nil, nil },
		SimulateDelivery:    func(id string, p SimulateDeliveryParams) (*SimulationResult, error) { return nil, nil },
	}

	result, _, _ := handleTestController(store, controllerInput{Scenario: "list_scenarios"})
	if result.IsError {
		t.Fatal("expected success")
	}

	var resp listScenariosResponse
	text := result.Content[0].(*mcp.TextContent).Text
	json.Unmarshal([]byte(text), &resp)

	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if len(resp.Scenarios) != 3 {
		t.Fatalf("expected 3 scenarios, got %d: %v", len(resp.Scenarios), resp.Scenarios)
	}
}

func TestUnknownScenario(t *testing.T) {
	store := &TestControllerStore{}
	result, _, _ := handleTestController(store, controllerInput{Scenario: "nonexistent"})
	if !result.IsError {
		t.Fatal("expected error")
	}

	var resp controllerErrorResponse
	text := result.Content[0].(*mcp.TextContent).Text
	json.Unmarshal([]byte(text), &resp)

	if resp.Error != "UNKNOWN_SCENARIO" {
		t.Fatalf("expected UNKNOWN_SCENARIO, got %s", resp.Error)
	}
}

func TestMissingParams(t *testing.T) {
	store := &TestControllerStore{
		ForceCreativeStatus: func(id, status, reason string) (*StateTransition, error) { return nil, nil },
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_creative_status",
		Params:   map[string]any{},
	})
	if !result.IsError {
		t.Fatal("expected error")
	}

	var resp controllerErrorResponse
	text := result.Content[0].(*mcp.TextContent).Text
	json.Unmarshal([]byte(text), &resp)

	if resp.Error != "INVALID_PARAMS" {
		t.Fatalf("expected INVALID_PARAMS, got %s", resp.Error)
	}
}

func TestForceAccountStatus(t *testing.T) {
	store := &TestControllerStore{
		ForceAccountStatus: func(id, status string) (*StateTransition, error) {
			return &StateTransition{
				Success:       true,
				PreviousState: "active",
				CurrentState:  status,
			}, nil
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_account_status",
		Params:   map[string]any{"account_id": "acc-1", "status": "suspended"},
	})
	if result.IsError {
		t.Fatal("expected success")
	}

	var resp StateTransition
	text := result.Content[0].(*mcp.TextContent).Text
	json.Unmarshal([]byte(text), &resp)

	if resp.CurrentState != "suspended" {
		t.Fatalf("expected suspended, got %s", resp.CurrentState)
	}
}

func TestControllerErrorHandling(t *testing.T) {
	store := &TestControllerStore{
		ForceMediaBuyStatus: func(id, status, reason string) (*StateTransition, error) {
			return nil, &TestControllerError{
				Code:         "NOT_FOUND",
				Message:      "Media buy not found",
				CurrentState: "",
			}
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_media_buy_status",
		Params:   map[string]any{"media_buy_id": "mb-999", "status": "active"},
	})
	if !result.IsError {
		t.Fatal("expected error")
	}

	var resp controllerErrorResponse
	text := result.Content[0].(*mcp.TextContent).Text
	json.Unmarshal([]byte(text), &resp)

	if resp.Error != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND, got %s", resp.Error)
	}
}
