package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

// ContextHash computes the Context Match response-cache context_hash from the
// exact validated request a provider will receive. It removes only the two
// top-level envelope fields that may vary without affecting the result,
// $schema and request_id, then applies RFC 8785 JCS and SHA-256. In particular,
// array order and every other forwarded field remain significant.
func ContextHash(forwardedRequest []byte) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if !utf8.Valid(forwardedRequest) {
		return zero, errors.New("context hash: request is not valid UTF-8")
	}
	if !validJSONSurrogates(forwardedRequest) {
		return zero, errors.New("context hash: request contains an invalid Unicode surrogate")
	}
	// Validate the original representation before decoding it into a map.
	// gowebpki/jcs rejects trailing values, duplicate object keys, malformed
	// JSON, and invalid/non-finite numbers. This prevents encoding/json's
	// duplicate-key last-write behavior from creating ambiguous preimages.
	if _, err := jcs.Transform(forwardedRequest); err != nil {
		return zero, errors.New("context hash: request is not valid I-JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(forwardedRequest))
	dec.UseNumber()
	var document map[string]any
	if err := dec.Decode(&document); err != nil {
		return zero, fmt.Errorf("context hash: decode request: %w", err)
	}
	if document == nil {
		return zero, errors.New("context hash: request must be an object")
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
