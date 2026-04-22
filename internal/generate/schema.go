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
	enumValueRe  = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)
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
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	// Defense-in-depth: if generated output ever uses block comments,
	// prevent a description from closing the comment early.
	s = strings.ReplaceAll(s, "*/", "* /")
	return s
}

// Overlay types for Go-specific annotations.

// Overlay holds Go-specific overrides applied on top of upstream schemas.
type Overlay struct {
	Enums   map[string]EnumOverlay  `json:"enums"`   // keyed by Go type name (derived from title)
	Structs map[string]string       `json:"structs"` // derived name -> desired name
	Fields  map[string]FieldOverlay `json:"fields"`  // keyed by "StructName.json_field_name" (uses final name after rename)
	Refs    map[string]string       `json:"refs"`    // $ref path -> Go type
}

// EnumOverlay customizes enum const naming.
// When Values is set, the enum is synthetic (not from a file).
type EnumOverlay struct {
	Prefix      string   `json:"prefix"`
	Names       []string `json:"names"`
	Values      []string `json:"values,omitempty"`
	Description string   `json:"description,omitempty"`
}

// FieldOverlay customizes a struct field.
type FieldOverlay struct {
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Pointer   bool   `json:"pointer,omitempty"`
	OmitEmpty *bool  `json:"omit_empty,omitempty"`
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

// enumRegistry maps $ref paths (e.g., "/schemas/enums/property-type.json") to Go type names.
type enumRegistry map[string]string

// structRegistry maps $ref paths (e.g., "/schemas/tmp/offer.json") to Go type names.
type structRegistry map[string]string

// loadContext holds state shared across loading functions.
type loadContext struct {
	overlay    *Overlay
	enumReg    enumRegistry
	structReg  structRegistry
	seen       map[string]string // struct name -> source file (for duplicate detection)
}

// LoadSchemas reads JSON Schema files from schemaDir, enum files from enumDir,
// and applies overlay overrides. enumDir and overlayPath may be empty.
func LoadSchemas(schemaDir, enumDir, overlayPath string) (*IR, error) {
	ir := &IR{}

	// Load overlay.
	var overlay *Overlay
	if overlayPath != "" {
		var err error
		overlay, err = loadOverlay(overlayPath)
		if err != nil {
			return nil, fmt.Errorf("overlay: %w", err)
		}
	}

	ctx := &loadContext{
		overlay:   overlay,
		enumReg:   make(enumRegistry),
		structReg: make(structRegistry),
		seen:      make(map[string]string),
	}

	// Load enums.
	if enumDir != "" {
		if err := loadEnumsDir(enumDir, overlay, ir, ctx); err != nil {
			return nil, fmt.Errorf("enums: %w", err)
		}
	} else {
		// Legacy: look for enums.json in schemaDir.
		enumPath := filepath.Join(schemaDir, "enums.json")
		if _, err := os.Stat(enumPath); err == nil {
			if err := loadEnumsLegacy(enumPath, ir); err != nil {
				return nil, fmt.Errorf("enums: %w", err)
			}
		}
	}

	// Build struct registry from schema filenames before loading.
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "enums.json" {
			continue
		}
		path := filepath.Join(schemaDir, e.Name())
		s, err := readSchema(path)
		if err != nil {
			continue // will be caught during full load
		}
		goName := deriveStructName(s, e.Name())
		if goName != "" {
			// Apply struct rename from overlay so $refs resolve to the final name.
			if overlay != nil {
				if renamed, ok := overlay.Structs[goName]; ok {
					goName = renamed
				}
			}
			// Infer the $id or construct ref path from filename.
			refPath := s.ID
			if refPath == "" {
				refPath = "/schemas/tmp/" + e.Name()
			}
			ctx.structReg[refPath] = goName
		}
	}

	// Load all schema files.
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "enums.json" {
			continue
		}
		path := filepath.Join(schemaDir, e.Name())
		if err := loadObjectSchema(path, ir, ctx); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
	}

	// Generate synthetic enums from overlay (enums with inline values, not from files).
	if overlay != nil {
		for name, eo := range overlay.Enums {
			if len(eo.Values) == 0 {
				continue // file-backed enum, already loaded
			}
			prefix := eo.Prefix
			if prefix == "" {
				prefix = name
			}
			ge := GoEnum{Name: name, Description: sanitizeComment(eo.Description)}
			for i, v := range eo.Values {
				constName := prefix + pascalCase(v)
				if i < len(eo.Names) {
					constName = prefix + eo.Names[i]
				}
				ge.Values = append(ge.Values, GoEnumValue{ConstName: constName, Value: v})
			}
			ir.Enums = append(ir.Enums, ge)
		}
		// Sort enums by name for deterministic output.
		sort.Slice(ir.Enums, func(i, j int) bool { return ir.Enums[i].Name < ir.Enums[j].Name })
	}

	// Apply struct renames from overlay.
	if overlay != nil && len(overlay.Structs) > 0 {
		for i, s := range ir.Structs {
			if newName, ok := overlay.Structs[s.Name]; ok {
				ir.Structs[i].Name = newName
			}
		}
	}

	return ir, nil
}

