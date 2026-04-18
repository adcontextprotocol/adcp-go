package idempotency

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateIsUUIDv4(t *testing.T) {
	k := Generate()
	assert.Len(t, k, 36)
	assert.Equal(t, byte('-'), k[8])
	assert.Equal(t, byte('-'), k[13])
	assert.Equal(t, byte('-'), k[18])
	assert.Equal(t, byte('-'), k[23])
	// v4 indicator
	assert.Equal(t, byte('4'), k[14])
	require.NoError(t, Validate(k))
}

func TestValidate(t *testing.T) {
	bad := []string{"", "short", "has space in it really", "has!chars!!!!!!!!"}
	for _, b := range bad {
		var ike *InvalidKeyError
		assert.Truef(t, errors.As(Validate(b), &ike), "%q should be invalid", b)
	}
	good := []string{
		"aaaaaaaaaaaaaaaa",
		"idem-" + Generate(),
		"A1.b2_c3:d4-e5f6",
	}
	for _, g := range good {
		assert.NoErrorf(t, Validate(g), "%q should be valid", g)
	}
}

func TestLogKeyRedacts(t *testing.T) {
	k := Generate()
	redacted := LogKey(k)
	assert.NotEqual(t, k, redacted)
	assert.Contains(t, redacted, k[:8])
	assert.NotContains(t, redacted, k[30:])
}

func TestFreezeGeneratesKey(t *testing.T) {
	req := map[string]any{"account": "acct-1"}
	env, err := Freeze(req, "")
	require.NoError(t, err)
	require.NoError(t, Validate(env.Key))

	var got map[string]any
	require.NoError(t, json.Unmarshal(env.Bytes, &got))
	assert.Equal(t, env.Key, got["idempotency_key"])
}

func TestFreezePreservesExistingKey(t *testing.T) {
	existing := Generate()
	req := map[string]any{"idempotency_key": existing, "account": "a"}
	env, err := Freeze(req, "")
	require.NoError(t, err)
	assert.Equal(t, existing, env.Key)
}

func TestFreezeOverrideMatchesExistingOK(t *testing.T) {
	k := Generate()
	req := map[string]any{"idempotency_key": k, "account": "a"}
	env, err := Freeze(req, k)
	require.NoError(t, err)
	assert.Equal(t, k, env.Key)
}

func TestFreezeOverrideConflictsErrors(t *testing.T) {
	req := map[string]any{"idempotency_key": Generate(), "account": "a"}
	_, err := Freeze(req, Generate())
	assert.Error(t, err)
}

func TestFreezeInvalidOverride(t *testing.T) {
	req := map[string]any{"account": "a"}
	_, err := Freeze(req, "bad")
	var ike *InvalidKeyError
	assert.True(t, errors.As(err, &ike))
}

func TestFreezeBytesPreservesExistingEncoding(t *testing.T) {
	// A non-alphabetical key ordering the caller chose — must be preserved
	// when idempotency_key is already present.
	key := Generate()
	raw := []byte(`{"z":1,"a":2,"idempotency_key":"` + key + `"}`)
	env, err := FreezeBytes(raw, "")
	require.NoError(t, err)
	assert.Equal(t, key, env.Key)
	assert.Equal(t, raw, env.Bytes, "bytes must be returned verbatim when key is already present")
}

func TestFreezeBytesRejectsAbsentKey(t *testing.T) {
	raw := []byte(`{"account":"a","budget":100}`)
	_, err := FreezeBytes(raw, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use Freeze")
}

func TestFreezeBytesRejectsInvalidExistingKey(t *testing.T) {
	raw := []byte(`{"idempotency_key":"bad","account":"a"}`)
	_, err := FreezeBytes(raw, "")
	var ike *InvalidKeyError
	assert.True(t, errors.As(err, &ike))
}

func TestFreezeBytesOverrideMatches(t *testing.T) {
	key := Generate()
	raw := []byte(`{"idempotency_key":"` + key + `","account":"a"}`)
	env, err := FreezeBytes(raw, key)
	require.NoError(t, err)
	assert.Equal(t, raw, env.Bytes)
}

func TestFreezeBytesOverrideConflicts(t *testing.T) {
	raw := []byte(`{"idempotency_key":"` + Generate() + `","account":"a"}`)
	_, err := FreezeBytes(raw, Generate())
	assert.Error(t, err)
}

func TestParseCapability(t *testing.T) {
	caps := map[string]any{
		"adcp": map[string]any{
			"idempotency": map[string]any{
				"replay_ttl_seconds": 86400,
			},
		},
	}
	ttl, err := ParseCapability(caps, "seller")
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, ttl)
}

func TestParseCapabilityFromJSONNumber(t *testing.T) {
	raw := []byte(`{"adcp":{"idempotency":{"replay_ttl_seconds":3600}}}`)
	var caps map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&caps))
	ttl, err := ParseCapability(caps, "seller")
	require.NoError(t, err)
	assert.Equal(t, time.Hour, ttl)
}

func TestParseCapabilityMissing(t *testing.T) {
	cases := []map[string]any{
		{},
		{"adcp": map[string]any{}},
		{"adcp": map[string]any{"idempotency": map[string]any{}}},
		{"adcp": map[string]any{"idempotency": map[string]any{"replay_ttl_seconds": 0}}},
		{"adcp": map[string]any{"idempotency": map[string]any{"replay_ttl_seconds": "nope"}}},
	}
	for _, c := range cases {
		_, err := ParseCapability(c, "s")
		var mc *MissingCapabilityError
		assert.True(t, errors.As(err, &mc), "expected MissingCapabilityError for %+v", c)
	}
}

