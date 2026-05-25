package adcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestControllerStore is the seller-side interface for comply_test_controller.
// Implement the methods for each scenario you support.
// Unimplemented (nil) methods mean that scenario is excluded from list_scenarios.
type TestControllerStore struct {
	ForceAccountStatus  func(accountID, status string) (*StateTransition, error)
	ForceMediaBuyStatus func(mediaBuyID, status string, rejectionReason string) (*StateTransition, error)
	ForceCreativeStatus func(creativeID, status string, rejectionReason string) (*StateTransition, error)
	ForceSessionStatus  func(sessionID, status string, terminationReason string) (*StateTransition, error)
	SimulateDelivery    func(mediaBuyID string, params SimulateDeliveryParams) (*SimulationResult, error)
	SimulateBudgetSpend func(params SimulateBudgetParams) (*SimulationResult, error)
	CustomScenario      func(scenario string, params map[string]any) (any, error)
	CustomScenarios     []string
}

// StateTransition is returned by force_* scenarios.
type StateTransition struct {
	Success       bool   `json:"success"`
	PreviousState string `json:"previous_state"`
	CurrentState  string `json:"current_state"`
}

// SimulateDeliveryParams contains delivery simulation parameters.
type SimulateDeliveryParams struct {
	Impressions   int            `json:"impressions,omitempty"`
	Clicks        int            `json:"clicks,omitempty"`
	ReportedSpend *ReportedSpend `json:"reported_spend,omitempty"`
	Conversions   int            `json:"conversions,omitempty"`
}

// ReportedSpend is the spend amount and currency.
type ReportedSpend struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// SimulateBudgetParams contains budget simulation parameters.
type SimulateBudgetParams struct {
	AccountID       string  `json:"account_id,omitempty"`
	MediaBuyID      string  `json:"media_buy_id,omitempty"`
	SpendPercentage float64 `json:"spend_percentage"`
}

// SimulationResult is returned by simulate_* scenarios.
type SimulationResult struct {
	Success    bool `json:"success"`
	Simulated  any  `json:"simulated"`
	Cumulative any  `json:"cumulative,omitempty"`
}

// TestControllerError is a typed error for test controller store methods.
type TestControllerError struct {
	Code         string // NOT_FOUND, INVALID_TRANSITION, INVALID_PARAMS
	Message      string
	CurrentState string
}

