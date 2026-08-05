package idempotency

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectReplayedPreservesSiblings(t *testing.T) {
	env := []byte(`{"adcp_version":"1.0.0","message":"ok","media_buy_id":"mb-1"}`)
	out, err := InjectReplayed(env, true)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	assert.Equal(t, true, m["replayed"])
	assert.Equal(t, "mb-1", m["media_buy_id"])
	assert.Equal(t, "1.0.0", m["adcp_version"])
}

func TestInjectReplayedFalse(t *testing.T) {
	out, err := InjectReplayed([]byte(`{"x":1}`), false)
	require.NoError(t, err)
	rep, ok := ReadReplayed(out)
	require.True(t, ok)
	assert.False(t, rep)
}

func TestInjectReplayedRejectsNonObject(t *testing.T) {
	_, err := InjectReplayed([]byte(`"not an object"`), true)
	assert.Error(t, err)
}

func TestInjectReplayedOverwritesExisting(t *testing.T) {
	env := []byte(`{"replayed":false,"x":1}`)
	out, err := InjectReplayed(env, true)
	require.NoError(t, err)
	rep, ok := ReadReplayed(out)
	require.True(t, ok)
	assert.True(t, rep)
}