func loadOverlay(path string) (*Overlay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var o Overlay
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse overlay: %w", err)
	}
	return &o, nil
}

// loadEnumsDir reads individual enum JSON files from dir.
// Only enums listed in the overlay are included.
func loadEnumsDir(dir string, overlay *Overlay, ir *IR, ctx *loadContext) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	// Build a map of all enum files by their $id or derived ref path.
	type enumFile struct {
		path   string
		schema *jsonschema.Schema
		refID  string // the $id or constructed ref path
	}
	var files []enumFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := readSchema(path)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		refID := s.ID
		if refID == "" {
			refID = "/schemas/enums/" + e.Name()
		}
		files = append(files, enumFile{path: path, schema: s, refID: refID})
	}

	// Derive Go type names for all enum files and populate the registry.
	// This allows $ref resolution even for enums we don't generate.
	for _, f := range files {
		goName := titleToPascalCase(f.schema.Title)
		if goName == "" {
			goName = filenameToPascalCase(filepath.Base(f.path))
		}
		ctx.enumReg[f.refID] = goName
	}

	// Only generate enums listed in the overlay (if overlay is provided).
	// If no overlay, generate all enums.
	overlayEnumNames := make(map[string]bool)
	if overlay != nil && len(overlay.Enums) > 0 {
		for name := range overlay.Enums {
			overlayEnumNames[name] = true
		}
	}

	// Sort for deterministic output.
	sort.Slice(files, func(i, j int) bool {
		ni := ctx.enumReg[files[i].refID]
		nj := ctx.enumReg[files[j].refID]
		return ni < nj
	})

	for _, f := range files {
		goName := ctx.enumReg[f.refID]
		if goName == "" {
			continue
		}

		// If overlay specifies which enums to generate, only include those.
		if len(overlayEnumNames) > 0 && !overlayEnumNames[goName] {
			continue
		}
		// Skip file-based generation for synthetic enums (defined with Values in overlay).
		if overlay != nil {
			if eo, ok := overlay.Enums[goName]; ok && len(eo.Values) > 0 {
				continue
			}
		}

		if err := validateIdentifier("enum type", goName); err != nil {
			return err
		}

		var eo *EnumOverlay
		if overlay != nil {
			if v, ok := overlay.Enums[goName]; ok {
				eo = &v
			}
		}

		ge, err := buildEnum(goName, f.schema, eo)
		if err != nil {
			return err
		}
		ir.Enums = append(ir.Enums, ge)
	}

	return nil
}

func buildEnum(name string, s *jsonschema.Schema, eo *EnumOverlay) (GoEnum, error) {
	prefix := name
	if eo != nil && eo.Prefix != "" {
		prefix = eo.Prefix
	}

	// Enforce positional names match schema length, so upstream insertions
	// produce a clear failure instead of silently mislabeling constants.
	if eo != nil && len(eo.Names) > 0 && len(eo.Names) != len(s.Enum) {
		return GoEnum{}, fmt.Errorf("enum %s: overlay names length %d != schema enum length %d — update overlay for upstream schema change",
			name, len(eo.Names), len(s.Enum))
	}

	ge := GoEnum{
		Name:        name,
		Description: sanitizeComment(s.Description),
	}

	for i, v := range s.Enum {
		sv, ok := v.(string)
		if !ok {
			continue
		}
		if !enumValueRe.MatchString(sv) {
			return GoEnum{}, fmt.Errorf("enum %s: value %q contains invalid characters", name, sv)
		}

		constName := prefix + pascalCase(sv)
		if eo != nil && i < len(eo.Names) {
			constName = prefix + eo.Names[i]
		}
		if err := validateIdentifier("enum const", constName); err != nil {
			return GoEnum{}, err
		}
		ge.Values = append(ge.Values, GoEnumValue{ConstName: constName, Value: sv})
	}

	return ge, nil
}

