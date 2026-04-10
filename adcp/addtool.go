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
//	type GetProductsInput struct {
//	    Brief   string `json:"brief,omitempty"`
//	    Account any    `json:"account,omitempty"`
//	}
//
//	adcp.AddTool(server, "get_products", "Returns available products",
//	    func(ctx context.Context, req *mcp.CallToolRequest, input GetProductsInput) (*mcp.CallToolResult, any, error) {
//	        return adcp.ProductsResult(&adcp.ProductsData{Products: products, Sandbox: true})
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
			if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
				return makeErrResult("INVALID_INPUT", "Failed to parse input: invalid JSON or type mismatch"), nil
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

	// Remove properties that serialize to `true` (the accept-anything schema
	// generated for `any`/`interface{}` fields). These cause validation errors
	// in some schema validators. The field is still accepted because
	// additionalProperties is not restricted.
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

// jsonRoundTrip marshals a value to JSON and back, ensuring struct tags
// (like json:"name") are respected in the structured content output.
func jsonRoundTrip(v any) any {
	b, _ := json.Marshal(v)
	var m any
	json.Unmarshal(b, &m)
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
