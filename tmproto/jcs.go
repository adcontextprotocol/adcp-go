package tmproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
)

// jcsMarshal serializes v as RFC 8785 JSON Canonicalization Scheme bytes.
//
// Used by the TMP request-signing envelope to canonicalize identity-match
// signing inputs. Object keys are sorted by UTF-16 code-unit value; strings
// use the minimal RFC 8259 escape set (control chars, quote, backslash); arrays
// preserve order; numbers use ECMAScript Number.toString. Floating-point
// formatting matches ECMAScript output for the integer values TMP signing
// inputs carry in practice — TMP fields that go through JCS do not contain
// non-integer floats.
func jcsMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := jcsEncode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jcsValue converts an arbitrary JSON-serializable Go value into the
// map[string]any / []any / primitive shape jcsMarshal accepts, routing through
// encoding/json with UseNumber so integer values survive canonicalization
// without acquiring a float representation. This lets canonical hashing cover
// complete typed objects (e.g. an identity with its attestation) using exactly
// the fields the struct serializes, instead of hand-building parallel maps that
// could silently drift from the wire shape.
func jcsValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func jcsEncode(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case string:
		jcsEncodeString(buf, x)
		return nil
	case int:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
		return nil
	case int32:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
		return nil
	case int64:
		buf.WriteString(strconv.FormatInt(x, 10))
		return nil
	case uint:
		buf.WriteString(strconv.FormatUint(uint64(x), 10))
		return nil
	case uint32:
		buf.WriteString(strconv.FormatUint(uint64(x), 10))
		return nil
	case uint64:
		buf.WriteString(strconv.FormatUint(x, 10))
		return nil
	case float32:
		return jcsEncodeNumber(buf, float64(x))
	case float64:
		return jcsEncodeNumber(buf, x)
	case json.Number:
		return jcsEncodeJSONNumber(buf, x)
	case []any:
		return jcsEncodeArray(buf, x)
	case []string:
		conv := make([]any, len(x))
		for i, s := range x {
			conv[i] = s
		}
		return jcsEncodeArray(buf, conv)
	case map[string]any:
		return jcsEncodeObject(buf, x)
	}
	return fmt.Errorf("tmproto: jcs cannot encode value of type %T", v)
}

func jcsEncodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
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
			if c < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, c)
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}

func jcsEncodeNumber(buf *bytes.Buffer, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("tmproto: jcs forbids non-finite number")
	}
	if f == 0 {
		buf.WriteByte('0')
		return nil
	}
	// Integer fast path — exact in the IEEE-754 safe-integer range.
	if f == math.Trunc(f) && f >= -(1<<53) && f <= (1<<53) {
		buf.WriteString(strconv.FormatInt(int64(f), 10))
		return nil
	}
	// Non-integer floats require ECMA-262 7.1.12.1 number-to-string
	// canonicalization, which Go's strconv.FormatFloat does not exactly
	// reproduce. TMP signing inputs do not carry non-integer floats today;
	// surfacing an error keeps two implementations from diverging silently
	// when one starts emitting them.
	return fmt.Errorf("tmproto: jcs non-integer floats are unsupported; only integers are canonicalized today")
}

func jcsEncodeJSONNumber(buf *bytes.Buffer, n json.Number) error {
	if i, err := n.Int64(); err == nil {
		buf.WriteString(strconv.FormatInt(i, 10))
		return nil
	}
	if f, err := n.Float64(); err == nil {
		return jcsEncodeNumber(buf, f)
	}
	return fmt.Errorf("tmproto: jcs cannot parse json.Number")
}

func jcsEncodeArray(buf *bytes.Buffer, a []any) error {
	buf.WriteByte('[')
	for i, e := range a {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := jcsEncode(buf, e); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func jcsEncodeObject(buf *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return jcsCompareKeys(keys[i], keys[j]) < 0
	})
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		jcsEncodeString(buf, k)
		buf.WriteByte(':')
		if err := jcsEncode(buf, m[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// jcsCompareKeys compares two object keys per RFC 8785 §3.2.3:
// by UTF-16 code-unit value. ASCII keys reduce to byte-order comparison.
func jcsCompareKeys(a, b string) int {
	if a == b {
		return 0
	}
	if jcsIsASCII(a) && jcsIsASCII(b) {
		if a < b {
			return -1
		}
		return 1
	}
	au := utf16.Encode([]rune(a))
	bu := utf16.Encode([]rune(b))
	n := min(len(bu), len(au))
	for i := range n {
		if au[i] != bu[i] {
			if au[i] < bu[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(au) < len(bu):
		return -1
	case len(au) > len(bu):
		return 1
	}
	return 0
}

func jcsIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
