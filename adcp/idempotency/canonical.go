// Package idempotency provides canonicalization, hashing, typed errors,
// storage backends, and handler middleware for AdCP idempotency_key support.
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// HashFn canonicalizes a JSON payload and returns a stable digest.
// Implementations MUST produce byte-identical output for semantically equal
// inputs so hash comparison correctly detects payload drift.
type HashFn func(payload []byte) (string, error)

// DefaultExcludePaths are top-level JSON fields removed before canonicalization.
// These are either the idempotency key itself (tautological) or transport-layer
// fields that legitimately vary between retries without altering request intent.
var DefaultExcludePaths = []string{
	"idempotency_key",
	"context",
	"governance_context",
	"push_notification_config.authentication.credentials",
}

// CanonicalJSONSHA256 is the default HashFn: RFC 8785 JCS canonicalization
// followed by SHA-256. The top-level exclude list is applied before hashing.
var CanonicalJSONSHA256 = NewCanonicalJSONSHA256(DefaultExcludePaths)

// NewCanonicalJSONSHA256 returns a HashFn that strips the given dotted JSON
// paths, canonicalizes the remainder with JCS (RFC 8785), and SHA-256 hashes
// the result. Dotted paths apply from the document root (e.g.,
// "push_notification_config.authentication.credentials").
func NewCanonicalJSONSHA256(excludePaths []string) HashFn {
	paths := compileExcludePaths(excludePaths)
	return func(payload []byte) (string, error) {
		var v any
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return "", fmt.Errorf("idempotency: decode payload: %w", err)
		}
		v = stripPaths(v, paths, nil)
		canon, err := canonicalize(v)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(canon)
		return hex.EncodeToString(sum[:]), nil
	}
}

type excludeNode struct {
	children map[string]*excludeNode
	leaf     bool
}

func compileExcludePaths(paths []string) *excludeNode {
	root := &excludeNode{children: map[string]*excludeNode{}}
	for _, p := range paths {
		parts := strings.Split(p, ".")
		cur := root
		for _, part := range parts {
			child, ok := cur.children[part]
			if !ok {
				child = &excludeNode{children: map[string]*excludeNode{}}
				cur.children[part] = child
			}
			cur = child
		}
		cur.leaf = true
	}
	return root
}

func stripPaths(v any, node *excludeNode, path []string) any {
	if node == nil {
		return v
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, child := range m {
		cn := node.children[k]
		if cn != nil && cn.leaf {
			continue
		}
		if cn != nil && len(cn.children) > 0 {
			out[k] = stripPaths(child, cn, append(path, k))
			continue
		}
		out[k] = child
	}
	return out
}

// canonicalize emits RFC 8785 JCS form for a value decoded with UseNumber.
func canonicalize(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		return writeJSONString(buf, x)
	case json.Number:
		return writeCanonicalNumber(buf, string(x))
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			if !utf8.ValidString(k) {
				return fmt.Errorf("idempotency: object key is not valid UTF-8")
			}
			keys = append(keys, k)
		}
		sortUTF16(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("idempotency: unsupported canonicalization type %T", v)
	}
	return nil
}

// sortUTF16 sorts strings by their UTF-16 code-unit sequences, as required by RFC 8785.
func sortUTF16(s []string) {
	sort.Slice(s, func(i, j int) bool {
		a := utf16.Encode([]rune(s[i]))
		b := utf16.Encode([]rune(s[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

// writeJSONString applies the RFC 8785 string serialization rules. Invalid
// UTF-8 is rejected so two payloads with distinct byte sequences cannot
// canonicalize to the same form via silent U+FFFD substitution.
func writeJSONString(buf *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("idempotency: string value is not valid UTF-8")
	}
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
	return nil
}

// writeCanonicalNumber formats a number in ECMAScript Number.prototype.toString
// form, matching RFC 8785 §3.2.2.3.
func writeCanonicalNumber(buf *bytes.Buffer, numStr string) error {
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return fmt.Errorf("idempotency: invalid number %q: %w", numStr, err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return errors.New("idempotency: NaN/Inf not allowed in JCS")
	}
	if f == 0 {
		buf.WriteByte('0')
		return nil
	}
	buf.WriteString(formatES6(f))
	return nil
}

// formatES6 reproduces ECMAScript Number.prototype.toString for finite nonzero doubles.
func formatES6(f float64) string {
	// Shortest-round-trip decimal.
	s := strconv.FormatFloat(f, 'e', -1, 64)
	// s is of the form "dddd.ddde±dd" or "de±dd".
	mantissa, expPart, ok := strings.Cut(s, "e")
	if !ok {
		return s
	}
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return s
	}

	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = mantissa[1:]
	}

	intPart, fracPart, _ := strings.Cut(mantissa, ".")
	digits := intPart + fracPart
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		digits = "0"
	}
	// decExp = exponent of the most significant digit relative to the decimal point.
	decExp := exp - len(fracPart) + len(digits) - 1

	// ES6 chooses fixed notation when exponent is in [-6, 20].
	if decExp >= -6 && decExp < 21 {
		return sign + fixedDecimal(digits, decExp)
	}
	// Exponential form: leading digit, optional .rest, 'e', sign, exponent (no leading zero).
	var out strings.Builder
	out.WriteString(sign)
	out.WriteByte(digits[0])
	if len(digits) > 1 {
		out.WriteByte('.')
		out.WriteString(digits[1:])
	}
	out.WriteByte('e')
	if decExp >= 0 {
		out.WriteByte('+')
	}
	out.WriteString(strconv.Itoa(decExp))
	return out.String()
}

func fixedDecimal(digits string, decExp int) string {
	if decExp < 0 {
		// 0.000...digits
		var b strings.Builder
		b.WriteString("0.")
		for i := 0; i < -decExp-1; i++ {
			b.WriteByte('0')
		}
		b.WriteString(digits)
		return b.String()
	}
	// decExp >= 0: move the implicit point right of position decExp.
	if decExp+1 >= len(digits) {
		// Pad with zeros; no decimal point.
		var b strings.Builder
		b.WriteString(digits)
		for i := 0; i < decExp+1-len(digits); i++ {
			b.WriteByte('0')
		}
		return b.String()
	}
	// Insert a decimal point after decExp+1 digits.
	return digits[:decExp+1] + "." + digits[decExp+1:]
}
