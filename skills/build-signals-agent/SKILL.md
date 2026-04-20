---
name: build-signals-agent
description: Use when building an AdCP signals agent in Go — a CDP, data provider, or audience data server that serves targeting segments to buyers.
---

# Build a Signals Agent (Go)

## Overview

A signals agent serves audience segments to buyers for campaign targeting. Two core tools: `get_signals` (discovery) and `activate_signal` (push to DSPs or sales agents).

## When to Use

- User wants to build an agent that serves audience/targeting data in Go
- User mentions signals, segments, audiences, data provider, or CDP

**Not this skill:** selling inventory → `skills/build-seller-agent/`, creative rendering → `skills/build-creative-agent/`

## Before Writing Code

Ask the user — don't guess.

1. **Marketplace or owned?** Marketplace = third-party data (`signal_type: "marketplace"`, `signal_id.source: "catalog"`, `signal_id.data_provider_domain`). Owned = first-party data (`signal_type: "owned"`, `signal_id.source: "agent"`, `signal_id.agent_url`).
2. **What segments?** 3-5 with variety. Each needs: name, description, coverage_percentage (5-30%), value_type (binary/categorical/numeric).
3. **Pricing.** At least one per signal. Five variants: `cpm`, `percent_of_media` (with optional `max_cpm` ceiling), `flat_fee`, `per_unit` (needs `Unit` + `UnitPrice`), `custom` (escape hatch — needs `Description` + `Metadata` with a `summary_for_operator` key; buyers route custom pricing through operator review rather than auto-selecting, so use it only for models the four enumerated forms can't express).
4. **Activation destinations.** Platform (`type: "platform"`, returns `segment_id`) or agent (`type: "agent"`, returns `key_value`).
5. **Property list change notifications?** Signals agents that publish resolved property lists SHOULD emit `adcp.PropertyListChangedWebhook` when the list changes — see `skills/build-webhook-publisher/` for the signed-delivery pattern.

## Tool Registration

Use `adcp.AddTool` for all tools.

```go
adcp.AddTool(server, "tool_name", "Description",
    func(ctx context.Context, req *mcp.CallToolRequest, input InputType) (*mcp.CallToolResult, any, error) {
        return adcp.Result(data, "summary")
    })
```

## Tools and Response Shapes

### 1. `get_adcp_capabilities`

```go
adcp.AddTool(server, "get_adcp_capabilities", "Returns agent capabilities",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
        return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
            ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
            SupportedProtocols: []string{"signals"},
        })
    })
```

### 2. `get_signals`

Input: `signal_spec` (natural language, optional), `signal_ids` (exact lookup, optional), `filters` (max_cpm, min_coverage_percentage), `max_results`.

If `signal_spec` doesn't match any signals, return ALL signals (it's a hint, not a strict filter).

Each signal must include: `signal_agent_segment_id`, `name`, `description`, `signal_type`, `data_provider`, `coverage_percentage`, `deployments` (empty `[]` until activated), `pricing_options`, `signal_id`, `value_type`.

```go
adcp.AddTool(server, "get_signals", "Discover audience signals",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetSignalsInput) (*mcp.CallToolResult, any, error) {
        matched := filterSignals(input) // your filtering logic
        return adcp.Result(map[string]any{"signals": matched, "sandbox": true},
            fmt.Sprintf("Found %d signals", len(matched)))
    })
```

Response JSON:
```json
{
  "signals": [
    {
      "signal_agent_segment_id": "seg-auto-intenders",
      "name": "Auto Intenders",
      "description": "Users researching vehicle purchases",
      "signal_type": "owned",
      "data_provider": "DataCo Audiences",
      "coverage_percentage": 18.5,
      "deployments": [],
      "pricing_options": [
        {"pricing_option_id": "po-cpm", "model": "cpm", "cpm": 2.50, "currency": "USD"}
      ],
      "signal_id": {"source": "agent", "agent_url": "http://localhost:3001/mcp", "id": "auto-intenders"},
      "value_type": "binary"
    }
  ],
  "sandbox": true
}
```

### 3. `activate_signal`

Input: `signal_agent_segment_id`, `pricing_option_id`, `destinations[]` with `type`, `platform`/`agent_url`, `account`.

Return deployments matching the destination type. Store activations so future `get_signals` returns non-empty deployments.

