package adcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testInput struct {
	Name   string  `json:"name"`
	Age    int     `json:"age,omitempty"`
	Nested *nested `json:"nested,omitempty"`
}

type nested struct {
	Value string `json:"value"`
}

func TestPermissiveSchemaFor(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	require.Equal(t, "object", schema.Type, "expected type object")

	// AdditionalProperties should be nil (permissive), not false
	require.Nil(t, schema.AdditionalProperties, "expected AdditionalProperties to be nil (permissive)")

	// Properties should still be documented
	require.NotNil(t, schema.Properties, "expected properties to be set")
	assert.Contains(t, schema.Properties, "name", "expected 'name' property")
	assert.Contains(t, schema.Properties, "age", "expected 'age' property")
}

func TestPermissiveSchemaForNested(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	// Check that nested object types also have additionalProperties removed.
	// The nested type might be in $defs or inline.
	nestedProp := schema.Properties["nested"]
	require.NotNil(t, nestedProp, "expected 'nested' property")

	// The nested schema might be a $ref. Walk $defs too.
	for _, def := range schema.Defs {
		if def.Type == "object" {
			assert.Nil(t, def.AdditionalProperties, "expected nested object $def to have nil AdditionalProperties")
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

	assert.Nil(t, schema.AdditionalProperties, "expected AdditionalProperties to be nil after patching")
	assert.NotNil(t, schema.Properties["foo"], "expected properties to be preserved")
}

func TestJsonRoundTrip(t *testing.T) {
	type sample struct {
		ProductID string `json:"product_id"`
		Name      string `json:"name"`
	}

	input := sample{ProductID: "p1", Name: "Test"}
	result := jsonRoundTrip(input)

	m, ok := result.(map[string]any)
	require.True(t, ok, "expected map[string]any, got %T", result)

	// Verify struct tags were respected (product_id not ProductID)
	assert.Contains(t, m, "product_id", "expected 'product_id' key (from json tag)")
	assert.NotContains(t, m, "ProductID", "unexpected 'ProductID' key (struct tag not applied)")
}

func TestBuildResultSetsStructuredContent(t *testing.T) {
	data := map[string]any{"foo": "bar"}
	result := buildResult("test", data)

	require.NotNil(t, result.StructuredContent, "expected StructuredContent to be set")

	sc, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok, "expected map[string]any, got %T", result.StructuredContent)
	require.Equal(t, "bar", sc["foo"], "expected foo=bar")
}

func TestPermissiveSchemaPreservesRequired(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	// "name" should be required (no omitempty), "age" should not
	assert.Contains(t, schema.Required, "name", "expected 'name' to be required")
	assert.NotContains(t, schema.Required, "age", "'age' should not be required (has omitempty)")
}

func TestPermissiveSchemaForAny(t *testing.T) {
	// any type falls back to empty object schema since jsonschema can't infer interface{}
	schema := permissiveSchemaFor[any]()
	// Should at least not panic and return a usable schema
	require.NotNil(t, schema, "expected non-nil schema")
}

func TestPermissiveSchemaSerializesToJSON(t *testing.T) {
	schema := permissiveSchemaFor[testInput]()

	b, err := json.Marshal(schema)
	require.NoError(t, err, "failed to marshal schema")

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	// Should have type: "object" and properties but NO additionalProperties
	assert.Equal(t, "object", m["type"], "expected type=object in JSON")
	assert.NotContains(t, m, "additionalProperties", "expected no additionalProperties key in JSON")
	assert.NotNil(t, m["properties"], "expected properties in JSON")
}

func TestPermissiveSchemaVsDefaultSchema(t *testing.T) {
	// Generate the default schema (which has additionalProperties: false)
	rt := reflect.TypeFor[testInput]()
	defaultSchema, err := jsonschema.ForType(rt, &jsonschema.ForOptions{})
	require.NoError(t, err)

	// Default should have additionalProperties set (false schema)
	require.NotNil(t, defaultSchema.AdditionalProperties, "expected default schema to have additionalProperties set")

	// Our permissive schema should NOT
	permissive := permissiveSchemaFor[testInput]()
	assert.Nil(t, permissive.AdditionalProperties, "expected permissive schema to have nil additionalProperties")
}

func TestPermissiveSchemaPreservesOptimizationGoalTarget(t *testing.T) {
	schema := permissiveSchemaFor[PackageInput]()

	b, err := json.Marshal(schema)
	require.NoError(t, err, "failed to marshal package input schema")
	body := string(b)

	assert.Contains(t, body, `"optimization_goals"`, "expected optimization_goals in schema")
	assert.Contains(t, body, `"target"`, "expected optimization goal target in schema")
	for _, want := range []string{
		`"const":"cost_per"`,
		`"const":"threshold_rate"`,
		`"const":"per_ad_spend"`,
		`"const":"maximize_value"`,
	} {
		assert.Contains(t, body, want, "expected target variant %s in schema", want)
	}
	assert.True(t, strings.Count(body, `"const":"threshold_rate"`) >= 1, "expected threshold_rate target variant")
}
