package signing

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// This file is a strict RFC 8941 Structured Field Values parser and
// serializer for the Dictionary fields the Web Bot Auth profile reads:
// Signature-Input, Signature, Signature-Agent, and Accept-Signature.
//
// It is stricter than RFC 8941 in one respect: a Dictionary key or a
// parameter key that appears twice is an error. RFC 8941 keeps the last
// value, which lets two parsers disagree about which value was signed; the
// Python SDK rejects duplicates before parsing for the same reason.

// maxStructuredFieldLen bounds every structured field the WBA verifier
// parses (the Python SDK uses the same 64 KiB limit).
const maxStructuredFieldLen = 64 << 10

var errSFParse = errors.New("structured field does not parse")

type sfKind int

const (
	sfInteger sfKind = iota + 1
	sfDecimal
	sfString
	sfToken
	sfBinary
	sfBoolean
)

// sfBare is an RFC 8941 bare item. A decimal is held as thousandths in i.
type sfBare struct {
	kind sfKind
	i    int64
	s    string
	b    []byte
	bo   bool
}

type sfParam struct {
	key   string
	value sfBare
}

type sfItem struct {
	value  sfBare
	params []sfParam
}

// sfMember is a Dictionary member value: an Item, or an Inner List.
type sfMember struct {
	inner  bool
	item   sfItem
	list   []sfItem
	params []sfParam // Inner List parameters
}

type sfDictEntry struct {
	key    string
	member sfMember
}

type sfDict []sfDictEntry

func sfStringItem(s string) sfBare { return sfBare{kind: sfString, s: s} }
func sfIntItem(i int64) sfBare     { return sfBare{kind: sfInteger, i: i} }
func sfTrue() sfBare               { return sfBare{kind: sfBoolean, bo: true} }

func (d sfDict) get(key string) (sfMember, bool) {
	for _, e := range d {
		if e.key == key {
			return e.member, true
		}
	}
	return sfMember{}, false
}

func (d sfDict) keys() []string {
	out := make([]string, len(d))
	for i, e := range d {
		out[i] = e.key
	}
	return out
}

func sfParamValue(params []sfParam, key string) (sfBare, bool) {
	for _, p := range params {
		if p.key == key {
			return p.value, true
		}
	}
	return sfBare{}, false
}

// stringParam returns the sf-string parameter key, reporting false when it
// is absent or not an sf-string.
func stringParam(params []sfParam, key string) (string, bool) {
	v, ok := sfParamValue(params, key)
	if !ok || v.kind != sfString {
		return "", false
	}
	return v.s, true
}

// intParam returns the sf-integer parameter key.
func intParam(params []sfParam, key string) (int64, bool) {
	v, ok := sfParamValue(params, key)
	if !ok || v.kind != sfInteger {
		return 0, false
	}
	return v.i, true
}

// parseSFDictionary parses an RFC 8941 Dictionary. An empty or oversized
// field, a non-ASCII byte, or a duplicated key or parameter is an error.
func parseSFDictionary(input string) (sfDict, error) {
	if len(input) == 0 || len(input) > maxStructuredFieldLen {
		return nil, errSFParse
	}
	for i := 0; i < len(input); i++ {
		if input[i] > 0x7e || (input[i] < 0x20 && input[i] != '\t') {
			return nil, errSFParse
		}
	}
	p := &sfParser{s: strings.Trim(input, " ")}
	if p.s == "" {
		return nil, errSFParse
	}
	var out sfDict
	seen := map[string]struct{}{}
	for {
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[key]; dup {
			return nil, errSFParse
		}
		seen[key] = struct{}{}
		var m sfMember
		if p.peek() == '=' {
			p.pos++
			m, err = p.itemOrInnerList()
			if err != nil {
				return nil, err
			}
		} else {
			params, err := p.parameters()
			if err != nil {
				return nil, err
			}
			m = sfMember{item: sfItem{value: sfTrue(), params: params}}
		}
		out = append(out, sfDictEntry{key: key, member: m})
		p.ows()
		if p.eof() {
			return out, nil
		}
		if p.peek() != ',' {
			return nil, errSFParse
		}
		p.pos++
		p.ows()
		if p.eof() {
			return nil, errSFParse // trailing comma
		}
	}
}

type sfParser struct {
	s   string
	pos int
}

func (p *sfParser) eof() bool { return p.pos >= len(p.s) }

func (p *sfParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.s[p.pos]
}

