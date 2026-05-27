package adcp

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AddTool registers an MCP tool with typed input and output, using a schema
// that allows additional properties. This is the recommended way to register
// AdCP tools — it gives you compile-time type safety on both input and output
// while accepting protocol-level fields (like adcp_major_version) that the
// storyboard runner sends.
//
// Usage:
//
//	type GetProductsRequest struct {
//	    Brief   string `json:"brief,omitempty"`
//	    Account any    `json:"account,omitempty"`
//	}
//
//	adcp.AddTool(server, "get_products", "Returns available products",
//	    func(ctx context.Context, req *mcp.CallToolRequest, input GetProductsRequest) (*mcp.CallToolResult, any, error) {
//	        return adcp.ProductsResponse(&adcp.ProductsData{Products: products, Sandbox: true})
//	    })
func AddTool[In any](server *mcp.Server, name, description string, handler func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error)) {
	schema := permissiveSchemaFor[In]()

	server.AddTool(&mcp.Tool{
		InputSchema: schema,
		Name:        name,
		Description: description,
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input In
		if req.Params.Arguments != nil {
			if parseErr := json.Unmarshal(req.Params.Arguments, &input); parseErr != nil {
				// Return an MCP tool error payload, not a transport-level error.
				return makeErrResult("INVALID_INPUT", "Failed to parse input: invalid JSON or type mismatch"), nil //nolint:nilerr
			}
		}

		result, out, err := handler(ctx, req, input)
		if err != nil {
			return nil, err
		}

		// If the handler returned structured output but no StructuredContent,
		// set it from the output value (with JSON round-trip for struct tags).
		if result != nil && result.StructuredContent == nil && out != nil {
			result.StructuredContent = jsonRoundTrip(out)
		}

		return result, nil
	})
}

// permissiveSchemaFor generates a JSON schema for type T with
// additionalProperties allowed on all object types. This preserves
// property documentation and type information for tool discovery
// while accepting extra protocol fields.
func permissiveSchemaFor[In any]() *jsonschema.Schema {
	rt := reflect.TypeFor[In]()
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}

	schema, err := jsonschema.ForType(rt, &jsonschema.ForOptions{})
	if err != nil {
		// Fallback to permissive empty schema if type inference fails.
		return &jsonschema.Schema{Type: "object"}
	}

	allowAdditionalProperties(schema)
	return schema
}

// allowAdditionalProperties recursively walks a JSON schema tree and removes
// additionalProperties: false from all object types. It also removes
// properties whose schema serializes to `true` (the "any" type), since
// some validators reject bare `true` as a property schema.
func allowAdditionalProperties(s *jsonschema.Schema) {
	if s == nil {
		return
	}

	// Remove additionalProperties: false (which is encoded as {not: {}})
	if s.Type == "object" && s.AdditionalProperties != nil {
		if s.AdditionalProperties.Not != nil {
			s.AdditionalProperties = nil
		}
	}

	if isOptimizationGoalSchema(s) {
		applyOptimizationGoalSchemaOverride(s)
	}

	// Remove properties that serialize to `true` (the accept-anything schema
	// generated for `any`/`interface{}` fields). These cause validation errors
	// in some schema validators. The field is still accepted because
	// additionalProperties is not restricted. Known typed interfaces get schema
	// overrides so tool discovery does not lose protocol fields.
	for name, prop := range s.Properties {
		if isTrueSchema(prop) {
			delete(s.Properties, name)
			// Also remove from required if present
			s.Required = slices.DeleteFunc(s.Required, func(s string) bool { return s == name })
		}
	}

	// Recurse into properties
	for _, prop := range s.Properties {
		allowAdditionalProperties(prop)
	}

	// Recurse into items (array elements)
	if s.Items != nil {
		allowAdditionalProperties(s.Items)
	}

	// Recurse into schema combinators
	for _, sub := range s.AllOf {
		allowAdditionalProperties(sub)
	}
	for _, sub := range s.AnyOf {
		allowAdditionalProperties(sub)
	}
	for _, sub := range s.OneOf {
		allowAdditionalProperties(sub)
	}

	// Recurse into $defs
	for _, def := range s.Defs {
		allowAdditionalProperties(def)
	}
}