// loadEnumsLegacy loads enums from a single enums.json with $defs (backward compat).
func loadEnumsLegacy(path string, ir *IR) error {
	s, err := readSchema(path)
	if err != nil {
		return err
	}
	if s.Defs == nil {
		return fmt.Errorf("enums.json has no $defs")
	}

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

// deriveStructName returns the Go struct name for a schema, using x-go-name, title, or filename.
func deriveStructName(s *jsonschema.Schema, filename string) string {
	if goName := extraString(s, "x-go-name"); goName != "" {
		return goName
	}
	if s.Title != "" {
		return titleToPascalCase(s.Title)
	}
	stem := strings.TrimSuffix(filename, ".json")
	return filenameToPascalCase(stem)
}

func loadObjectSchema(path string, ir *IR, ctx *loadContext) error {
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
			if prev, ok := ctx.seen[name]; ok {
				return fmt.Errorf("duplicate struct name %q (first in %s, also in %s)", name, prev, file)
			}
			ctx.seen[name] = file
			def := s.Defs[name]
			gs, err := schemaToStruct(name, def, ctx)
			if err != nil {
				return err
			}
			ir.Structs = append(ir.Structs, gs)
		}
	}

	// Derive the top-level struct name.
	goName := deriveStructName(s, file)
	if goName == "" {
		return nil
	}
	if err := validateIdentifier("top-level struct", goName); err != nil {
		return err
	}
	if prev, ok := ctx.seen[goName]; ok {
		return fmt.Errorf("duplicate struct name %q (first in %s, also in %s)", goName, prev, file)
	}
	ctx.seen[goName] = file

	gs, err := schemaToStruct(goName, s, ctx)
	if err != nil {
		return err
	}
	ir.Structs = append(ir.Structs, gs)

	return nil
}

func schemaToStruct(name string, s *jsonschema.Schema, ctx *loadContext) (GoStruct, error) {
	gs := GoStruct{
		Name:        name,
		Description: sanitizeComment(s.Description),
	}

	// For overlay field lookups, use the final struct name (after rename).
	overlayName := name
	if ctx.overlay != nil {
		if renamed, ok := ctx.overlay.Structs[name]; ok {
			overlayName = renamed
		}
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

		// Skip $schema property — it's a JSON Schema meta-field, not data.
		if jsonName == "$schema" {
			continue
		}

		fname := fieldName(jsonName, prop)

		// Apply overlay field name override.
		overlayKey := overlayName + "." + jsonName
		if ctx.overlay != nil {
			if fo, ok := ctx.overlay.Fields[overlayKey]; ok {
				if fo.Name != "" {
					fname = fo.Name
				}
			}
		}

		if err := validateIdentifier(fmt.Sprintf("field %s.%s", name, jsonName), fname); err != nil {
			return GoStruct{}, err
		}

		goType := resolveType(prop, ctx)

		// Apply overlay field type override.
		if ctx.overlay != nil {
			if fo, ok := ctx.overlay.Fields[overlayKey]; ok {
				if fo.Type != "" {
					goType = fo.Type
				}
				if fo.Pointer {
					goType = "*" + goType
				}
			}
		}

		// Legacy x-go-pointer support.
		if extraBool(prop, "x-go-pointer") {
			goType = "*" + goType
		}

		if err := validateGoType(fmt.Sprintf("field %s.%s type", name, jsonName), goType); err != nil {
			return GoStruct{}, err
		}

		omitEmpty := !requiredSet[jsonName]
		if extraBool(prop, "x-go-omitempty") {
			omitEmpty = true
		}

		// Apply overlay omit_empty override.
		if ctx.overlay != nil {
			if fo, ok := ctx.overlay.Fields[overlayKey]; ok {
				if fo.OmitEmpty != nil {
					omitEmpty = *fo.OmitEmpty
				}
			}
		}

		field := GoField{
			Name:      fname,
			Type:      goType,
			JSONName:  jsonName,
			OmitEmpty: omitEmpty,
			Comment:   sanitizeComment(prop.Description),
		}

		gs.Fields = append(gs.Fields, field)
	}

	return gs, nil
}

