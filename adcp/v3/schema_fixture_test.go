package adcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Wire-fixture validation (adcontextprotocol/adcp-go#564, check 1).
//
// A JSON fixture that stands for a request or response on the wire is checked
// against its schema in the downloaded bundle, so a fixture the Go type
// accepts but the protocol does not fails the test that uses it:
//
//	assertSchemaValid(t, "creative/sync-creatives-request.json", raw)
//	assertSchemaValid(t, "creative/sync-creatives-request.json#/properties/creatives/items", raw)
//
// The reference is a path under adcp/v3/schemas, optionally followed by a JSON
// Pointer into that file. Schemas are read from disk only: every $ref resolves
// through the bundle's own $id URLs, never the network.

const (
	schemaBundleDir = "schemas"
	draft07         = "http://json-schema.org/draft-07/schema#"
)

// errSchemaBundleMissing reports that adcp/v3/schemas/download.sh has not run.
var errSchemaBundleMissing = errors.New("schemas not downloaded; run adcp/v3/schemas/download.sh")

// assertSchemaValid fails t when raw is not valid against ref. Without a
// downloaded bundle it only logs, so the caller's other assertions still run,
// as the other tests that read schemas/ skip rather than fail; CI downloads
// the bundle before go test.
func assertSchemaValid(t testing.TB, ref string, raw []byte) {
	t.Helper()
	err := validateAgainstSchema(ref, raw)
	if errors.Is(err, errSchemaBundleMissing) {
		t.Logf("%s: not validated: %v", ref, err)
		return
	}
	if err != nil {
		t.Errorf("fixture is not valid against %s:\n%v", ref, err)
	}
}

// requireSchemaBundle skips a test that is only about the bundle.
func requireSchemaBundle(t testing.TB) *schemaBundle {
	t.Helper()
	b, err := loadSchemaBundle()
	if errors.Is(err, errSchemaBundleMissing) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	return b
}

func validateAgainstSchema(ref string, raw []byte) error {
	b, err := loadSchemaBundle()
	if err != nil {
		return err
	}
	rs, err := b.resolve(ref)
	if err != nil {
		return err
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return fmt.Errorf("fixture is not JSON: %w", err)
	}

	return rs.Validate(instance)
}

type schemaBundle struct {
	base string // https://adcontextprotocol.org/schemas/<version>/, from index.json

	mu       sync.Mutex
	docs     map[string]*jsonschema.Schema
	resolved map[string]*jsonschema.Resolved
}

var loadSchemaBundle = sync.OnceValues(func() (*schemaBundle, error) {
	raw, err := os.ReadFile(filepath.Join(schemaBundleDir, "index.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errSchemaBundleMissing
	}
	if err != nil {
		return nil, err
	}
	// The registry's own $id names the base every other $id sits under.
	var index struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("schemas/index.json: %w", err)
	}
	base, ok := strings.CutSuffix(index.ID, "index.json")
	if !ok || !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("schemas/index.json: unexpected $id %q", index.ID)
	}

	return &schemaBundle{
		base:     base,
		docs:     map[string]*jsonschema.Schema{},
		resolved: map[string]*jsonschema.Resolved{},
	}, nil
})

// resolve returns the schema at ref ("<file>" or "<file>#<JSON Pointer>").
func (b *schemaBundle) resolve(ref string) (*jsonschema.Resolved, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rs, ok := b.resolved[ref]; ok {
		return rs, nil
	}
	file, _, _ := strings.Cut(ref, "#")
	if _, err := os.Stat(filepath.Join(schemaBundleDir, filepath.FromSlash(file))); err != nil {
		return nil, fmt.Errorf("schema %s: %w", ref, err)
	}
	// A one-keyword root whose $ref names the target; the loader below
	// serves every document it reaches, so the target's own $refs resolve
	// the same way.
	root := &jsonschema.Schema{Schema: draft07, Ref: b.base + ref}
	rs, err := root.Resolve(&jsonschema.ResolveOptions{BaseURI: b.base, Loader: b.load})
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}
	b.resolved[ref] = rs

	return rs, nil
}