// isTrueSchema returns true if the schema serializes to the JSON value `true`,
// which is what jsonschema-go produces for Go `any`/`interface{}` fields.
func isTrueSchema(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}
	b, err := json.Marshal(s)
	if err != nil {
		return false
	}
	return string(b) == "true"
}

func isOptimizationGoalSchema(s *jsonschema.Schema) bool {
	if s == nil || s.Type != "object" || s.Properties == nil || len(s.OneOf) > 0 {
		return false
	}
	if _, ok := s.Properties["kind"]; !ok {
		return false
	}
	_, hasMetric := s.Properties["metric"]
	_, hasEventSources := s.Properties["event_sources"]
	_, hasTarget := s.Properties["target"]
	return hasTarget && hasMetric && hasEventSources
}

// applyOptimizationGoalSchemaOverride replaces the flattened Go type schema
// with the protocol's metric/event branches. Branches deliberately omit
// additionalProperties so tool inputs remain forward-compatible with Extra.
func applyOptimizationGoalSchemaOverride(s *jsonschema.Schema) {
	props := s.Properties
	s.Type = ""
	s.Properties = nil
	s.Required = nil
	s.PropertyOrder = nil
	s.OneOf = []*jsonschema.Schema{
		optimizationGoalBranchJSONSchema("metric", map[string]*jsonschema.Schema{
			"metric":                props["metric"],
			"reach_unit":            props["reach_unit"],
			"target_frequency":      props["target_frequency"],
			"view_duration_seconds": props["view_duration_seconds"],
			"target":                optimizationGoalMetricTargetJSONSchema(),
			"priority":              props["priority"],
		}, []string{"kind", "metric"}),
		optimizationGoalBranchJSONSchema("event", map[string]*jsonschema.Schema{
			"event_sources":      props["event_sources"],
			"target":             optimizationGoalEventTargetJSONSchema(),
			"attribution_window": props["attribution_window"],
			"priority":           props["priority"],
		}, []string{"kind", "event_sources"}),
	}
}

func optimizationGoalMetricTargetJSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		optimizationGoalTargetVariantSchema("cost_per", true, "Target cost per unit of the metric or conversion event."),
		optimizationGoalTargetVariantSchema("threshold_rate", true, "Minimum per-impression rate for the metric."),
	}}
}

func optimizationGoalEventTargetJSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		optimizationGoalTargetVariantSchema("cost_per", true, "Target cost per unit of the metric or conversion event."),
		optimizationGoalTargetVariantSchema("per_ad_spend", true, "Target return per unit of ad spend."),
		optimizationGoalTargetVariantSchema("maximize_value", false, "Maximize total conversion value within budget."),
	}}
}

func optimizationGoalBranchJSONSchema(kind string, properties map[string]*jsonschema.Schema, required []string) *jsonschema.Schema {
	kindValue := any(kind)
	branchProperties := make(map[string]*jsonschema.Schema, len(properties)+1)
	for name, prop := range properties {
		if prop != nil {
			branchProperties[name] = prop
		}
	}
	branchProperties["kind"] = &jsonschema.Schema{
		Type:  "string",
		Const: &kindValue,
	}
	return &jsonschema.Schema{
		Type:       "object",
		Properties: branchProperties,
		Required:   required,
	}
}

func optimizationGoalTargetVariantSchema(kind string, withValue bool, description string) *jsonschema.Schema {
	kindValue := any(kind)
	properties := map[string]*jsonschema.Schema{
		"kind": {
			Type:  "string",
			Const: &kindValue,
		},
	}
	required := []string{"kind"}
	if withValue {
		properties["value"] = &jsonschema.Schema{
			Type: "number",
		}
		required = append(required, "value")
	}
	return &jsonschema.Schema{
		Type:        "object",
		Description: description,
		Properties:  properties,
		Required:    required,
	}
}

// jsonRoundTrip marshals a value to JSON and back, ensuring struct tags
// (like json:"name") are respected in the structured content output.
func jsonRoundTrip(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func makeErrResult(code, message string) *mcp.CallToolResult {
	errData := map[string]any{"adcp_error": map[string]any{
		"code": code, "message": message, "recovery": "terminal",
	}}
	b, _ := json.Marshal(errData)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		IsError:           true,
		StructuredContent: errData,
	}
}