```go
adcp.AddTool(server, "activate_signal", "Activate a signal to a destination",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ActivateSignalInput) (*mcp.CallToolResult, any, error) {
        // Find signal by input.SignalAgentSegmentID
        // For each destination, create a deployment:
        //   platform → ActivationKey{Type: "segment_id", SegmentID: "..."}
        //   agent    → ActivationKey{Type: "key_value", Key: "...", Value: "..."}
        return adcp.Result(map[string]any{"deployments": deployments, "sandbox": true},
            fmt.Sprintf("Activated %d deployments", len(deployments)))
    })
```

Response JSON for platform activation:
```json
{
  "deployments": [
    {
      "type": "platform",
      "platform": "dv360",
      "is_live": true,
      "activation_key": {"type": "segment_id", "segment_id": "plat-dv360-auto-intenders"}
    }
  ],
  "sandbox": true
}
```

Response JSON for agent activation:
```json
{
  "deployments": [
    {
      "type": "agent",
      "agent_url": "https://sales.example.com/mcp",
      "is_live": true,
      "activation_key": {"type": "key_value", "key": "adcp_segment", "value": "auto-intenders"}
    }
  ],
  "sandbox": true
}
```

## Signal Definition

```go
var signals = []adcp.Signal{
    {
        SignalAgentSegmentID: "seg-auto-intenders",
        Name: "Auto Intenders",
        Description: "Users actively researching vehicle purchases",
        SignalType: "owned",
        DataProvider: "DataCo Audiences",
        CoveragePercentage: 18.5,
        Deployments: []adcp.Deployment{}, // empty until activated
        PricingOptions: []adcp.SignalPricing{
            {PricingOptionID: "po-auto-cpm", Model: "cpm", CPM: 2.50, Currency: "USD"},
        },
        SignalID: adcp.SignalID{Source: "agent", AgentURL: agentURL, ID: "auto-intenders"},
        ValueType: "binary",
    },
    // ... more signals
}
```

## Complete Skeleton

```go
package main

import (
    "context"
    "fmt"
    "log"
    "strings"
    "sync"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentURL = "http://localhost:3001/mcp"

type store struct {
    mu          sync.RWMutex
    deployments map[string][]adcp.Deployment // segmentID -> deployments
}

var signals = []adcp.Signal{ /* define 3-5 signals */ }

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-signals", Version: "1.0.0"}, nil)
    // Register get_adcp_capabilities, get_signals, activate_signal
    return server
}

func main() {
    s := &store{deployments: make(map[string][]adcp.Deployment)}
    log.Fatal(adcp.Serve(func() *mcp.Server { return createServer(s) }))
}
```

## go.mod

```
module your-signals-agent

go 1.25

require (
    github.com/adcontextprotocol/adcp-go/adcp v0.0.0
    github.com/modelcontextprotocol/go-sdk v1.5.0
)
```

Then `go mod tidy`.

## Validation

```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp signal_owned --json      # for owned
npx @adcp/client storyboard run http://localhost:3001/mcp signal_marketplace --json # for marketplace
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Using `mcp.AddTool` directly | Use `adcp.AddTool` — generates permissive schemas |
| Missing `signal_agent_segment_id` | Required for activation |
| Wrong `signal_id` shape | Owned: `source: "agent"`. Marketplace: `source: "catalog"` |
| Empty `pricing_options` | Must have at least one per signal |
| `is_live: true` in initial `get_signals` | Signals aren't live until activated — use empty `deployments: []` |
| `signal_spec` returns 0 results | If no match, return all signals (spec is a hint) |
| Activation doesn't match destination type | Platform → `segment_id` key, agent → `key_value` key |

## SDK Reference

```go
import (
    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

| Function | Usage |
|----------|-------|
| `adcp.AddTool(server, name, desc, handler)` | Register tool |
| `adcp.Serve(createAgent)` | HTTP server on `:3001/mcp` |
| `adcp.CapabilitiesResponse(data)` | Capabilities builder |
| `adcp.Result(data, summary)` | Generic response builder |
| `adcp.Errorf(code, opts)` | Error response |

Input types: `adcp.EmptyInput`, `adcp.GetSignalsInput`, `adcp.ActivateSignalInput`

The skill contains everything you need. Do not read additional docs before writing code.
