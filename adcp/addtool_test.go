package adcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type testInput struct {
	Name    string `json:"name"`
	Age     int    `json:"age,omitempty"`
	Nested  *nested `json:"nested,omitempty"`
}

type nested struct {
	Value string `json:"value"`
}

func TestPermissiveSchemaFor(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	if schema.Type != "object" {
		t.Fatalf("expected type object, got %s", schema.Type)
	}

	// AdditionalProperties should be nil (permissive), not false
	if schema.AdditionalProperties != nil {
		t.Fatal("expected AdditionalProperties to be nil (permissive)")
	}

	// Properties should still be documented
	if schema.Properties == nil {
		t.Fatal("expected properties to be set")
	}
	if _, ok := schema.Properties["name"]; !ok {
		t.Fatal("expected 'name' property")
	}
	if _, ok := schema.Properties["age"]; !ok {
		t.Fatal("expected 'age' property")
	}
}

func TestPermissiveSchemaForNested(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	// Check that nested object types also have additionalProperties removed.
	// The nested type might be in $defs or inline.
	nestedProp := schema.Properties["nested"]
	if nestedProp == nil {
		t.Fatal("expected 'nested' property")
	}

	// The nested schema might be a $ref. Walk $defs too.
	for _, def := range schema.Defs {
		if def.Type == "object" && def.AdditionalProperties != nil {
			t.Fatal("expected nested object $def to have nil AdditionalProperties")
		}
	}
}

func TestAllowAdditionalProperties(t *testing.T) {
	// Create a schema with additionalProperties: false (the false schema pattern)
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"foo": {Type: "string"},
		},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false schema
	}

	allowAdditionalProperties(schema)

	if schema.AdditionalProperties != nil {
		t.Fatal("expected AdditionalProperties to be nil after patching")
	}
	if schema.Properties["foo"] == nil {
		t.Fatal("expected properties to be preserved")
	}
}

func TestJsonRoundTrip(t *testing.T) {
	type sample struct {
		ProductID string `json:"product_id"`
		Name      string `json:"name"`
	}

	input := sample{ProductID: "p1", Name: "Test"}
	result := jsonRoundTrip(input)

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", result)
	}

	// Verify struct tags were respected (product_id not ProductID)
	if _, ok := m["product_id"]; !ok {
		t.Fatal("expected 'product_id' key (from json tag)")
	}
	if _, ok := m["ProductID"]; ok {
		t.Fatal("unexpected 'ProductID' key (struct tag not applied)")
	}
}

func TestBuildResultSetsStructuredContent(t *testing.T) {
	data := map[string]any{"foo": "bar"}
	result := buildResult("test", data)

	if result.StructuredContent == nil {
		t.Fatal("expected StructuredContent to be set")
	}

	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", result.StructuredContent)
	}
	if sc["foo"] != "bar" {
		t.Fatalf("expected foo=bar, got %v", sc["foo"])
	}
}

func TestPermissiveSchemaPreservesRequired(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	// "name" should be required (no omitempty), "age" should not
	found := false
	for _, r := range schema.Required {
		if r == "name" {
			found = true
		}
		if r == "age" {
			t.Fatal("'age' should not be required (has omitempty)")
		}
	}
	if !found {
		t.Fatal("expected 'name' to be required")
	}
}

func TestPermissiveSchemaForAny(t *testing.T) {
	// any type falls back to empty object schema since jsonschema can't infer interface{}
	schema := permissiveSchemaFor[any]()
	// Should at least not panic and return a usable schema
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestPermissiveSchemaSerializesToJSON(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("failed to marshal schema: %v", err)
	}

	var m map[string]any
	json.Unmarshal(b, &m)

	// Should have type: "object" and properties but NO additionalProperties
	if m["type"] != "object" {
		t.Fatalf("expected type=object in JSON, got %v", m["type"])
	}
	if _, ok := m["additionalProperties"]; ok {
		t.Fatal("expected no additionalProperties key in JSON")
	}
	if m["properties"] == nil {
		t.Fatal("expected properties in JSON")
	}
}

func TestPermissiveSchemaVsDefaultSchema(t *testing.T) {
	// Generate the default schema (which has additionalProperties: false)
	rt := reflect.TypeFor[testInput]()
	defaultSchema, err := jsonschema.ForType(rt, &jsonschema.ForOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Default should have additionalProperties set (false schema)
	if defaultSchema.AdditionalProperties == nil {
		t.Fatal("expected default schema to have additionalProperties set")
	}

	// Our permissive schema should NOT
	permissive := permissiveSchemaFor[testInput]()
	if permissive.AdditionalProperties != nil {
		t.Fatal("expected permissive schema to have nil additionalProperties")
	}
}
