package idempotency

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalOrderingIsStable(t *testing.T) {
	a := []byte(`{"b":1,"a":2,"c":{"z":1,"y":2}}`)
	b := []byte(`{"a":2,"c":{"y":2,"z":1},"b":1}`)
	ha, err := CanonicalJSONSHA256(a)
	require.NoError(t, err)
	hb, err := CanonicalJSONSHA256(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb, "key-order permutations must hash identically")
}

func TestCanonicalExcludesIdempotencyKeyAndContext(t *testing.T) {
	a := []byte(`{"idempotency_key":"abc","context":{"trace":"1"},"account":"x"}`)
	b := []byte(`{"idempotency_key":"zzz","context":{"trace":"2"},"account":"x"}`)
	ha, err := CanonicalJSONSHA256(a)
	require.NoError(t, err)
	hb, err := CanonicalJSONSHA256(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb, "excluded top-level fields must not affect hash")
}

func TestCanonicalExcludesNestedCredentials(t *testing.T) {
	a := []byte(`{"account":"x","push_notification_config":{"url":"u","authentication":{"scheme":"bearer","credentials":"AAA"}}}`)
	b := []byte(`{"account":"x","push_notification_config":{"url":"u","authentication":{"scheme":"bearer","credentials":"BBB"}}}`)
	ha, err := CanonicalJSONSHA256(a)
	require.NoError(t, err)
	hb, err := CanonicalJSONSHA256(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb, "nested credentials must be excluded")
}

func TestCanonicalDetectsPayloadDrift(t *testing.T) {
	a := []byte(`{"account":"x","budget":1000}`)
	b := []byte(`{"account":"x","budget":1001}`)
	ha, err := CanonicalJSONSHA256(a)
	require.NoError(t, err)
	hb, err := CanonicalJSONSHA256(b)
	require.NoError(t, err)
	assert.NotEqual(t, ha, hb, "different values MUST yield different hashes")
}

func TestCanonicalNumbers(t *testing.T) {
	cases := []struct {
		in       string
		expected string
	}{
		{`0`, `0`},
		{`1`, `1`},
		{`-1`, `-1`},
		{`100`, `100`},
		{`1.5`, `1.5`},
		{`1e2`, `100`},
		{`1e21`, `1e+21`},
		{`1e-7`, `1e-7`},
		{`1.0`, `1`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var v any
			dec := json.NewDecoder(strings.NewReader(tc.in))
			dec.UseNumber()
			require.NoError(t, dec.Decode(&v))
			out, err := canonicalize(v)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, string(out))
		})
	}
}

func TestCanonicalStringEscaping(t *testing.T) {
	v := map[string]any{"x": "line1\nline2\t\"q\"\\"}
	out, err := canonicalize(v)
	require.NoError(t, err)
	assert.Equal(t, `{"x":"line1\nline2\t\"q\"\\"}`, string(out))
}

func TestCanonicalRejectsInvalidJSON(t *testing.T) {
	_, err := CanonicalJSONSHA256([]byte(`{`))
	assert.Error(t, err)
}

func TestCanonicalRejectsInvalidUTF8InStringValue(t *testing.T) {
	v := map[string]any{"x": "abc\xffdef"}
	_, err := canonicalize(v)
	require.Error(t, err)
}

func TestCanonicalRejectsInvalidUTF8InKey(t *testing.T) {
	v := map[string]any{"bad\xffkey": "v"}
	_, err := canonicalize(v)
	require.Error(t, err)
}

func TestCanonicalUTF16SortDifferentiatesPrecomposedAndDecomposed(t *testing.T) {
	// "é" (U+00E9) vs "e" + combining acute (U+0065 U+0301) — JCS sorts by
	// UTF-16 code units, not NFC equivalence, so these must hash differently.
	a := []byte(`{"\u00e9":1}`)
	b := []byte(`{"\u0065\u0301":1}`)
	ha, err := CanonicalJSONSHA256(a)
	require.NoError(t, err)
	hb, err := CanonicalJSONSHA256(b)
	require.NoError(t, err)
	assert.NotEqual(t, ha, hb)
}

func TestCanonicalCustomExcludes(t *testing.T) {
	hash := NewCanonicalJSONSHA256([]string{"trace_id"})
	a := []byte(`{"trace_id":"t1","v":1}`)
	b := []byte(`{"trace_id":"t2","v":1}`)
	ha, err := hash(a)
	require.NoError(t, err)
	hb, err := hash(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb)
}