func (e *TestControllerError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type controllerInput struct {
	Scenario string            `json:"scenario"`
	Account  *AccountReference `json:"account,omitempty"`
	Params   map[string]any    `json:"params,omitempty"`
	Context  any               `json:"context,omitempty"`
}

type controllerErrorResponse struct {
	Success      bool   `json:"success"`
	Error        string `json:"error"`
	ErrorDetail  string `json:"error_detail"`
	CurrentState string `json:"current_state,omitempty"`
}

type listScenariosResponse struct {
	Success   bool     `json:"success"`
	Scenarios []string `json:"scenarios"`
}

// RegisterTestController adds the comply_test_controller tool to an MCP server.
// This tool allows arbitrary state mutations for compliance testing and MUST NOT
// be registered in production. Gate registration on a sandbox flag in your agent.
func RegisterTestController(server *mcp.Server, store *TestControllerStore) {
	AddTool(server, "comply_test_controller",
		"Triggers seller-side state transitions for compliance testing. Sandbox only.",
		func(ctx context.Context, req *mcp.CallToolRequest, input controllerInput) (*mcp.CallToolResult, any, error) {
			result, out, err := handleTestController(store, input)
			return attachContext(result, input.Context), out, err
		})
}

func handleTestController(store *TestControllerStore, input controllerInput) (*mcp.CallToolResult, any, error) {
	if input.Scenario == "" {
		return controllerErr("INVALID_PARAMS", "Missing required field: scenario", "")
	}

	if input.Scenario == "list_scenarios" {
		scenarios := listScenarios(store)
		resp := listScenariosResponse{Success: true, Scenarios: scenarios}
		return controllerOK(resp)
	}

	switch input.Scenario {
	case "force_account_status":
		return handleForceAccount(store, input.Params)
	case "force_media_buy_status":
		return handleForceMediaBuy(store, input.Params)
	case "force_creative_status":
		return handleForceCreative(store, input.Params)
	case "force_session_status":
		return handleForceSession(store, input.Params)
	case "simulate_delivery":
		return handleSimulateDelivery(store, input.Params)
	case "simulate_budget_spend":
		return handleSimulateBudget(store, input.Params)
	default:
		if store.CustomScenario != nil {
			result, err := store.CustomScenario(input.Scenario, input.Params)
			if err != nil {
				if tce, ok := err.(*TestControllerError); ok {
					return controllerErr(tce.Code, tce.Message, tce.CurrentState)
				}
				return controllerErr("INTERNAL_ERROR", "An unexpected error occurred in the test controller store", "")
			}
			return controllerOK(result)
		}
		return controllerErr("UNKNOWN_SCENARIO", "Unrecognized scenario name", "")
	}
}

func handleForceAccount(store *TestControllerStore, params map[string]any) (*mcp.CallToolResult, any, error) {
	if store.ForceAccountStatus == nil {
		return controllerErr("UNKNOWN_SCENARIO", "Scenario not supported: force_account_status", "")
	}
	accountID, _ := params["account_id"].(string)
	status, _ := params["status"].(string)
	if accountID == "" || status == "" {
		return controllerErr("INVALID_PARAMS", "force_account_status requires params.account_id and params.status", "")
	}
	result, err := store.ForceAccountStatus(accountID, status)
	return wrapStoreResult(result, err)
}

func handleForceMediaBuy(store *TestControllerStore, params map[string]any) (*mcp.CallToolResult, any, error) {
	if store.ForceMediaBuyStatus == nil {
		return controllerErr("UNKNOWN_SCENARIO", "Scenario not supported: force_media_buy_status", "")
	}
	mediaBuyID, _ := params["media_buy_id"].(string)
	status, _ := params["status"].(string)
	reason, _ := params["rejection_reason"].(string)
	if mediaBuyID == "" || status == "" {
		return controllerErr("INVALID_PARAMS", "force_media_buy_status requires params.media_buy_id and params.status", "")
	}
	result, err := store.ForceMediaBuyStatus(mediaBuyID, status, reason)
	return wrapStoreResult(result, err)
}

func handleForceCreative(store *TestControllerStore, params map[string]any) (*mcp.CallToolResult, any, error) {
	if store.ForceCreativeStatus == nil {
		return controllerErr("UNKNOWN_SCENARIO", "Scenario not supported: force_creative_status", "")
	}
	creativeID, _ := params["creative_id"].(string)
	status, _ := params["status"].(string)
	reason, _ := params["rejection_reason"].(string)
	if creativeID == "" || status == "" {
		return controllerErr("INVALID_PARAMS", "force_creative_status requires params.creative_id and params.status", "")
	}
	result, err := store.ForceCreativeStatus(creativeID, status, reason)
	return wrapStoreResult(result, err)
}

func handleForceSession(store *TestControllerStore, params map[string]any) (*mcp.CallToolResult, any, error) {
	if store.ForceSessionStatus == nil {
		return controllerErr("UNKNOWN_SCENARIO", "Scenario not supported: force_session_status", "")
	}
	sessionID, _ := params["session_id"].(string)
	status, _ := params["status"].(string)
	reason, _ := params["termination_reason"].(string)
	if sessionID == "" || status == "" {
		return controllerErr("INVALID_PARAMS", "force_session_status requires params.session_id and params.status", "")
	}
	result, err := store.ForceSessionStatus(sessionID, status, reason)
	return wrapStoreResult(result, err)
}

func handleSimulateDelivery(store *TestControllerStore, params map[string]any) (*mcp.CallToolResult, any, error) {
	if store.SimulateDelivery == nil {
		return controllerErr("UNKNOWN_SCENARIO", "Scenario not supported: simulate_delivery", "")
	}
	mediaBuyID, _ := params["media_buy_id"].(string)
	if mediaBuyID == "" {
		return controllerErr("INVALID_PARAMS", "simulate_delivery requires params.media_buy_id", "")
	}
	var p SimulateDeliveryParams
	b, _ := json.Marshal(params)
	if err := json.Unmarshal(b, &p); err != nil {
		return controllerErr("INVALID_PARAMS", "Invalid params: could not parse delivery simulation parameters", "")
	}
	result, err := store.SimulateDelivery(mediaBuyID, p)
	return wrapSimResult(result, err)
}

func handleSimulateBudget(store *TestControllerStore, params map[string]any) (*mcp.CallToolResult, any, error) {
	if store.SimulateBudgetSpend == nil {
		return controllerErr("UNKNOWN_SCENARIO", "Scenario not supported: simulate_budget_spend", "")
	}
	var p SimulateBudgetParams
	b, _ := json.Marshal(params)
	if err := json.Unmarshal(b, &p); err != nil {
		return controllerErr("INVALID_PARAMS", "Invalid params: could not parse budget simulation parameters", "")
	}
	if p.AccountID == "" && p.MediaBuyID == "" {
		return controllerErr("INVALID_PARAMS", "simulate_budget_spend requires params.account_id or params.media_buy_id", "")
	}
	if p.SpendPercentage == 0 {
		// Check raw value since 0 could be intentional
		if _, ok := params["spend_percentage"]; !ok {
			return controllerErr("INVALID_PARAMS", "simulate_budget_spend requires params.spend_percentage", "")
		}
	}
	result, err := store.SimulateBudgetSpend(p)
	return wrapSimResult(result, err)
}

func listScenarios(store *TestControllerStore) []string {
	var scenarios []string
	if store.ForceAccountStatus != nil {
		scenarios = append(scenarios, "force_account_status")
	}
	if store.ForceMediaBuyStatus != nil {
		scenarios = append(scenarios, "force_media_buy_status")
	}
	if store.ForceCreativeStatus != nil {
		scenarios = append(scenarios, "force_creative_status")
	}
	if store.ForceSessionStatus != nil {
		scenarios = append(scenarios, "force_session_status")
	}
	if store.SimulateDelivery != nil {
		scenarios = append(scenarios, "simulate_delivery")
	}
	if store.SimulateBudgetSpend != nil {
		scenarios = append(scenarios, "simulate_budget_spend")
	}
	if store.CustomScenario != nil {
		scenarios = append(scenarios, store.CustomScenarios...)
	}
	return scenarios
}

func wrapStoreResult(result *StateTransition, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		if tce, ok := err.(*TestControllerError); ok {
			return controllerErr(tce.Code, tce.Message, tce.CurrentState)
		}
		return controllerErr("INTERNAL_ERROR", "An unexpected error occurred in the test controller store", "")
	}
	return controllerOK(result)
}

func wrapSimResult(result *SimulationResult, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		if tce, ok := err.(*TestControllerError); ok {
			return controllerErr(tce.Code, tce.Message, tce.CurrentState)
		}
		return controllerErr("INTERNAL_ERROR", "An unexpected error occurred in the test controller store", "")
	}
	return controllerOK(result)
}

func controllerOK(data any) (*mcp.CallToolResult, any, error) {
	b, _ := json.Marshal(data)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
		StructuredContent: jsonRoundTrip(data),
	}, data, nil
}

func controllerErr(code, detail, currentState string) (*mcp.CallToolResult, any, error) {
	resp := controllerErrorResponse{
		Success:      false,
		Error:        code,
		ErrorDetail:  detail,
		CurrentState: currentState,
	}
	b, _ := json.Marshal(resp)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
		IsError:           true,
		StructuredContent: jsonRoundTrip(resp),
	}, resp, nil
}
