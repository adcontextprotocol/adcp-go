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

func TestForceCreateMediaBuyArm_Submitted(t *testing.T) {
	store := &TestControllerStore{
		ForceCreateMediaBuyArm: func(arm, taskID, message string) (*ForcedDirectiveSuccess, error) {
			return &ForcedDirectiveSuccess{Success: true, Arm: arm, TaskID: taskID}, nil
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_create_media_buy_arm",
		Params:   map[string]any{"arm": "submitted", "task_id": "task-abc"},
	})
	require.False(t, result.IsError, "expected success")

	var resp ForcedDirectiveSuccess
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.True(t, resp.Success)
	assert.Equal(t, "submitted", resp.Arm)
	assert.Equal(t, "task-abc", resp.TaskID)
}

func TestForceCreateMediaBuyArm_InputRequired(t *testing.T) {
	store := &TestControllerStore{
		ForceCreateMediaBuyArm: func(arm, taskID, message string) (*ForcedDirectiveSuccess, error) {
			return &ForcedDirectiveSuccess{Success: true, Arm: arm, Message: message}, nil
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_create_media_buy_arm",
		Params:   map[string]any{"arm": "input-required", "message": "needs clarification"},
	})
	require.False(t, result.IsError, "expected success")

	var resp ForcedDirectiveSuccess
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.Equal(t, "input-required", resp.Arm)
	assert.Equal(t, "needs clarification", resp.Message)
}

func TestForceCreateMediaBuyArm_InvalidParams(t *testing.T) {
	store := &TestControllerStore{
		ForceCreateMediaBuyArm: func(arm, taskID, message string) (*ForcedDirectiveSuccess, error) {
			return &ForcedDirectiveSuccess{Success: true, Arm: arm}, nil
		},
	}

	cases := []struct {
		name   string
		params map[string]any
		code   string
	}{
		{"missing arm", map[string]any{}, "INVALID_PARAMS"},
		{"invalid arm", map[string]any{"arm": "unknown"}, "INVALID_PARAMS"},
		{"submitted without task_id", map[string]any{"arm": "submitted"}, "INVALID_PARAMS"},
		{"task_id too long", map[string]any{"arm": "submitted", "task_id": string(make([]byte, 129))}, "INVALID_PARAMS"},
		{"message too long", map[string]any{"arm": "input-required", "message": string(make([]byte, 2001))}, "INVALID_PARAMS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _, _ := handleTestController(store, controllerInput{
				Scenario: "force_create_media_buy_arm",
				Params:   tc.params,
			})
			require.True(t, result.IsError, "expected error")
			var resp controllerErrorResponse
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &resp))
			assert.Equal(t, tc.code, resp.Error)
		})
	}
}

func TestForceTaskCompletion_Valid(t *testing.T) {
	store := &TestControllerStore{
		ForceTaskCompletion: func(taskID string, result json.RawMessage) (*StateTransitionSuccess, error) {
			return &StateTransitionSuccess{Success: true, PreviousState: "submitted", CurrentState: "completed"}, nil
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_task_completion",
		Params:   map[string]any{"task_id": "t-1", "result": map[string]any{"status": "ok"}},
	})
	require.False(t, result.IsError, "expected success")

	var resp StateTransitionSuccess
	text := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &resp))

	assert.True(t, resp.Success)
	assert.Equal(t, "submitted", resp.PreviousState)
	assert.Equal(t, "completed", resp.CurrentState)
}

func TestForceTaskCompletion_InvalidParams(t *testing.T) {
	store := &TestControllerStore{
		ForceTaskCompletion: func(taskID string, result json.RawMessage) (*StateTransitionSuccess, error) {
			return &StateTransitionSuccess{Success: true}, nil
		},
	}

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing task_id", map[string]any{"result": map[string]any{"x": 1}}},
		{"task_id too long", map[string]any{"task_id": string(make([]byte, 129)), "result": map[string]any{"x": 1}}},
		{"missing result", map[string]any{"task_id": "t-1"}},
		{"empty result", map[string]any{"task_id": "t-1", "result": map[string]any{}}},
		{"result not object", map[string]any{"task_id": "t-1", "result": "not-an-object"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _, _ := handleTestController(store, controllerInput{
				Scenario: "force_task_completion",
				Params:   tc.params,
			})
			require.True(t, result.IsError, "expected error")
			var resp controllerErrorResponse
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &resp))
			assert.Equal(t, "INVALID_PARAMS", resp.Error)
		})
	}
}

func TestForceTaskCompletion_NotFound(t *testing.T) {
	store := &TestControllerStore{
		ForceTaskCompletion: func(taskID string, result json.RawMessage) (*StateTransitionSuccess, error) {
			return nil, &TestControllerError{Code: "NOT_FOUND", Message: "task not found for this account"}
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_task_completion",
		Params:   map[string]any{"task_id": "other-account-task", "result": map[string]any{"x": 1}},
	})
	require.True(t, result.IsError)
	var resp controllerErrorResponse
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &resp))
	assert.Equal(t, "NOT_FOUND", resp.Error)
}

func TestForceTaskCompletion_InvalidTransition(t *testing.T) {
	store := &TestControllerStore{
		ForceTaskCompletion: func(taskID string, result json.RawMessage) (*StateTransitionSuccess, error) {
			return nil, &TestControllerError{Code: "INVALID_TRANSITION", Message: "task already completed with different result", CurrentState: "completed"}
		},
	}
	result, _, _ := handleTestController(store, controllerInput{
		Scenario: "force_task_completion",
		Params:   map[string]any{"task_id": "t-done", "result": map[string]any{"new": "payload"}},
	})
	require.True(t, result.IsError)
	var resp controllerErrorResponse
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &resp))
	assert.Equal(t, "INVALID_TRANSITION", resp.Error)
	assert.Equal(t, "completed", resp.CurrentState)
}

func TestListScenarios_IncludesNewScenarios(t *testing.T) {
	store := &TestControllerStore{
		ForceCreateMediaBuyArm: func(arm, taskID, message string) (*ForcedDirectiveSuccess, error) { return nil, nil },
		ForceTaskCompletion:    func(taskID string, result json.RawMessage) (*StateTransitionSuccess, error) { return nil, nil },
	}
	result, _, _ := handleTestController(store, controllerInput{Scenario: "list_scenarios"})
	require.False(t, result.IsError)

	var resp listScenariosResponse
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &resp))

	assert.Contains(t, resp.Scenarios, "force_create_media_buy_arm")
	assert.Contains(t, resp.Scenarios, "force_task_completion")
}
