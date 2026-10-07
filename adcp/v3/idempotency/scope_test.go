package idempotency

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func principalScope(t *testing.T, p string) string {
	t.Helper()
	s, err := PrincipalScope(WithPrincipal(context.Background(), p), nil)
	require.NoError(t, err)
	return s
}

func sessionScope(t *testing.T, p, sid string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"session_id": sid})
	require.NoError(t, err)
	s, err := SessionScope("session_id")(WithPrincipal(context.Background(), p), payload)
	require.NoError(t, err)
	return s
}

// TestScopesCannotCollide covers inputs that collided under plain string
// concatenation, e.g. principal "a:session:b" vs principal "a" in session "b".
func TestScopesCannotCollide(t *testing.T) {
	assert.NotEqual(t, principalScope(t, "a:session:b"), sessionScope(t, "a", "b"))
	assert.NotEqual(t, sessionScope(t, "a:session:b", "c"), sessionScope(t, "a", "b:session:c"))
	assert.NotEqual(t, sessionScope(t, "a", "1:b"), principalScope(t, "a:session:1:b"))
	assert.NotEqual(t, principalScope(t, "webhook:sender:x"), EncodeScope("webhook", "sender", "x"))
	assert.Equal(t, EncodeScope("principal", "a", "session", "b"), sessionScope(t, "a", "b"))
	assert.Equal(t, "principal:1:a:7:session:1:b", sessionScope(t, "a", "b"))
}

// TestEncodeScopeIsInjective brute-forces every tuple of 0-3 parts over an
// alphabet chosen to stress the format (empty, separator, digits, length-like
// prefixes, multi-byte UTF-8) and asserts no two tuples share an encoding.
func TestEncodeScopeIsInjective(t *testing.T) {
	alphabet := []string{"", ":", "1", "a", "1:a", ":1:", "é"}
	var tuples [][]string
	var build func(prefix []string, depth int)
	build = func(prefix []string, depth int) {
		tuples = append(tuples, append([]string(nil), prefix...))
		if depth == 3 {
			return
		}
		for _, part := range alphabet {
			build(append(prefix, part), depth+1)
		}
	}
	build(nil, 0)

	seen := map[string][]string{}
	for _, namespace := range []string{"principal", "webhook"} {
		for _, parts := range tuples {
			enc := EncodeScope(namespace, parts...)
			key := append([]string{namespace}, parts...)
			if prev, dup := seen[enc]; dup {
				t.Fatalf("EncodeScope collision: %q and %q both encode to %q", prev, key, enc)
			}
			seen[enc] = key
		}
	}
}

func TestEncodeScopeRejectsSeparatorInNamespace(t *testing.T) {
	assert.Panics(t, func() { EncodeScope("a:1:x") })
}

func TestPrincipalScopeRequiresPrincipal(t *testing.T) {
	_, err := PrincipalScope(context.Background(), nil)
	require.Error(t, err)
}

func contextIDScope(t *testing.T, p, body string) string {
	t.Helper()
	s, err := ContextIDScope(WithPrincipal(context.Background(), p), []byte(body))
	require.NoError(t, err)
	return s
}

// TestContextIDScopeCannotCollide covers inputs that collided when
// ContextIDScope joined components with plain string concatenation.
func TestContextIDScopeCannotCollide(t *testing.T) {
	assert.NotEqual(t, contextIDScope(t, "a:ctx:b", `{}`), contextIDScope(t, "a", `{"context_id":"b"}`))
	assert.NotEqual(t, contextIDScope(t, "1:a", `{}`), principalScope(t, "a"))
	assert.Equal(t, principalScope(t, "a"), contextIDScope(t, "a", `{}`), "no context_id falls back to the principal scope")
	assert.Equal(t, principalScope(t, "a"), contextIDScope(t, "a", `{"context_id":""}`))
	assert.Equal(t, EncodeScope("principal", "a", "ctx", "b"), contextIDScope(t, "a", `{"context_id":"b"}`))
}