// load serves a bundle document by its $id, after re2Translations. A parsed
// document is shared by every later Resolve, which jsonschema-go allows: it
// keeps what it computes in the Resolved, not in the Schema.
func (b *schemaBundle) load(u *url.URL) (*jsonschema.Schema, error) {
	rel, ok := strings.CutPrefix(u.String(), b.base)
	if !ok {
		return nil, fmt.Errorf("%s is outside the %s bundle; the fixture helper never fetches", u, b.base)
	}
	if s, ok := b.docs[rel]; ok {
		return s, nil
	}
	data, err := readTranslatedSchema(rel)
	if err != nil {
		return nil, err
	}
	s := &jsonschema.Schema{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if s.Schema == "" {
		// Resolve would otherwise set it on the shared document.
		s.Schema = draft07
	}
	b.docs[rel] = s

	return s, nil
}

func readTranslatedSchema(rel string) ([]byte, error) {
	// #nosec G304 G703 -- rel is a $id path under the bundle base URI, read from adcp/v3/schemas.
	raw, err := os.ReadFile(filepath.Join(schemaBundleDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	// UseNumber keeps numbers as written through the re-encoding below.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	var errs []error
	walkSchemaPatterns(doc, "", func(obj map[string]any, ptr string) {
		p, _ := obj["pattern"].(string)
		at := rel + "#" + ptr + "/pattern"
		if _, err := regexp.Compile(p); err == nil {
			return
		}
		tr, ok := re2TranslationAt(at)
		if !ok || tr.Original != p {
			errs = append(errs, fmt.Errorf("%s: pattern %q does not compile as Go RE2 and has no entry in re2Translations", at, p))
			return
		}
		if tr.RE2 == "" {
			delete(obj, "pattern")
			return
		}
		obj["pattern"] = tr.RE2
	}, func(ptr, key string) {
		errs = append(errs, fmt.Errorf("%s#%s: patternProperties key %q does not compile as Go RE2; re2Translations covers pattern keywords only", rel, ptr, key))
	})
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return json.Marshal(doc)
}

// walkSchemaPatterns visits every subschema of doc that carries a pattern
// keyword, and reports each patternProperties key that is not valid RE2.
// It follows the keywords that hold subschemas in draft-07 and in 2020-12, as
// jsonschema-go compiles both, so a "pattern" key inside examples, const,
// enum or default data is never taken for the keyword. A subschema this walk missed still reaches Resolve, which
// fails on an invalid pattern rather than skipping it.
func walkSchemaPatterns(node any, ptr string, onPattern func(obj map[string]any, ptr string), onBadKey func(ptr, key string)) {
	obj, ok := node.(map[string]any)
	if !ok {
		return
	}
	if _, ok := obj["pattern"].(string); ok {
		onPattern(obj, ptr)
	}
	for _, kw := range sortedKeys(obj) {
		v := obj[kw]
		at := ptr + "/" + escapeJSONPointer(kw)
		switch kw {
		case "properties", "patternProperties", "definitions", "$defs", "dependencies", "dependentSchemas":
			m, _ := v.(map[string]any)
			for _, name := range sortedKeys(m) {
				if kw == "patternProperties" {
					if _, err := regexp.Compile(name); err != nil {
						onBadKey(ptr, name)
					}
				}
				walkSchemaPatterns(m[name], at+"/"+escapeJSONPointer(name), onPattern, onBadKey)
			}
		case "items", "allOf", "anyOf", "oneOf", "prefixItems":
			if list, ok := v.([]any); ok {
				for i, item := range list {
					walkSchemaPatterns(item, fmt.Sprintf("%s/%d", at, i), onPattern, onBadKey)
				}
			} else {
				walkSchemaPatterns(v, at, onPattern, onBadKey)
			}
		case "additionalItems", "additionalProperties", "contains", "propertyNames", "if", "then", "else", "not",
			"unevaluatedItems", "unevaluatedProperties", "contentSchema":
			walkSchemaPatterns(v, at, onPattern, onBadKey)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}

func escapeJSONPointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// re2Translation replaces one bundle pattern that Go's regexp (RE2) cannot
// compile with an RE2 pattern that accepts exactly the same strings under
// ECMA-262 semantics, which JSON Schema patterns use. It applies only at the
// locations listed in At; the same pattern anywhere else fails until it is
// listed. TestRE2TranslationsMatchBundle fails when an entry goes stale.
//
// Where no RE2 equivalent exists, RE2 is left empty: the pattern keyword
// alone is dropped at those locations, the rest of the schema still applies,
// and Reason says what goes unchecked. Every entry today is exact.
type re2Translation struct {
	Original string
	RE2      string // "" drops the keyword; see above
	Reason   string
	At       []string // "<file>#<JSON Pointer to the pattern keyword>"
	// Probes: strings the original pattern accepts (Match) and rejects
	// (NoMatch) in an ECMA-262 engine, asserted here against RE2.
	Match   []string
	NoMatch []string
}

// ECMA-262's \s, used inside a negated class below; RE2's \s is ASCII only.
const ecmaWhitespaceOutsideASCII = `\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

// ISO 8601 duration building blocks: a non-empty date part and a non-empty
// time part, each component in order.
const (
	isoDurationDate = `(?:\d+Y(?:\d+M)?(?:\d+D)?|\d+M(?:\d+D)?|\d+D)`
	isoDurationTime = `T(?:\d+H(?:\d+M)?(?:\d+S)?|\d+M(?:\d+S)?|\d+S)`
	nonZeroInt      = `\d*[1-9]\d*`
)

var re2Translations = []re2Translation{
	{
		Original: "^https?://(?:[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?|\\[[0-9A-Fa-f:.]+\\])(?::[0-9]{1,5})?(?:[/?#](?:[^%\\s\\u0000-\\u001F\\u007F\"<>`\\\\]|%[0-9A-Fa-f]{2}|%%[A-Za-z0-9_.:-]+%%)*)?$",
		RE2:      "^https?://(?:[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?|\\[[0-9A-Fa-f:.]+\\])(?::[0-9]{1,5})?(?:[/?#](?:[^%\\x00-\\x1F\\x7F \"<>`\\\\" + ecmaWhitespaceOutsideASCII + "]|%[0-9A-Fa-f]{2}|%%[A-Za-z0-9_.:-]+%%)*)?$",
		Reason:   `\uXXXX escapes are written \xXX; \s is spelled out as ECMA-262 whitespace (space here, the rest of ASCII via \x00-\x1F).`,
		At:       []string{"core/macro-bearing-url.json#/anyOf/2/pattern"},
		Match:    []string{"https://ads.example/click?u=%%CLICK_URL%%", "http://[::1]:8080/a%20b", "https://x.example"},
		NoMatch:  []string{"https://ads.example/a b", "https://ads.example/a\u00a0b", "https://ads.example/a\tb", "https://ads.example/%zz", "ftp://x.example/", "https://ads.example/a\ufeffb"},
	},
	{
		Original: `^https://(?![^/]*@)(?!localhost(?:[:/]|$))(?!\[)(?!\d+(?:\.\d+){3}(?::|/|$))(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}(?::\d+)?(?:/|$)`,
		RE2:      `^https://(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}(?::\d+)?(?:/|$)`,
		Reason: "Each lookahead excludes only strings the rest already rejects: the authority must be dot-separated labels ending in an " +
			"alphabetic TLD, optionally :port, then / or the end, so it holds no @, is never bare localhost, never starts with [ and never ends in a digit.",
		At: []string{
			"core/reporting-canonical-content-digest.json#/properties/canonicalization_uri/pattern",
			"core/reporting-delivery-capabilities.json#/properties/operations_contact/properties/url/pattern",
			"core/reporting-delivery-offering.json#/properties/report_definition_uri/pattern",
			"core/reporting-delivery-offering.json#/properties/reporting_profile/properties/canonicalization_uri/pattern",
			"core/reporting-delivery-offering.json#/properties/reporting_profile/properties/schema_uri/pattern",
			"core/reporting-reliability-statistics.json#/properties/evidence/properties/attestation_uri/pattern",
			"core/reporting-revision.json#/properties/report_definition_uri/pattern",
			"core/reporting-revision.json#/properties/schema_uri/pattern",
		},
		Match:   []string{"https://reports.example.com/v1/def.json", "https://a.example.org:8443/", "https://cdn.example.co"},
		NoMatch: []string{"https://user@example.com/", "https://localhost/", "https://localhost:8080/x", "https://[::1]/", "https://127.0.0.1/", "http://example.com/", "https://example.com@evil/"},
	},
	{
		Original: `^P(?=\d|T)(?=.*\d)(?:\d+Y)?(?:\d+M)?(?:\d+D)?(?:T(?=\d)(?:\d+H)?(?:\d+M)?(?:\d+S)?)?$`,
		RE2:      `^P(?:` + isoDurationDate + `(?:` + isoDurationTime + `)?|` + isoDurationTime + `)$`,
		Reason:   "The lookaheads require at least one component and at least one component after T; the RE2 form lists the non-empty date and time parts instead.",
		At: []string{
			"core/reporting-report-definition.json#/properties/finality_policies/items/oneOf/1/properties/duration_after_period_end/pattern",
			"core/reporting-report-definition.json#/properties/finality_policies/items/oneOf/2/properties/minimum_age/pattern",
			"core/reporting-report-definition.json#/properties/finality_policies/items/oneOf/2/properties/unchanged_for/pattern",
			"core/reporting-report-definition.json#/properties/restatement_policy/properties/source_requery_duration/pattern",
			"core/reporting-schedule-offering.json#/properties/delivery_sla/pattern",
			"core/reporting-schedule.json#/properties/delivery_sla/pattern",
		},
		Match:   []string{"P1D", "PT4H", "P1Y2M3DT4H5M6S", "P0D", "PT30M", "P1M2D"},
		NoMatch: []string{"P", "PT", "P1DT", "1D", "P1H", "PT1D", "PY"},
	},
	{
		Original: `^P(?=.*[1-9])(?=\d|T)(?:\d+Y)?(?:\d+M)?(?:\d+D)?(?:T(?=\d)(?:\d+H)?(?:\d+M)?(?:\d+S)?)?$`,
		RE2: `^P(?:` + strings.Join([]string{
			nonZeroInt + `Y(?:\d+M)?(?:\d+D)?(?:` + isoDurationTime + `)?`,
			`(?:0+Y)?` + nonZeroInt + `M(?:\d+D)?(?:` + isoDurationTime + `)?`,
			`(?:0+Y)?(?:0+M)?` + nonZeroInt + `D(?:` + isoDurationTime + `)?`,
			`(?:0+Y)?(?:0+M)?(?:0+D)?T(?:` + nonZeroInt + `H(?:\d+M)?(?:\d+S)?|(?:0+H)?` + nonZeroInt + `M(?:\d+S)?|(?:0+H)?(?:0+M)?` + nonZeroInt + `S)`,
		}, "|") + `)$`,
		Reason: "As the duration above, plus (?=.*[1-9]): some component is non-zero. The RE2 form branches on the first non-zero component; those before it are zero.",
		At: []string{
			"core/reporting-schedule-offering.json#/properties/period_duration/pattern",
			"core/reporting-schedule.json#/properties/period_duration/pattern",
		},
		Match:   []string{"P1D", "PT1H", "P0Y1M", "P0DT0H10M", "P1Y2M3DT4H5M6S", "P0Y1D"},
		NoMatch: []string{"P0D", "PT0S", "P0Y0M0DT0H0M0S", "P", "PT", "P1DT"},
	},
	{
		Original: `^P(?!$)(\d+Y)?(\d+M)?(\d+D)?(T(\d+H)?(\d+M)?(\d+S)?)?$`,
		RE2:      `^P(?:\d+Y(\d+M)?(\d+D)?(T(\d+H)?(\d+M)?(\d+S)?)?|\d+M(\d+D)?(T(\d+H)?(\d+M)?(\d+S)?)?|\d+D(T(\d+H)?(\d+M)?(\d+S)?)?|T(\d+H)?(\d+M)?(\d+S)?)$`,
		Reason:   "(?!$) rejects a bare P; the RE2 form lists the alternatives that start with a component or with T.",
		At: []string{
			"core/sla-window.json#/properties/completion_max/pattern",
			"core/sla-window.json#/properties/response_max/pattern",
		},
		Match:   []string{"PT", "P1D", "P1DT", "PT15M", "PT4H"},
		NoMatch: []string{"P", "1D", "P1H", "PX"},
	},
	{
		Original: `^(?:/(?:[^~/\u0000-\u001F\u007F]|~0|~1)*)*$`,
		RE2:      `^(?:/(?:[^~/\x00-\x1F\x7F]|~0|~1)*)*$`,
		Reason:   `\uXXXX escapes are written \xXX.`,
		At:       []string{"error-details/accessibility-violation.json#/properties/violations/items/properties/pointer/pattern"},
		Match:    []string{"", "/assets/0/alt_text", "/a~1b/~0c"},
		NoMatch:  []string{"assets", "/a~2", "/a\nb"},
	},
	{
		Original: `^[^\u0000-\u001F\u007F]+$`,
		RE2:      `^[^\x00-\x1F\x7F]+$`,
		Reason:   `\uXXXX escapes are written \xXX.`,
		At: []string{
			"error-details/accessibility-violation.json#/properties/violations/items/properties/criterion/pattern",
			"error-details/accessibility-violation.json#/properties/violations/items/properties/required_level/pattern",
		},
		Match:   []string{"1.1.1", "AA"},
		NoMatch: []string{"A\tA", "x\x7f"},
	},
	{
		Original: `^[^\u0000-\u001F\u007F]*$`,
		RE2:      `^[^\x00-\x1F\x7F]*$`,
		Reason:   `\uXXXX escapes are written \xXX.`,
		At:       []string{"error-details/accessibility-violation.json#/properties/violations/items/properties/remediation/pattern"},
		Match:    []string{"", "Add alt text."},
		NoMatch:  []string{"line\nbreak"},
	},
}

