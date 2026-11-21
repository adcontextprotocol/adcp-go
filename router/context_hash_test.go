package router

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gowebpki/jcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJCS_RFC8785CanonicalizationVector(t *testing.T) {
	input := []byte(`{
  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
  "string": "\u20ac$\u000F\nA'B\"\\\"/",
  "literals": [null, true, false]
}`)
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\"/"}`
	got, err := jcs.Transform(input)
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
}

func TestContextHash_RemovesOnlySchemaAndRequestID(t *testing.T) {
	a := []byte(`{"$schema":"https://schemas.example/a","request_id":"one","type":"context_match_request","property_rid":"rid","geo":{"metro":"501","country":"US"},"package_ids":["a","b"]}`)
	b := []byte(`{"package_ids":["a","b"],"geo":{"country":"US","metro":"501"},"property_rid":"rid","type":"context_match_request","request_id":"two","$schema":"https://schemas.example/b"}`)
	ha, err := ContextHash(a)
	require.NoError(t, err)
	hb, err := ContextHash(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb, "object order, request_id, and $schema must not affect context_hash")

	// Fixed regression value proves this is SHA-256 over the JCS form, not an
	// ad-hoc delimiter concatenation or ordinary json.Marshal output.
	assert.Equal(t, "0eb666a3e01750e0f884d81bc437ae1f7e86573fe8aaeca2c82f1a1f970e0fd0", hex.EncodeToString(ha[:]))
}

func TestContextHash_PreservesArrayOrder(t *testing.T) {
	a, err := ContextHash([]byte(`{"request_id":"one","package_ids":["a","b"],"artifact_refs":[{"type":"url","value":"https://example/a"},{"type":"url","value":"https://example/b"}]}`))
	require.NoError(t, err)
	b, err := ContextHash([]byte(`{"request_id":"two","package_ids":["b","a"],"artifact_refs":[{"type":"url","value":"https://example/b"},{"type":"url","value":"https://example/a"}]}`))
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestContextHash_RejectsAmbiguousOrNonIJSONInput(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"trailing value", []byte(`{"a":1} {"b":2}`)},
		{"duplicate key", []byte(`{"a":1,"a":2}`)},
		{"escaped duplicate key", []byte(`{"a":1,"\u0061":2}`)},
		{"lone high surrogate", []byte(`{"a":"\ud800"}`)},
		{"lone low surrogate", []byte(`{"a":"\udc00"}`)},
		{"invalid surrogate pair", []byte(`{"a":"\ud800\ud800"}`)},
		{"non-object", []byte(`[1,2,3]`)},
		{"invalid UTF-8", []byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ContextHash(tt.raw)
			assert.Error(t, err)
		})
	}
}

func TestContextHash_AcceptsValidSurrogatePair(t *testing.T) {
	_, err := ContextHash([]byte(`{"request_id":"one","emoji":"\ud83d\ude00"}`))
	assert.NoError(t, err)
}

func TestContextHash_PreservesNoncharactersDeterministically(t *testing.T) {
	escaped, err := ContextHash([]byte(`{"request_id":"one","\ufdd0":"\ufffe"}`))
	require.NoError(t, err)
	literal, err := ContextHash([]byte(`{"request_id":"two","` + string(rune(0xfdd0)) + `":"` + string(rune(0xfffe)) + `"}`))
	require.NoError(t, err)
	assert.Equal(t, escaped, literal, "escaped and direct noncharacter forms must canonicalize identically")
}

func TestContextHash_WideObjectReturnsCacheBypass(t *testing.T) {
	raw := wideContextHashDocument(5500)
	require.Less(t, len(raw), MaxRequestBodyBytes)
	require.NoError(t, validateContextIngressIJSON(raw), "cache work bounds must not redefine request validity")
	_, err := ContextHash(raw)
	assert.True(t, errors.Is(err, ErrContextHashComplexity))
}

func wideContextHashDocument(members int) []byte {
	return wideContextHashDocumentWithPrefix(members, "k")
}

func wideContextHashDocumentWithPrefix(members int, prefix string) []byte {
	var body strings.Builder
	body.WriteString(`{"request_id":"request","geo":{`)
	for i := range members {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"%s%04d":0`, prefix, i)
	}
	body.WriteString(`}}`)
	return []byte(body.String())
}

func BenchmarkContextHash_MaximumBoundedObject(b *testing.B) {
	// Long common prefixes exercise the maintained dependency's worst sorting
	// comparisons while keeping the body just below the HTTP admission limit.
	prefix := strings.Repeat("a", MaxRequestBodyBytes/MaxContextHashObjectMembers-32)
	raw := wideContextHashDocumentWithPrefix(MaxContextHashObjectMembers, prefix)
	if len(raw) >= MaxRequestBodyBytes {
		b.Fatalf("benchmark body is %d bytes", len(raw))
	}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ContextHash(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContextHash_OversizedObjectBypass(b *testing.B) {
	raw := wideContextHashDocument(5500)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ContextHash(raw); !errors.Is(err, ErrContextHashComplexity) {
			b.Fatalf("got %v, want complexity bypass", err)
		}
	}
}
