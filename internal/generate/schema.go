package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

var (
	identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	goTypeRe     = regexp.MustCompile(`^[\[\]*]*(?:map\[string\])?[A-Za-z_][A-Za-z0-9_.]*$`)
	enumValueRe  = regexp.MustCompile(`^[a-z0-9_]+$`)
)

func validateIdentifier(context, value string) error {
	if !identifierRe.MatchString(value) {
		return fmt.Errorf("%s: %q is not a valid Go identifier", context, value)
	}
	return nil
}

func validateGoType(context, value string) error {
	if !goTypeRe.MatchString(value) {
		return fmt.Errorf("%s: %q is not a valid Go type expression", context, value)
	}
	return nil
}

func sanitizeComment(s string) string {
	// Strip newlines to prevent breaking out of // comments into code.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// IR types for codegen

// GoEnum represents a string enum type.
type GoEnum struct {
	Name        string
	Description string
	Values      []GoEnumValue
}

// GoEnumValue is a single enum constant.
type GoEnumValue struct {
	ConstName string
	Value     string
}

// GoStruct represents a Go struct.
type GoStruct struct {
	Name        string
	Description string
	Fields      []GoField
}

// GoField represents a struct field.
type GoField struct {
	Name      string
	Type      string
	JSONName  string
	OmitEmpty bool
	Comment   string
}

// IR is the intermediate representation of all types to generate.
type IR struct {
	Enums   []GoEnum
	Structs []GoStruct
}

// LoadSchemas reads all JSON Schema files from dir and returns an IR.
func LoadSchemas(dir string) (*IR, error) {
	ir := &IR{}

	// Load enums first (other schemas reference them).
	enumPath := filepath.Join(dir, "enums.json")
	if err := loadEnums(enumPath, ir); err != nil {
		return nil, fmt.Errorf("enums: %w", err)
	}

	// Track struct names to detect duplicates across schema files.
	seen := make(map[string]string) // name -> source file

	// Load all other schema files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "enums.json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := loadObjectSchema(path, ir, seen); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
	}

	return ir, nil
}

func loadEnums(path string, ir *IR) error {
	s, err := readSchema(path)
	if err != nil {
		return err
	}
	if s.Defs == nil {
		return fmt.Errorf("enums.json has no $defs")
	}

	// Sort enum names for deterministic output.
	names := make([]string, 0, len(s.Defs))
	for name := range s.Defs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := validateIdentifier("enum type", name); err != nil {
			return err
		}
		def := s.Defs[name]
		prefix := extraString(def, "x-go-enum-prefix")
		if prefix == "" {
			prefix = name
		}
		enumNames := extraStringSlice(def, "x-go-enum-names")

		ge := GoEnum{
			Name:        name,
			Description: sanitizeComment(def.Description),
		}
		for i, v := range def.Enum {
			sv, ok := v.(string)
			if !ok {
				continue
			}
			if !enumValueRe.MatchString(sv) {
				return fmt.Errorf("enum %s: value %q contains invalid characters", name, sv)
			}
			constName := prefix + pascalCase(sv)
			if i < len(enumNames) {
				constName = prefix + enumNames[i]
			}
			if err := validateIdentifier("enum const", constName); err != nil {
				return err
			}
			ge.Values = append(ge.Values, GoEnumValue{ConstName: constName, Value: sv})
		}
		ir.Enums = append(ir.Enums, ge)
	}
	return nil
}

func loadObjectSchema(path string, ir *IR, seen map[string]string) error {
	s, err := readSchema(path)
	if err != nil {
		return err
	}
	file := filepath.Base(path)

	// Process $defs first (inner types).
	if s.Defs != nil {
		defNames := make([]string, 0, len(s.Defs))
		for name := range s.Defs {
			defNames = append(defNames, name)
		}
		sort.Strings(defNames)

		for _, name := range defNames {
			if err := validateIdentifier("$defs struct", name); err != nil {
				return err
			}
			if prev, ok := seen[name]; ok {
				return fmt.Errorf("duplicate struct name %q (first in %s, also in %s)", name, prev, file)
			}
			seen[name] = file
			def := s.Defs[name]
			gs, err := schemaToStruct(name, def)
			if err != nil {
				return err
			}
			ir.Structs = append(ir.Structs, gs)
		}
	}

	// Process the top-level object.
	goName := extraString(s, "x-go-name")
	if goName == "" {
		return nil // skip schemas without x-go-name
	}
	if err := validateIdentifier("top-level struct", goName); err != nil {
		return err
	}
	if prev, ok := seen[goName]; ok {
		return fmt.Errorf("duplicate struct name %q (first in %s, also in %s)", goName, prev, file)
	}
	seen[goName] = file
	gs, err := schemaToStruct(goName, s)
	if err != nil {
		return err
	}
	ir.Structs = append(ir.Structs, gs)

	return nil
}