func resolveType(s *jsonschema.Schema, ctx *loadContext) string {
	// x-go-type takes precedence.
	if goType := extraString(s, "x-go-type"); goType != "" {
		return goType
	}

	// $ref to an enum, struct, or external type.
	if s.Ref != "" {
		return refToGoType(s.Ref, ctx)
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
			return "[]" + resolveType(s.Items, ctx)
		}
		return "[]any"
	case "object":
		if s.AdditionalProperties != nil {
			return "map[string]" + resolveType(s.AdditionalProperties, ctx)
		}
		return "map[string]any"
	default:
		return "any"
	}
}

// schemaVersionSegmentRe matches /schemas/<version>/ where version is either
// "latest" or a semver like "3.0.0" / "3.0.0-rc.1". Used to normalize $ref
// paths so a version-agnostic overlay entry works across pinned and snapshot
// bundles.
var schemaVersionSegmentRe = regexp.MustCompile(`^/schemas/(?:latest|\d+\.\d+\.\d+(?:-[A-Za-z0-9.]+)?)/`)

// canonicalizeRef normalizes a schema $ref path to the /schemas/latest/ form
// so overlay lookups work regardless of which version the bundled schemas
// advertise in their $id.
func canonicalizeRef(ref string) string {
	return schemaVersionSegmentRe.ReplaceAllString(ref, "/schemas/latest/")
}

// refToGoType resolves a $ref path to a Go type name.
func refToGoType(ref string, ctx *loadContext) string {
	// Check overlay refs first. Overlay keys are version-agnostic (canonicalized
	// to /schemas/latest/...) so a pinned-version bundle ($id=/schemas/3.0.0/...)
	// resolves the same entry as a latest-snapshot bundle.
	if ctx.overlay != nil {
		if goType, ok := ctx.overlay.Refs[ref]; ok {
			return goType
		}
		if goType, ok := ctx.overlay.Refs[canonicalizeRef(ref)]; ok {
			return goType
		}
	}

	// Check enum registry.
	if goType, ok := ctx.enumReg[ref]; ok {
		return goType
	}

	// Check struct registry.
	if goType, ok := ctx.structReg[ref]; ok {
		return goType
	}

	// Legacy: "enums.json#/$defs/PropertyType" -> extract last segment.
	if strings.Contains(ref, "#/") {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}

	// Fall back: last path segment, strip .json, convert to PascalCase.
	base := filepath.Base(ref)
	stem := strings.TrimSuffix(base, ".json")
	return filenameToPascalCase(stem)
}

func fieldName(jsonName string, s *jsonschema.Schema) string {
	if goName := extraString(s, "x-go-name"); goName != "" {
		return goName
	}
	return pascalCase(jsonName)
}

// wordSplitRe splits on underscores, hyphens, dots, and slashes.
var wordSplitRe = regexp.MustCompile(`[_\-./]+`)

func pascalCase(s string) string {
	parts := wordSplitRe.Split(s, -1)
	for i, p := range parts {
		if p == "" {
			continue
		}
		upper := strings.ToUpper(p)
		switch upper {
		case "ID", "URL", "URI", "API", "HTTP", "HTTPS", "HTML", "CSS", "JSON", "XML",
			"SQL", "RID", "IP", "UID", "TTL", "KVS":
			parts[i] = upper
		case "IDS":
			parts[i] = "IDs"
		default:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// titleToPascalCase converts a title like "Context Match Request" to "ContextMatchRequest".
// Splits on whitespace and hyphens.
func titleToPascalCase(title string) string {
	if title == "" {
		return ""
	}
	// Split on whitespace first, then split each word on hyphens.
	spaceWords := strings.Fields(title)
	var words []string
	for _, sw := range spaceWords {
		parts := strings.Split(sw, "-")
		words = append(words, parts...)
	}
	for i, w := range words {
		if w == "" {
			continue
		}
		upper := strings.ToUpper(w)
		switch upper {
		case "ID", "URL", "URI", "API", "HTTP", "HTTPS", "HTML", "CSS", "JSON", "XML",
			"SQL", "RID", "IP", "UID", "TTL":
			words[i] = upper
		default:
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, "")
}

// filenameToPascalCase converts "context-match-request" to "ContextMatchRequest".
func filenameToPascalCase(stem string) string {
	parts := strings.Split(stem, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		upper := strings.ToUpper(p)
		switch upper {
		case "ID", "URL", "URI", "API", "HTTP", "HTTPS", "HTML", "CSS", "JSON", "XML",
			"SQL", "RID", "IP", "UID", "TMP", "TTL":
			parts[i] = upper
		default:
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
