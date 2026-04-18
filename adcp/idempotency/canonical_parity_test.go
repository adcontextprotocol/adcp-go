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
		// Key ordering by UTF-16 code units
		[]byte(`{"\u00e9":1,"\u0065":2}`),
		// AdCP-shaped payload
		[]byte(`{"account":"acct-1","brand":{"name":"Acme","id":"b-1"},"budget":1000,"packages":[{"id":"p1","size":"300x250"},{"id":"p2","size":"728x90"}]}`),
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
		// Skip inputs that aren't valid JSON at all — not our concern.
		var probe any
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Skip()
		}
		// Skip inputs containing NaN/Inf via Go-only extensions (shouldn't
		// appear in valid JSON anyway, but json.Unmarshal accepts some edge
		// cases the reference library rejects; a divergence on invalid JSON
		// is uninteresting).
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