func schemaToStruct(name string, s *jsonschema.Schema) (GoStruct, error) {
	gs := GoStruct{
		Name:        name,
		Description: sanitizeComment(s.Description),
	}

	requiredSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		requiredSet[r] = true
	}

	// Sort properties for deterministic output.
	propNames := make([]string, 0, len(s.Properties))
	for pn := range s.Properties {
		propNames = append(propNames, pn)
	}
	// Use PropertyOrder if available, otherwise sort alphabetically.
	if len(s.PropertyOrder) > 0 {
		propNames = s.PropertyOrder
	} else {
		sort.Strings(propNames)
	}

	for _, jsonName := range propNames {
		prop := s.Properties[jsonName]
		if prop == nil {
			continue
		}

		fname := fieldName(jsonName, prop)
		if err := validateIdentifier(fmt.Sprintf("field %s.%s", name, jsonName), fname); err != nil {
			return GoStruct{}, err
		}

		goType := resolveType(prop)
		if extraBool(prop, "x-go-pointer") {
			goType = "*" + goType
		}
		// Validate all resolved types (covers $ref, x-go-type, and computed types).
		if err := validateGoType(fmt.Sprintf("field %s.%s type", name, jsonName), goType); err != nil {
			return GoStruct{}, err
		}

		field := GoField{
			Name:      fname,
			Type:      goType,
			JSONName:  jsonName,
			OmitEmpty: extraBool(prop, "x-go-omitempty") || !requiredSet[jsonName],
			Comment:   sanitizeComment(prop.Description),
		}

		gs.Fields = append(gs.Fields, field)
	}

	return gs, nil
}

func resolveType(s *jsonschema.Schema) string {
	// x-go-type takes precedence.
	if goType := extraString(s, "x-go-type"); goType != "" {
		return goType
	}

	// $ref to an enum or struct.
	if s.Ref != "" {
		return refToGoType(s.Ref)
	}

	switch s.Type {
	case "string":
		return "string"
	case "integer":
		return "int"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		if s.Items != nil {
			return "[]" + resolveType(s.Items)
		}
		return "[]any"
	case "object":
		if s.AdditionalProperties != nil {
			return "map[string]" + resolveType(s.AdditionalProperties)
		}
		return "map[string]any"
	default:
		return "any"
	}
}

// refToGoType extracts the Go type name from a $ref like "enums.json#/$defs/PropertyType".
func refToGoType(ref string) string {
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

func fieldName(jsonName string, s *jsonschema.Schema) string {
	if goName := extraString(s, "x-go-name"); goName != "" {
		return goName
	}
	return pascalCase(jsonName)
}

func pascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		// Common acronyms that should be fully uppercased.
		upper := strings.ToUpper(p)
		switch upper {
		case "ID", "URL", "URI", "API", "HTTP", "HTTPS", "HTML", "CSS", "JSON", "XML",
			"SQL", "RID", "IP", "UID":
			parts[i] = upper
		default:
			// Capitalize first letter, keep rest as-is.
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// Extra field helpers

func extraString(s *jsonschema.Schema, key string) string {
	if s.Extra == nil {
		return ""
	}
	v, ok := s.Extra[key]
	if !ok {
		return ""
	}
	sv, _ := v.(string)
	return sv
}

func extraBool(s *jsonschema.Schema, key string) bool {
	if s.Extra == nil {
		return false
	}
	v, ok := s.Extra[key]
	if !ok {
		return false
	}
	bv, _ := v.(bool)
	return bv
}

func extraStringSlice(s *jsonschema.Schema, key string) []string {
	if s.Extra == nil {
		return nil
	}
	v, ok := s.Extra[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if sv, ok := item.(string); ok {
				out = append(out, sv)
			}
		}
		return out
	}
	return nil
}

func readSchema(path string) (*jsonschema.Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}

	// Extract property order from raw JSON (encoding/json loses map key order).
	s.PropertyOrder = extractPropertyOrder(data, "properties")

	// Also extract property order for $defs.
	if s.Defs != nil {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("re-parse %s for property order: %w", filepath.Base(path), err)
		}
		if defsRaw, ok := raw["$defs"]; ok {
			var defs map[string]json.RawMessage
			if err := json.Unmarshal(defsRaw, &defs); err != nil {
				return nil, fmt.Errorf("parse $defs in %s: %w", filepath.Base(path), err)
			}
			for defName, defRaw := range defs {
				if def, ok := s.Defs[defName]; ok {
					def.PropertyOrder = extractPropertyOrder(defRaw, "properties")
				}
			}
		}
	}

	return &s, nil
}

// extractPropertyOrder does a lightweight parse of JSON to find the key order
// within a named object field. This preserves the declaration order from schema files.
func extractPropertyOrder(data []byte, field string) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	propsRaw, ok := raw[field]
	if !ok {
		return nil
	}

	// Use json.Decoder to get key order.
	dec := json.NewDecoder(strings.NewReader(string(propsRaw)))
	t, err := dec.Token() // opening {
	if err != nil {
		return nil
	}
	if delim, ok := t.(json.Delim); !ok || delim != '{' {
		return nil
	}

	var keys []string
	depth := 0
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		if depth == 0 {
			if key, ok := t.(string); ok {
				keys = append(keys, key)
				// Skip the value.
				var skip json.RawMessage
				if err := dec.Decode(&skip); err != nil {
					break
				}
				continue
			}
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return keys
}
