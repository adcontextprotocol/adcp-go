package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

// The maintained gowebpki/jcs implementation uses insertion sorting for each
// object. Bound object width and aggregate members before invoking it so a
// valid but adversarial 64 KiB request cannot spend the provider latency budget
// in quadratic sorting. Exceeding these cache-only bounds is not a protocol
// error: the router forwards the request uncached.
const (
	MaxContextHashObjectMembers      = 64
	MaxContextHashTotalObjectMembers = 2048
	MaxContextHashNestingDepth       = 64
)

// ErrContextHashComplexity means a valid provider-forwarded request exceeds
// the cache canonicalization work bounds. Callers must bypass caching rather
// than reject the request or report a provider failure.
var ErrContextHashComplexity = errors.New("context hash: canonicalization complexity limit exceeded")

type contextJSONLimits struct {
	objectMembers int
	totalMembers  int
	depth         int
}

// ContextHash computes the Context Match response-cache context_hash from the
// exact validated request a provider will receive. It removes only the two
// top-level envelope fields that may vary without affecting the result,
// $schema and request_id, then applies RFC 8785 JCS and SHA-256. In particular,
// array order and every other forwarded field remain significant.
//
// The function returns ErrContextHashComplexity when the input is valid but
// too wide/deep for bounded use of the maintained JCS dependency. That outcome
// is a cache bypass, not request invalidity.
func ContextHash(forwardedRequest []byte) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	document, err := decodeContextJSONObject(forwardedRequest, contextJSONLimits{
		objectMembers: MaxContextHashObjectMembers,
		totalMembers:  MaxContextHashTotalObjectMembers,
		depth:         MaxContextHashNestingDepth,
	}, true)
	if err != nil {
		if errors.Is(err, ErrContextHashComplexity) {
			return zero, err
		}
		return zero, errors.New("context hash: request is not valid I-JSON")
	}
	delete(document, "$schema")
	delete(document, "request_id")

	stripped, err := json.Marshal(document)
	if err != nil {
		return zero, fmt.Errorf("context hash: serialize stripped request: %w", err)
	}
	canonical, err := jcs.Transform(stripped)
	if err != nil {
		return zero, fmt.Errorf("context hash: canonicalize request: %w", err)
	}
	return sha256.Sum256(canonical), nil
}

// validateContextIngressIJSON rejects representations that encoding/json would
// otherwise normalize ambiguously before the router constructs the exact
// provider-forwarded body: invalid UTF-8, unpaired surrogates, duplicate member
// names (including escaped equivalents), trailing data, and malformed JSON.
// Cache complexity limits do not apply at ingress; the independent 64 KiB HTTP
// body limit bounds this linear validation pass.
func validateContextIngressIJSON(raw []byte) error {
	_, err := decodeContextJSONObject(raw, contextJSONLimits{}, false)
	return err
}

type contextJSONDecoder struct {
	dec              *json.Decoder
	limits           contextJSONLimits
	totalMembers     int
	requireJCSNumber bool
}

func decodeContextJSONObject(raw []byte, limits contextJSONLimits, requireJCSNumber bool) (map[string]any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	if !validJSONSurrogates(raw) {
		return nil, errors.New("invalid Unicode surrogate")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := contextJSONDecoder{dec: decoder, limits: limits, requireJCSNumber: requireJCSNumber}
	value, err := state.decodeValue(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON data")
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("request must be an object")
	}
	return document, nil
}

func (d *contextJSONDecoder) decodeValue(depth int) (any, error) {
	if d.limits.depth > 0 && depth > d.limits.depth {
		return nil, ErrContextHashComplexity
	}
	token, err := d.dec.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return d.decodeObject(depth + 1)
		case '[':
			return d.decodeArray(depth + 1)
		default:
			return nil, errors.New("unexpected closing delimiter")
		}
	case json.Number:
		if d.requireJCSNumber {
			n, err := strconv.ParseFloat(string(value), 64)
			if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
				return nil, errors.New("number is outside JCS binary64 domain")
			}
		}
		return value, nil
	case nil, bool, string:
		// Noncharacters are preserved rather than promoted into an additional
		// router rejection policy. encoding/json and JCS canonicalize their
		// direct and escaped forms deterministically; tests pin that behavior.
		return value, nil
	default:
		return nil, errors.New("unexpected JSON token")
	}
}

func (d *contextJSONDecoder) decodeObject(depth int) (map[string]any, error) {
	result := make(map[string]any)
	members := 0
	for d.dec.More() {
		keyToken, err := d.dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object member name is not a string")
		}
		members++
		d.totalMembers++
		if (d.limits.objectMembers > 0 && members > d.limits.objectMembers) ||
			(d.limits.totalMembers > 0 && d.totalMembers > d.limits.totalMembers) {
			return nil, ErrContextHashComplexity
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("duplicate object member")
		}
		value, err := d.decodeValue(depth)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	closing, err := d.dec.Token()
	if err != nil {
		return nil, err
	}
	if closing != json.Delim('}') {
		return nil, errors.New("object is not terminated")
	}
	return result, nil
}

func (d *contextJSONDecoder) decodeArray(depth int) ([]any, error) {
	result := make([]any, 0)
	for d.dec.More() {
		value, err := d.decodeValue(depth)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	closing, err := d.dec.Token()
	if err != nil {
		return nil, err
	}
	if closing != json.Delim(']') {
		return nil, errors.New("array is not terminated")
	}
	return result, nil
}

// validJSONSurrogates rejects lone or incorrectly paired UTF-16 \u escapes.
// encoding/json replaces those with U+FFFD, while I-JSON and RFC 8785 require
// Unicode scalar values and therefore require rejection before decoding.
func validJSONSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw); i++ {
			switch raw[i] {
			case '"':
				goto nextString
			case '\\':
				if i+1 >= len(raw) {
					return false
				}
				i++
				if raw[i] != 'u' {
					continue
				}
				first, ok := jsonHex16(raw, i+1)
				if !ok {
					return false
				}
				i += 4
				switch {
				case first >= 0xd800 && first <= 0xdbff:
					if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
						return false
					}
					second, ok := jsonHex16(raw, i+3)
					if !ok || second < 0xdc00 || second > 0xdfff {
						return false
					}
					i += 6
				case first >= 0xdc00 && first <= 0xdfff:
					return false
				}
			}
		}
	nextString:
	}
	return true
}

func jsonHex16(raw []byte, start int) (uint64, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(raw[start:start+4]), 16, 16)
	return n, err == nil
}
