package idempotency

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canonicalBytes runs our canonicalizer end-to-end on raw JSON bytes.
// Used for parity comparison against gowebpki/jcs.Transform.
func canonicalBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	out, err := canonicalize(v)
	require.NoError(t, err)
	return out
}

// TestCanonicalParityFixtures asserts byte-identical output with gowebpki/jcs
// across inputs spanning the RFC 8785 §3 spec surface: key ordering, number
// formatting boundaries, string escapes, nested structures.
func TestCanonicalParityFixtures(t *testing.T) {
	fixtures := [][]byte{
		// Basic structures
		[]byte(`{"b":1,"a":2}`),
		[]byte(`{"nested":{"y":1,"x":2},"array":[3,1,2]}`),
		[]byte(`[]`),
		[]byte(`{}`),
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`false`),
		// Numbers at ES6 formatting boundaries
		[]byte(`{"n":0}`),
		[]byte(`{"n":1}`),
		[]byte(`{"n":-1}`),
		[]byte(`{"n":1.5}`),
		[]byte(`{"n":100}`),
		[]byte(`{"n":1e2}`),
		[]byte(`{"n":1e20}`),
		[]byte(`{"n":1e21}`),
		[]byte(`{"n":1e-6}`),
		[]byte(`{"n":1e-7}`),
		[]byte(`{"n":0.000001}`),
		[]byte(`{"n":0.0000001}`),
		// String escapes
		[]byte(`{"s":"hello"}`),
		[]byte(`{"s":"line\nbreak"}`),
		[]byte(`{"s":"tab\there"}`),
		[]byte(`{"s":"quote\"here"}`),
		[]byte(`{"s":"back\\slash"}`),
		// Unicode
		[]byte(`{"s":"unicode: café"}`),
		[]byte(`{"s":"emoji: 🎉"}`),
		// U+2028 / U+2029: ES6 JSON.stringify escapes these; RFC 8785 does
		// not. Highest-value parity guard for the string-escape rules.
		[]byte(`{"s":"line\u2028sep\u2029para"}`),
		// Control characters force lower-case \uXXXX hex output.
		[]byte(`{"s":"\u0000\u0001\u001f\u007f"}`),
		// Key ordering by UTF-16 code units — BMP cases.
		[]byte(`{"\u00e9":1,"\u0065":2}`),
		// UTF-16 ordering across BMP / supplementary-plane boundary. A
		// supplementary-plane codepoint is encoded as a surrogate pair in
		// UTF-16, so its leading code unit (\uD800-\uDBFF) sorts BELOW
		// \uFFFF — this is where UTF-16 order diverges from Unicode
		// codepoint order.
		[]byte(`{"\uD83D\uDE00":1,"\uFFFF":2,"\u0041":3}`),
		// 2^53 integer precision boundary. Both values canonicalize to the
		// same bytes per RFC 8785 §3.2.2.3 (IEEE-754 double precision loss).
		// Fixture locks in the documented behavior on CanonicalJSONSHA256.
		[]byte(`{"a":9007199254740992,"b":9007199254740993}`),
		// Denormal and boundary float values.
		[]byte(`{"tiny":5e-324,"huge":1.7976931348623157e+308}`),
		// AdCP-shaped payload with nested array-of-objects, nullable brand,
		// empty array in optional slot, ISO-8601 timestamp.
		[]byte(`{"account":"acct-1","brand":null,"budget":1000,"deadline":"2026-06-01T00:00:00Z","packages":[{"id":"p1","size":"300x250"},{"id":"p2","size":"728x90"}],"tags":[]}`),
	}

	for _, raw := range fixtures {
		t.Run(string(raw), func(t *testing.T) {
			ours := canonicalBytes(t, raw)
			theirs, err := jcs.Transform(raw)
			require.NoError(t, err, "jcs.Transform failed on %s", raw)
			assert.Equal(t, string(theirs), string(ours),
				"canonical form diverges from gowebpki/jcs for %s", raw)
		})
	}
}

// FuzzCanonicalParityVsGowebpki compares our canonicalizer against the
// reference library across arbitrary JSON inputs. Divergence → either our
// canonicalizer has a spec bug, or the fuzz generated input that both
// libraries handle differently from the spec (in which case, harden the
// fuzz filter).
//
// Run with: go test -fuzz=FuzzCanonicalParityVsGowebpki ./idempotency/
func FuzzCanonicalParityVsGowebpki(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"a":1}`),
		[]byte(`{"nested":{"key":["array",1,2.5],"other":"value"}}`),
		[]byte(`[]`),
		[]byte(`null`),
		[]byte(`{"n":1.5e-7}`),
		// Direct the fuzzer toward UTF-16 sort edge cases.
		[]byte(`{"\uD834\uDD1E":1,"a":2}`),
		[]byte(`{"\uFFFF":1,"\uD800\uDC00":2}`),
		// ES6/RFC 8785 divergence site.
		[]byte(`{"s":"\u2028\u2029"}`),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		// RFC 8259 requires JSON to be UTF-8. Go's json.Decoder silently
		// substitutes U+FFFD for invalid bytes while gowebpki/jcs may treat
		// them differently; the resulting divergence tells us nothing about
		// spec compliance.
		if !utf8.Valid(raw) {
			t.Skip()
		}
		// Skip inputs that aren't valid JSON — not our concern.
		var probe any
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Skip()
		}
		// Skip inputs the reference library rejects — a divergence on
		// inputs one side considers invalid tells us nothing about spec
		// compliance.
		theirs, err := jcs.Transform(raw)
		if err != nil {
			t.Skip()
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Skip()
		}
		ours, err := canonicalize(v)
		if err != nil {
			// Our canonicalizer rejects things like invalid UTF-8 that the
			// reference accepts. Skip those rather than flag as divergence.
			t.Skip()
		}
		if !bytes.Equal(ours, theirs) {
			t.Fatalf("divergence on input %q:\nours:   %s\ntheirs: %s", raw, ours, theirs)
		}
	})
}
