package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var permissiveSchema = map[string]any{"type": "object"}

const agentURL = "http://localhost:3001/mcp"

type store struct {
	mu          sync.RWMutex
	deployments map[string][]adcp.Deployment
}

func newStore() *store {
	return &store{deployments: make(map[string][]adcp.Deployment)}
}

var signals = []adcp.Signal{
	{
		SignalAgentSegmentID: "seg-auto-intenders",
		Name:                "Auto Intenders",
		Description:         "Users actively researching vehicle purchases",
		SignalType:          "owned",
		DataProvider:        "DataCo Audiences",
		CoveragePercentage:  18.5,
		Deployments:         []adcp.Deployment{},
		PricingOptions:      []adcp.SignalPricing{{PricingOptionID: "po-auto-cpm", Model: "cpm", CPM: 2.50, Currency: "USD"}},
		SignalID:            adcp.SignalID{Source: "agent", AgentURL: agentURL, ID: "auto-intenders"},
		ValueType:           "binary",
	},
	{
		SignalAgentSegmentID: "seg-income-tier",
		Name:                "Income Tier",
		Description:         "Household income classification (low, mid, high)",
		SignalType:          "owned",
		DataProvider:        "DataCo Audiences",
		CoveragePercentage:  25.0,
		Deployments:         []adcp.Deployment{},
		PricingOptions:      []adcp.SignalPricing{{PricingOptionID: "po-income-cpm", Model: "cpm", CPM: 3.00, Currency: "USD"}},
		SignalID:            adcp.SignalID{Source: "agent", AgentURL: agentURL, ID: "income-tier"},
		ValueType:           "categorical",
	},
	{
		SignalAgentSegmentID: "seg-purchase-propensity",
		Name:                "Purchase Propensity",
		Description:         "Likelihood score for near-term purchase (0-100)",
		SignalType:          "owned",
		DataProvider:        "DataCo Audiences",
		CoveragePercentage:  12.0,
		Deployments:         []adcp.Deployment{},
		PricingOptions:      []adcp.SignalPricing{{PricingOptionID: "po-propensity-cpm", Model: "cpm", CPM: 4.00, Currency: "USD"}},
		SignalID:            adcp.SignalID{Source: "agent", AgentURL: agentURL, ID: "purchase-propensity"},
		ValueType:           "numeric",
	},
	{
		SignalAgentSegmentID: "seg-travel-enthusiasts",
		Name:                "Travel Enthusiasts",
		Description:         "Users with strong travel and vacation interest",
		SignalType:          "owned",
		DataProvider:        "DataCo Audiences",
		CoveragePercentage:  22.0,
		Deployments:         []adcp.Deployment{},
		PricingOptions:      []adcp.SignalPricing{{PricingOptionID: "po-travel-cpm", Model: "cpm", CPM: 2.00, Currency: "USD"}},
		SignalID:            adcp.SignalID{Source: "agent", AgentURL: agentURL, ID: "travel-enthusiasts"},
		ValueType:           "binary",
	},
	{
		SignalAgentSegmentID: "seg-health-conscious",
		Name:                "Health Conscious",
		Description:         "Users interested in fitness, nutrition, and wellness",
		SignalType:          "owned",
		DataProvider:        "DataCo Audiences",
		CoveragePercentage:  20.0,
		Deployments:         []adcp.Deployment{},
		PricingOptions: []adcp.SignalPricing{
			{PricingOptionID: "po-health-cpm", Model: "cpm", CPM: 1.75, Currency: "USD"},
			{PricingOptionID: "po-health-pom", Model: "percent_of_media", Percent: 10, Currency: "USD"},
		},
		SignalID:  adcp.SignalID{Source: "agent", AgentURL: agentURL, ID: "health-conscious"},
		ValueType: "binary",
	},
}

func makeResult(data any, summary string) (*mcp.CallToolResult, error) {
	b, _ := json.Marshal(data)
	// Round-trip through JSON to ensure struct tags are respected in StructuredContent
	var sc map[string]any
	json.Unmarshal(b, &sc)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: summary + "\n" + string(b)}},
		StructuredContent: sc,
	}, nil
}

func makeErrorResult(code, message string) (*mcp.CallToolResult, error) {
	errData := map[string]any{"adcp_error": map[string]any{"code": code, "message": message, "recovery": "terminal"}}
	b, _ := json.Marshal(errData)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		IsError:           true,
		StructuredContent: errData,
	}, nil
}

func parseArgs(req *mcp.CallToolRequest) map[string]any {
	args := make(map[string]any)
	if len(req.Params.Arguments) > 0 {
		json.Unmarshal(req.Params.Arguments, &args)
	}
	return args
}

