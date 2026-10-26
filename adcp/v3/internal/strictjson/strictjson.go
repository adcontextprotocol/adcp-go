// Package strictjson validates JSON documents that carry authority: trust
// records, key directories, revocation lists, and Web Bot Auth signed
// bodies. Go's encoding/json keeps the last of two duplicate object keys and
// matches struct fields case-insensitively, while other parsers (including
// the MCP SDK, which decodes case-sensitively) may keep the first or match
// exactly. A document with keys that are equal, or equal under Unicode case
// folding, could therefore mean different things to different readers.
// Validate rejects it before any field is read.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// DefaultMaxDepth is the nesting limit AdCP applies to trust records
// (security.mdx, Web Bot Auth identities: "nesting depth 32").
const DefaultMaxDepth = 32

// ErrInvalid is wrapped by every validation failure.
var ErrInvalid = errors.New("strictjson: invalid document")

// Validate reports whether data is exactly one JSON value with no duplicate
// object keys and nesting no deeper than maxDepth. Keys are compared after
// unescaping and under Unicode simple case folding, the comparison
// encoding/json uses to match struct fields, so "name" and "Name" in one
// object are duplicates. maxDepth <= 0 selects DefaultMaxDepth.
func Validate(data []byte, maxDepth int) error {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	if !json.Valid(data) {
		return fmt.Errorf("%w: not valid JSON", ErrInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type frame struct {
		object  bool
		keys    map[string]struct{}
		wantKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				continue
			}
			key, ok := tok.(string)
			if !ok {
				return fmt.Errorf("%w: object key is not a string", ErrInvalid)
			}
			folded := Fold(key)
			if _, dup := top.keys[folded]; dup {
				return fmt.Errorf("%w: duplicate object key (keys are compared case-insensitively)", ErrInvalid)
			}
			top.keys[folded] = struct{}{}
			top.wantKey = false
			continue
		}
		if top != nil && top.object {
			top.wantKey = true
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				if len(stack) >= maxDepth {
					return fmt.Errorf("%w: nesting deeper than %d", ErrInvalid, maxDepth)
				}
				stack = append(stack, &frame{object: d == '{', keys: map[string]struct{}{}, wantKey: d == '{'})
			case ']', '}':
				stack = stack[:len(stack)-1]
			}
		}
	}
	return nil
}

// Fold maps s to a canonical form such that Fold(a) == Fold(b) exactly when
// strings.EqualFold(a, b): each rune becomes the smallest rune in its
// Unicode simple-folding orbit.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < m {
				m = f
			}
		}
		b.WriteRune(m)
	}
	return b.String()
}

// Unmarshal validates data with Validate, then decodes it into v.
func Unmarshal(data []byte, maxDepth int, v any) error {
	if err := Validate(data, maxDepth); err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}