func re2TranslationAt(at string) (re2Translation, bool) {
	for _, tr := range re2Translations {
		if slices.Contains(tr.At, at) {
			return tr, true
		}
	}

	return re2Translation{}, false
}

// schemaSourceFiles lists the bundle's source documents: the files whose
// $id is their own path under the base. It leaves out the files that are
// not schemas (manifest.json) and the derived copies: bundled/ reuses the
// source $id values, and mcp/ holds 2020-12 projections with the referenced
// schemas inlined under $defs.
func schemaSourceFiles(t testing.TB, b *schemaBundle) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(schemaBundleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, schemaBundleDir+string(filepath.Separator)))
		if d.IsDir() && (rel == "bundled" || rel == "mcp") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(rel, ".json") {
			return nil
		}
		// #nosec G304 G122 -- test reads the downloaded bundle it walks under adcp/v3/schemas.
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc struct {
			ID string `json:"$id"`
		}
		if json.Unmarshal(raw, &doc) == nil && doc.ID == b.base+rel {
			files = append(files, rel)
		}

		return nil
	})
	require.NoError(t, err)

	return files
}

// TestRE2TranslationsMatchBundle keeps re2Translations exact: every pattern
// in the bundle that Go cannot compile is listed at its location, and every
// listed location still holds its original pattern, which still does not
// compile. Each translation compiles and agrees with its probes.
func TestRE2TranslationsMatchBundle(t *testing.T) {
	b := requireSchemaBundle(t)

	found := map[string]string{} // location -> pattern
	for _, rel := range schemaSourceFiles(t, b) {
		// #nosec G304 -- rel comes from walking adcp/v3/schemas.
		raw, err := os.ReadFile(filepath.Join(schemaBundleDir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		var doc any
		require.NoError(t, json.Unmarshal(raw, &doc), rel)
		walkSchemaPatterns(doc, "", func(obj map[string]any, ptr string) {
			p, _ := obj["pattern"].(string)
			if _, err := regexp.Compile(p); err != nil {
				found[rel+"#"+ptr+"/pattern"] = p
			}
		}, func(ptr, key string) {
			t.Errorf("%s#%s: patternProperties key %q does not compile as Go RE2", rel, ptr, key)
		})
	}

	listed := map[string]bool{}
	for _, tr := range re2Translations {
		t.Run(tr.Original, func(t *testing.T) {
			_, err := regexp.Compile(tr.Original)
			assert.Error(t, err, "the original compiles as RE2 now; remove this entry")
			assert.NotEmpty(t, tr.Reason)
			if tr.RE2 == "" {
				assert.Empty(t, append(tr.Match, tr.NoMatch...), "a dropped keyword has no translation to probe")
			}
			re, err := regexp.Compile(tr.RE2)
			require.NoError(t, err)
			for _, s := range tr.Match {
				assert.True(t, re.MatchString(s), "translation rejects %q, which the original accepts", s)
			}
			for _, s := range tr.NoMatch {
				assert.False(t, re.MatchString(s), "translation accepts %q, which the original rejects", s)
			}
			for _, at := range tr.At {
				assert.False(t, listed[at], "%s is listed twice", at)
				listed[at] = true
				got, ok := found[at]
				if assert.True(t, ok, "%s no longer holds an uncompilable pattern; remove it from At", at) {
					assert.Equal(t, tr.Original, got, "%s holds a different pattern now", at)
				}
			}
		})
	}
	for at, p := range found {
		assert.True(t, listed[at], "%s: pattern %q does not compile as Go RE2; add it to re2Translations", at, p)
	}
}

// TestRE2TranslationsApplyInSchemas validates each probe as the value of the
// property that carries the translated pattern, at every listed location, so
// a translation that the loader failed to apply would show here.
func TestRE2TranslationsApplyInSchemas(t *testing.T) {
	requireSchemaBundle(t)
	for _, tr := range re2Translations {
		for _, at := range tr.At {
			ref := strings.TrimSuffix(at, "/pattern")
			for _, s := range tr.Match {
				raw, err := json.Marshal(s)
				require.NoError(t, err)
				assert.NoError(t, validateAgainstSchema(ref, raw), "%s: %q", ref, s)
			}
			for _, s := range tr.NoMatch {
				raw, err := json.Marshal(s)
				require.NoError(t, err)
				assert.Error(t, validateAgainstSchema(ref, raw), "%s: %q", ref, s)
			}
		}
	}
}

// TestSchemaBundleExamplesValidate validates every top-level example in the
// bundle's source schemas against its own schema, so the helper is shown to
// accept what the protocol publishes as valid, including
// creative/sync-creatives-request.json, which reaches a translated pattern.
func TestSchemaBundleExamplesValidate(t *testing.T) {
	b := requireSchemaBundle(t)
	examples := map[string]int{}
	for _, rel := range schemaSourceFiles(t, b) {
		// #nosec G304 -- rel comes from walking adcp/v3/schemas.
		raw, err := os.ReadFile(filepath.Join(schemaBundleDir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		var doc struct {
			Examples []any `json:"examples"`
		}
		require.NoError(t, json.Unmarshal(raw, &doc), rel)
		if len(doc.Examples) == 0 {
			continue
		}
		rs, err := b.resolve(rel)
		if !assert.NoError(t, err) {
			continue
		}
		for i, ex := range doc.Examples {
			// Most bundle examples are {"description": ..., "data": <instance>}.
			if m, ok := ex.(map[string]any); ok && len(m) == 2 && m["description"] != nil && m["data"] != nil {
				ex = m["data"]
			}
			examples[rel]++
			assert.NoError(t, rs.Validate(ex), "%s examples[%d]", rel, i)
		}
	}
	assert.NotZero(t, examples["creative/sync-creatives-request.json"])
	total := 0
	for _, n := range examples {
		total += n
	}
	t.Logf("validated %d examples in %d schemas", total, len(examples))
}

// TestAssertSchemaValidRejectsKnownBadFixtures replays two fixtures that
// passed CI and review because the Go types accept any string or object: the
// first heads of adcontextprotocol/adcp-go#545 (c715938) and #581 (7643ff1),
// verbatim. Each is rejected, and the defect named is what makes it invalid.
func TestAssertSchemaValidRejectsKnownBadFixtures(t *testing.T) {
	requireSchemaBundle(t)

	t.Run("545 creative status outside the enum", func(t *testing.T) {
		const ref = "creative/sync-creatives-response.json#/oneOf/0/properties/creatives/items"
		const fixture = `{
		"creative_id": "cr-1",
		"revision_id": "rev-7",
		"account": {"account_id": "acct-1", "name": "Acme", "status": "active"},
		"action": "updated",
		"status": "active",
		"platform_id": "gam-123",
		"changes": ["name", "format_id"],
		"errors": [],
		"warnings": ["low resolution asset"],
		"preview_url": "https://example.com/preview/cr-1",
		"expires_at": "2026-12-31T00:00:00Z",
		"assigned_to": ["pkg-1", "pkg-2"],
		"assignment_errors": {"pkg-3": "format not supported"}
	}`
		err := validateAgainstSchema(ref, []byte(fixture))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "enums/creative-status.json")

		fixed := strings.Replace(fixture, `"status": "active",`, `"status": "approved",`, 1)
		assert.NoError(t, validateAgainstSchema(ref, []byte(fixed)))
	})

	t.Run("581 localization in readback shape", func(t *testing.T) {
		const ref = "creative/sync-creatives-request.json#/properties/creatives/items"
		const fixture = `{
		"creative_id": "cr-1",
		"revision_id": "rev-7",
		"name": "Summer Sale",
		"assets": {"image": {"asset_type": "image", "url": "https://cdn.example.com/summer.jpg"}},
		"localization": {
			"default_locale_variant_id": "loc_en_us",
			"variants": [{"locale_variant_id": "loc_en_us", "locale": "en-US", "role": "source"}]
		}
	}`
		require.Error(t, validateAgainstSchema(ref, []byte(fixture)))

		// The fixture also lacks format_kind and the image's width and
		// height. With those added, localization is what remains invalid.
		complete := strings.NewReplacer(
			`"name": "Summer Sale",`, `"name": "Summer Sale", "format_kind": "image",`,
			`summer.jpg"}`, `summer.jpg", "width": 300, "height": 250}`,
		).Replace(fixture)
		err := validateAgainstSchema(ref, []byte(complete))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/properties/localization")

		var creative map[string]any
		require.NoError(t, json.Unmarshal([]byte(complete), &creative))
		delete(creative, "localization")
		withoutLocalization, err := json.Marshal(creative)
		require.NoError(t, err)
		assert.NoError(t, validateAgainstSchema(ref, withoutLocalization))

		creative["localization"] = map[string]any{
			"source":                    map[string]any{"locale_variant_id": "loc_en_us", "locale": "en-US"},
			"target_variants":           []any{},
			"default_locale_variant_id": "loc_en_us",
			"unmatched_locale_action":   "serve_default",
		}
		requestShaped, err := json.Marshal(creative)
		require.NoError(t, err)
		assert.NoError(t, validateAgainstSchema(ref, requestShaped))
	})
}
