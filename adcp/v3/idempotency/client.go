package idempotency

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"time"
)

// keyPattern matches the schema requirement: ASCII letters, digits, and the
// set [_.:-], length 16..255.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{16,255}$`)

// Generate returns a new UUID v4 formatted idempotency key (36 chars with
// dashes). The caller SHOULD cache this on the request struct so internal
// retries resend the same key.
func Generate() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("idempotency: crypto/rand failed: %w", err))
	}
	// RFC 4122 v4 variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Validate checks a key against the AdCP schema pattern. Returns nil on OK,
// or *InvalidKeyError.
func Validate(key string) error {
	if key == "" {
		return &InvalidKeyError{Reason: "empty"}
	}
	if !keyPattern.MatchString(key) {
		return &InvalidKeyError{Reason: "must match ^[A-Za-z0-9_.:-]{16,255}$"}
	}
	return nil
}

// LogKey returns a prefix-truncated form of key safe for default logging.
// Full keys are retry-pattern oracles; callers that want full keys in logs
// must opt in explicitly.
func LogKey(key string) string {
	const n = 8
	if len(key) <= n {
		return "****"
	}
	return key[:n] + "…"
}

// RequestEnvelope is the frozen form of a request, bound to a specific key.
// Callers that implement their own transport use *RequestEnvelope to satisfy
// the freeze-bytes-on-first-send rule: marshal once, resend byte-identical
// on every retry. Re-marshaling the struct is incorrect because Go's
// json.Marshal stability is not guaranteed across dependency changes.
type RequestEnvelope struct {
	Key   string
	Bytes []byte
}

// Freeze marshals req through map[string]any, injects an idempotency_key
// (generating one if neither req nor keyOverride has one), and returns the
// bound bytes. Use this for untyped / map requests. For strongly-typed
// request structs where byte-for-byte preservation matters, marshal yourself
// and call FreezeBytes.
func Freeze(req any, keyOverride string) (*RequestEnvelope, error) {
	obj, err := asMap(req)
	if err != nil {
		return nil, err
	}

	existing, _ := obj["idempotency_key"].(string)
	key, err := resolveKey(existing, keyOverride)
	if err != nil {
		return nil, err
	}
	obj["idempotency_key"] = key

	frozen, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("idempotency: freeze request bytes: %w", err)
	}
	return &RequestEnvelope{Key: key, Bytes: frozen}, nil
}

// FreezeBytes takes already-marshaled JSON and returns it unchanged, bound to
// its existing idempotency_key. Use this when byte-exact retry semantics
// matter: the caller marshals their request struct once (preserving field
// order, number precision, custom encoders), and the SDK resends those same
// bytes on every retry.
//
// The input MUST contain a top-level idempotency_key. Generating one here
// would require re-marshaling, which is what this function exists to avoid —
// callers without a key should use Freeze instead, then treat the returned
// bytes as the canonical form.
//
// If keyOverride is non-empty it must match the key in the payload;
// mismatches return an error so a caller cannot silently overwrite a key
// already committed to bytes.
func FreezeBytes(jsonReq []byte, keyOverride string) (*RequestEnvelope, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(jsonReq, &probe); err != nil {
		return nil, fmt.Errorf("idempotency: decode request: %w", err)
	}
	raw, ok := probe["idempotency_key"]
	if !ok {
		return nil, fmt.Errorf("idempotency: FreezeBytes requires idempotency_key in the payload; use Freeze to generate one")
	}
	var existing string
	if err := json.Unmarshal(raw, &existing); err != nil {
		return nil, &InvalidKeyError{Reason: "not a string"}
	}
	if err := Validate(existing); err != nil {
		return nil, err
	}
	if keyOverride != "" && keyOverride != existing {
		return nil, fmt.Errorf("idempotency: WithIdempotencyKey(%s) conflicts with idempotency_key already set on request (%s)", LogKey(keyOverride), LogKey(existing))
	}
	out := make([]byte, len(jsonReq))
	copy(out, jsonReq)
	return &RequestEnvelope{Key: existing, Bytes: out}, nil
}

func resolveKey(existing, override string) (string, error) {
	switch {
	case override != "" && existing != "" && override != existing:
		return "", fmt.Errorf("idempotency: WithIdempotencyKey(%s) conflicts with idempotency_key already set on request (%s)", LogKey(override), LogKey(existing))
	case override != "":
		if err := Validate(override); err != nil {
			return "", err
		}
		return override, nil
	case existing != "":
		if err := Validate(existing); err != nil {
			return "", err
		}
		return existing, nil
	default:
		return Generate(), nil
	}
}

func asMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		out := make(map[string]any, len(m))
		maps.Copy(out, m)
		return out, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("idempotency: marshal request: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("idempotency: decode request: %w", err)
	}
	return out, nil
}

// ParseCapability extracts adcp.idempotency.replay_ttl_seconds from a
// get_adcp_capabilities response body. Returns MissingCapabilityError if the
// field is absent — per spec, clients MUST NOT fall back to an assumed TTL.
func ParseCapability(caps map[string]any, agentID string) (time.Duration, error) {
	adcp, ok := caps["adcp"].(map[string]any)
	if !ok {
		return 0, &MissingCapabilityError{AgentID: agentID}
	}
	ide, ok := adcp["idempotency"].(map[string]any)
	if !ok {
		return 0, &MissingCapabilityError{AgentID: agentID}
	}
	raw, ok := ide["replay_ttl_seconds"]
	if !ok {
		return 0, &MissingCapabilityError{AgentID: agentID}
	}
	secs, err := toFloat(raw)
	if err != nil || secs <= 0 {
		return 0, &MissingCapabilityError{AgentID: agentID}
	}
	// Cap at ~292 years to avoid Duration overflow on nonsense values.
	const maxSecs = float64(1 << 40)
	if secs > maxSecs {
		secs = maxSecs
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case json.Number:
		return x.Float64()
	}
	return 0, fmt.Errorf("idempotency: replay_ttl_seconds is not numeric")
}

// ReadReplayed returns the envelope's `replayed` flag. Clients use this to
// suppress side effects on replays (billing, analytics, webhooks). When the
// field is absent or non-boolean, ok=false.
func ReadReplayed(envelope []byte) (replayed bool, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(envelope))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return false, false
	}
	v, present := m["replayed"]
	if !present {
		return false, false
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, false
	}
	return b, true
}