func main() {
	s := newStore()

	createServer := func() *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "signals-agent", Version: "1.0.0"}, nil)

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "get_adcp_capabilities", Description: "Returns agent capabilities",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return makeResult(&adcp.CapabilitiesData{
				ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
				SupportedProtocols: []string{"signals"},
			}, "Agent capabilities retrieved")
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "get_signals", Description: "Discover available audience signals",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := parseArgs(req)
			s.mu.RLock()
			defer s.mu.RUnlock()

			signalSpec, _ := args["signal_spec"].(string)
			maxResults := 0
			if mr, ok := args["max_results"].(float64); ok {
				maxResults = int(mr)
			}

			var filters map[string]any
			if f, ok := args["filters"].(map[string]any); ok {
				filters = f
			}

			// Build full list with deployments applied
			all := make([]adcp.Signal, 0, len(signals))
			for _, sig := range signals {
				sig2 := sig
				if deps, ok := s.deployments[sig.SignalAgentSegmentID]; ok {
					sig2.Deployments = deps
				}
				all = append(all, sig2)
			}

			// If signal_spec is set, try to filter. If no matches, return all (spec is a hint).
			matched := all
			if signalSpec != "" {
				spec := strings.ToLower(signalSpec)
				var specMatched []adcp.Signal
				for _, sig := range all {
					if strings.Contains(strings.ToLower(sig.Name), spec) ||
						strings.Contains(strings.ToLower(sig.Description), spec) {
						specMatched = append(specMatched, sig)
					}
				}
				if len(specMatched) > 0 {
					matched = specMatched
				}
			}

			// Apply numeric filters
			if filters != nil {
				var filtered []adcp.Signal
				for _, sig := range matched {
					if maxCPM, ok := filters["max_cpm"].(float64); ok && maxCPM > 0 {
						cpmOK := false
						for _, p := range sig.PricingOptions {
							if p.Model == "cpm" && p.CPM <= maxCPM {
								cpmOK = true
								break
							}
						}
						if !cpmOK {
							continue
						}
					}
					if minCov, ok := filters["min_coverage_percentage"].(float64); ok && sig.CoveragePercentage < minCov {
						continue
					}
					filtered = append(filtered, sig)
				}
				matched = filtered
			}

			if maxResults > 0 && len(matched) > maxResults {
				matched = matched[:maxResults]
			}

			out := map[string]any{"signals": matched, "sandbox": true}
			return makeResult(out, fmt.Sprintf("Found %d signals", len(matched)))
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "activate_signal", Description: "Activate a signal to a destination",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := parseArgs(req)
			segID, _ := args["signal_agent_segment_id"].(string)

			var found *adcp.Signal
			for i := range signals {
				if signals[i].SignalAgentSegmentID == segID {
					found = &signals[i]
					break
				}
			}
			if found == nil {
				return makeErrorResult("SIGNAL_NOT_FOUND", fmt.Sprintf("Signal %s not found", segID))
			}

			destsRaw, _ := args["destinations"].([]any)
			deployments := make([]adcp.Deployment, 0)
			for _, raw := range destsRaw {
				dest, _ := raw.(map[string]any)
				if dest == nil {
					continue
				}
				destType, _ := dest["type"].(string)
				switch destType {
				case "platform":
					platform, _ := dest["platform"].(string)
					account, _ := dest["account"].(string)
					deployments = append(deployments, adcp.Deployment{
						Type: "platform", Platform: platform, Account: account, IsLive: true,
						ActivationKey: &adcp.ActivationKey{Type: "segment_id", SegmentID: fmt.Sprintf("plat-%s-%s", platform, found.SignalID.ID)},
					})
				case "agent":
					agentU, _ := dest["agent_url"].(string)
					deployments = append(deployments, adcp.Deployment{
						Type: "agent", AgentURL: agentU, IsLive: true,
						ActivationKey: &adcp.ActivationKey{Type: "key_value", Key: "adcp_segment", Value: found.SignalID.ID},
					})
				default:
					deployments = append(deployments, adcp.Deployment{Type: destType, IsLive: true})
				}
			}

			s.mu.Lock()
			s.deployments[segID] = append(s.deployments[segID], deployments...)
			s.mu.Unlock()

			out := map[string]any{"deployments": deployments, "sandbox": true}
			return makeResult(out, fmt.Sprintf("Activated %d deployments", len(deployments)))
		})

		return server
	}

	log.Fatal(adcp.Serve(createServer))
}
