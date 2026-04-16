package adcp

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListScenarios(t *testing.T) {
	store := &TestControllerStore{
		ForceAccountStatus:  func(id, status string) (*StateTransition, error) { return nil, nil },
		ForceMediaBuyStatus: func(id, status, reason string) (*StateTransition, error) { return nil, nil },
		SimulateDelivery:    func(id string, p SimulateDeliveryParams) (*SimulationResult, error) { return nil, nil },
	}

	result, _, _ := handleTestController(store, controllerInput{Scenario: "list_scenarios"})
	require.False(t, result.IsError, "expected success")

	var resp listScenariosResponse
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	require.True(t, resp.Success, "expected success=true")
	assert.Len(t, resp.Scenarios, 3, "expected 3 scenarios, got %d: %v", len(resp.Scenarios), resp.Scenarios)
}

func TestUnknownScenario(t *testing.T) {
	store := &TestControllerStore{}
	result, _, _ := handleTestController(store, controllerInput{Scenario: "nonexistent"})
	require.True(t, result.IsError, "expected error")

	var resp controllerErrorResponse
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.Equal(t, "UNKNOWN_SCENARIO", resp.Error)
}

func TestMissingParams(t *testing.T) {
	store := &TestControllerStore{
		ForceCreativeStatus: func(id, status, reason string) (*StateTransition, error) { return nil, nil },
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_creative_status",
		Params:   map[string]any{},
	})
	require.True(t, result.IsError, "expected error")

	var resp controllerErrorResponse
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.Equal(t, "INVALID_PARAMS", resp.Error)
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
	require.False(t, result.IsError, "expected success")

	var resp StateTransition
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.Equal(t, "suspended", resp.CurrentState)
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
	require.True(t, result.IsError, "expected error")

	var resp controllerErrorResponse
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.Equal(t, "NOT_FOUND", resp.Error)
}