func (p *sfParser) ows() {
	for !p.eof() && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

func (p *sfParser) sp() {
	for !p.eof() && p.s[p.pos] == ' ' {
		p.pos++
	}
}

func isLCAlpha(c byte) bool { return c >= 'a' && c <= 'z' }
func isAlpha(c byte) bool   { return isLCAlpha(c) || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool   { return c >= '0' && c <= '9' }

func isTChar(c byte) bool {
	return isAlpha(c) || isDigit(c) || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func (p *sfParser) key() (string, error) {
	if c := p.peek(); !isLCAlpha(c) && c != '*' {
		return "", errSFParse
	}
	start := p.pos
	for !p.eof() {
		c := p.s[p.pos]
		if !isLCAlpha(c) && !isDigit(c) && c != '_' && c != '-' && c != '.' && c != '*' {
			break
		}
		p.pos++
	}
	return p.s[start:p.pos], nil
}

func (p *sfParser) parameters() ([]sfParam, error) {
	var out []sfParam
	for p.peek() == ';' {
		p.pos++
		p.sp()
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		for _, existing := range out {
			if existing.key == key {
				return nil, errSFParse
			}
		}
		value := sfTrue()
		if p.peek() == '=' {
			p.pos++
			value, err = p.bareItem()
			if err != nil {
				return nil, err
			}
		}
		out = append(out, sfParam{key: key, value: value})
	}
	return out, nil
}

func (p *sfParser) itemOrInnerList() (sfMember, error) {
	if p.peek() == '(' {
		return p.innerList()
	}
	it, err := p.item()
	if err != nil {
		return sfMember{}, err
	}
	return sfMember{item: it}, nil
}

func (p *sfParser) innerList() (sfMember, error) {
	p.pos++ // (
	var items []sfItem
	for !p.eof() {
		p.sp()
		if p.peek() == ')' {
			p.pos++
			params, err := p.parameters()
			if err != nil {
				return sfMember{}, err
			}
			return sfMember{inner: true, list: items, params: params}, nil
		}
		it, err := p.item()
		if err != nil {
			return sfMember{}, err
		}
		items = append(items, it)
		if c := p.peek(); c != ' ' && c != ')' {
			return sfMember{}, errSFParse
		}
	}
	return sfMember{}, errSFParse
}

func (p *sfParser) item() (sfItem, error) {
	v, err := p.bareItem()
	if err != nil {
		return sfItem{}, err
	}
	params, err := p.parameters()
	if err != nil {
		return sfItem{}, err
	}
	return sfItem{value: v, params: params}, nil
}

func (p *sfParser) bareItem() (sfBare, error) {
	c := p.peek()
	switch {
	case c == '-' || isDigit(c):
		return p.number()
	case c == '"':
		return p.str()
	case c == ':':
		return p.binary()
	case c == '*' || isAlpha(c):
		return p.token()
	case c == '?':
		return p.boolean()
	default:
		return sfBare{}, errSFParse
	}
}

func (p *sfParser) number() (sfBare, error) {
	neg := false
	if p.peek() == '-' {
		neg = true
		p.pos++
	}
	if !isDigit(p.peek()) {
		return sfBare{}, errSFParse
	}
	start := p.pos
	dot := -1
	for !p.eof() {
		c := p.s[p.pos]
		if isDigit(c) {
			p.pos++
		} else if c == '.' && dot < 0 {
			if p.pos-start > 12 {
				return sfBare{}, errSFParse
			}
			dot = p.pos
			p.pos++
		} else {
			break
		}
		if dot < 0 && p.pos-start > 15 {
			return sfBare{}, errSFParse
		}
		if dot >= 0 && p.pos-start > 16 {
			return sfBare{}, errSFParse
		}
	}
	num := p.s[start:p.pos]
	if dot < 0 {
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return sfBare{}, errSFParse
		}
		if neg {
			n = -n
		}
		return sfBare{kind: sfInteger, i: n}, nil
	}
	intPart, frac := num[:dot-start], num[dot-start+1:]
	if frac == "" || len(frac) > 3 {
		return sfBare{}, errSFParse
	}
	for len(frac) < 3 {
		frac += "0"
	}
	n, err := strconv.ParseInt(intPart+frac, 10, 64)
	if err != nil {
		return sfBare{}, errSFParse
	}
	if neg {
		n = -n
	}
	return sfBare{kind: sfDecimal, i: n}, nil
}

func (p *sfParser) str() (sfBare, error) {
	p.pos++ // "
	var b strings.Builder
	for !p.eof() {
		c := p.s[p.pos]
		p.pos++
		switch {
		case c == '\\':
			if p.eof() {
				return sfBare{}, errSFParse
			}
			next := p.s[p.pos]
			if next != '"' && next != '\\' {
				return sfBare{}, errSFParse
			}
			b.WriteByte(next)
			p.pos++
		case c == '"':
			return sfStringItem(b.String()), nil
		case c < 0x20 || c > 0x7e:
			return sfBare{}, errSFParse
		default:
			b.WriteByte(c)
		}
	}
	return sfBare{}, errSFParse
}

func (p *sfParser) token() (sfBare, error) {
	start := p.pos
	p.pos++
	for !p.eof() {
		c := p.s[p.pos]
		if !isTChar(c) && c != ':' && c != '/' {
			break
		}
		p.pos++
	}
	return sfBare{kind: sfToken, s: p.s[start:p.pos]}, nil
}

func (p *sfParser) binary() (sfBare, error) {
	p.pos++ // :
	end := strings.IndexByte(p.s[p.pos:], ':')
	if end < 0 {
		return sfBare{}, errSFParse
	}
	enc := p.s[p.pos : p.pos+end]
	p.pos += end + 1
	for i := 0; i < len(enc); i++ {
		c := enc[i]
		if !isAlpha(c) && !isDigit(c) && c != '+' && c != '/' && c != '=' {
			return sfBare{}, errSFParse
		}
	}
	// RFC 8941 §4.2.7: parsers SHOULD NOT fail when padding is absent.
	if rem := len(strings.TrimRight(enc, "=")) % 4; rem != 0 && !strings.Contains(enc, "=") {
		enc += strings.Repeat("=", 4-rem)
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return sfBare{}, errSFParse
	}
	return sfBare{kind: sfBinary, b: b}, nil
}

func (p *sfParser) boolean() (sfBare, error) {
	p.pos++ // ?
	switch p.peek() {
	case '1':
		p.pos++
		return sfTrue(), nil
	case '0':
		p.pos++
		return sfBare{kind: sfBoolean}, nil
	default:
		return sfBare{}, errSFParse
	}
}

// Serialization (RFC 8941 §4.1).

func (v sfBare) serialize() string {
	switch v.kind {
	case sfInteger:
		return strconv.FormatInt(v.i, 10)
	case sfDecimal:
		sign := ""
		n := v.i
		if n < 0 {
			sign, n = "-", -n
		}
		frac := strings.TrimRight(strconv.FormatInt(n%1000+1000, 10)[1:], "0")
		if frac == "" {
			frac = "0"
		}
		return sign + strconv.FormatInt(n/1000, 10) + "." + frac
	case sfString:
		var b strings.Builder
		b.WriteByte('"')
		for i := 0; i < len(v.s); i++ {
			if v.s[i] == '"' || v.s[i] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(v.s[i])
		}
		b.WriteByte('"')
		return b.String()
	case sfToken:
		return v.s
	case sfBinary:
		return ":" + base64.StdEncoding.EncodeToString(v.b) + ":"
	case sfBoolean:
		if v.bo {
			return "?1"
		}
		return "?0"
	}
	return ""
}

func serializeParams(params []sfParam) string {
	var b strings.Builder
	for _, p := range params {
		b.WriteByte(';')
		b.WriteString(p.key)
		if p.value.kind == sfBoolean && p.value.bo {
			continue
		}
		b.WriteByte('=')
		b.WriteString(p.value.serialize())
	}
	return b.String()
}

func (it sfItem) serialize() string { return it.value.serialize() + serializeParams(it.params) }

func (m sfMember) serialize() string {
	if !m.inner {
		return m.item.serialize()
	}
	parts := make([]string, len(m.list))
	for i, it := range m.list {
		parts[i] = it.serialize()
	}
	return "(" + strings.Join(parts, " ") + ")" + serializeParams(m.params)
}

func (d sfDict) serialize() string {
	parts := make([]string, len(d))
	for i, e := range d {
		m := e.member
		if !m.inner && m.item.value.kind == sfBoolean && m.item.value.bo {
			parts[i] = e.key + serializeParams(m.item.params)
			continue
		}
		parts[i] = e.key + "=" + m.serialize()
	}
	return strings.Join(parts, ", ")
}
