package signing

import (
	"fmt"
	"strconv"
	"strings"
)

// sigInput is the parsed form of a single label in a Signature-Input header.
type sigInput struct {
	label      string
	components []string // ordered covered-component names, inside the parens
	paramsText string   // verbatim sig-params serialization including leading "(...)"
	created    int64
	createdSet bool
	expires    int64
	expiresSet bool
	nonce      string
	nonceSet   bool
	keyID      string
	keyIDSet   bool
	alg        string
	algSet     bool
	tag        string
	tagSet     bool
	dupParam   string // set if any param appeared more than once
}

// parseSignatureInput parses a Signature-Input header value and returns the
// first label's entry. Additional labels are ignored per AdCP profile §
// "One signature per request".
//
// Returns CodeHeaderMalformed on syntactic errors.
func parseSignatureInput(header string) (*sigInput, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, newError(CodeHeaderMalformed, "empty Signature-Input header")
	}
	if len(header) > maxSignatureInputLen {
		return nil, newError(CodeHeaderMalformed, "Signature-Input header too long")
	}

	// Locate the first "=" that introduces a dict entry's value.
	eq := strings.IndexByte(header, '=')
	if eq <= 0 {
		return nil, newError(CodeHeaderMalformed, "missing = in Signature-Input")
	}
	label := strings.TrimSpace(header[:eq])
	if !isValidLabel(label) {
		return nil, newError(CodeHeaderMalformed, "invalid label in Signature-Input")
	}
	rest := header[eq+1:]

	// The value must start with "(".
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "(") {
		return nil, newError(CodeHeaderMalformed, "expected '(' after label")
	}

	// Find matching ")" at depth 0 while tracking quoted strings.
	close := findMatchingClose(rest)
	if close < 0 {
		return nil, newError(CodeHeaderMalformed, "unterminated covered components")
	}

	inside := rest[1:close]
	components, err := parseComponentList(inside)
	if err != nil {
		return nil, err
	}

	// After the ")" come optional ";"-delimited params up to next dict entry "," or end.
	afterClose := rest[close+1:]
	paramsEnd := findParamsEnd(afterClose)
	paramsRaw := afterClose[:paramsEnd]

	out := &sigInput{
		label:      label,
		components: components,
		paramsText: rest[:close+1] + paramsRaw, // "(...)...;…" — the @signature-params value
	}

	// Parse params.
	if err := out.parseParams(paramsRaw); err != nil {
		return nil, err
	}

	// If there are additional dict entries, verify they don't repeat the chosen
	// label — RFC 8941 dict semantics would keep the last value, but that breaks
	// the first-label-wins contract of this parser.
	remaining := afterClose[paramsEnd:]
	if remaining != "" {
		if hasLabel(remaining, label) {
			return nil, newError(CodeHeaderMalformed, "duplicate label in Signature-Input")
		}
	}
	return out, nil
}

// hasLabel returns true if s contains a dict entry with the given label.
// s is the remainder of a Signature-Input header starting at or after a comma.
func hasLabel(s, want string) bool {
	rest := s
	for len(rest) > 0 {
		for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == ',') {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			break
		}
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			return false
		}
		label := strings.TrimSpace(rest[:eq])
		if label == want {
			return true
		}
		rest = rest[eq+1:]
		// Skip the (...) inner list.
		rest = strings.TrimLeft(rest, " \t")
		if !strings.HasPrefix(rest, "(") {
			return false
		}
		close := findMatchingClose(rest)
		if close < 0 {
			return false
		}
		rest = rest[close+1:]
		// Skip params up to next top-level ",".
		rest = rest[findParamsEnd(rest):]
	}
	return false
}

// parseComponentList parses the inside of (...) — space-separated quoted strings.
func parseComponentList(inside string) ([]string, error) {
	var comps []string
	i := 0
	for i < len(inside) {
		if len(comps) > maxCoveredComponents {
			return nil, newError(CodeHeaderMalformed, "too many covered components")
		}
		// Skip whitespace.
		for i < len(inside) && (inside[i] == ' ' || inside[i] == '\t') {
			i++
		}
		if i >= len(inside) {
			break
		}
		if inside[i] != '"' {
			return nil, newError(CodeHeaderMalformed, "component not quoted")
		}
		// Find closing quote.
		end := i + 1
		for end < len(inside) && inside[end] != '"' {
			if inside[end] == '\\' {
				// Escaped char — skip next byte.
				end += 2
				continue
			}
			end++
		}
		if end >= len(inside) {
			return nil, newError(CodeHeaderMalformed, "unterminated component string")
		}
		comps = append(comps, inside[i+1:end])
		i = end + 1
	}
	return comps, nil
}

// findMatchingClose returns the index of the ")" that closes the opening "(".
// Handles quoted-string nesting. Input must start with "(".
func findMatchingClose(s string) int {
	if len(s) == 0 || s[0] != '(' {
		return -1
	}
	inQuote := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			i++ // skip escaped
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == ')':
			return i
		}
	}
	return -1
}

// findParamsEnd returns the index in s where the current dict entry's params end.
// The entry ends at a top-level "," (outside quotes) or at end of string.
func findParamsEnd(s string) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == ',':
			return i
		}
	}
	return len(s)
}

func (si *sigInput) parseParams(raw string) error {
	// raw starts either empty or with ";name=value;name=value..."
	if raw == "" {
		return nil
	}
	if raw[0] != ';' {
		return newError(CodeHeaderMalformed, "params must start with ;")
	}
	rest := raw
	for len(rest) > 0 {
		if rest[0] != ';' {
			return newError(CodeHeaderMalformed, "expected ; between params")
		}
		rest = rest[1:]
		// name
		eq := indexParamSeparator(rest)
		if eq < 0 {
			return newError(CodeHeaderMalformed, "param missing =")
		}
		name := strings.TrimSpace(rest[:eq])
		rest = rest[eq+1:]
		val, consumed, err := readParamValue(rest)
		if err != nil {
			return err
		}
		rest = rest[consumed:]
		if err := si.setParam(name, val); err != nil {
			return err
		}
	}
	return nil
}

// indexParamSeparator finds the first "=" that separates name=value for a
// param, respecting quoted strings.
func indexParamSeparator(s string) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == '=':
			return i
		case !inQuote && c == ';':
			return -1
		}
	}
	return -1
}

type paramValue struct {
	isString bool
	isInt    bool
	str      string
	intv     int64
}

// readParamValue reads one param value from the start of s and returns the
// parsed value plus the number of bytes consumed.
func readParamValue(s string) (paramValue, int, error) {
	if len(s) == 0 {
		return paramValue{}, 0, newError(CodeHeaderMalformed, "empty param value")
	}
	if s[0] == '"' {
		// Quoted string.
		end := 1
		for end < len(s) && s[end] != '"' {
			if s[end] == '\\' && end+1 < len(s) {
				end += 2
				continue
			}
			end++
		}
		if end >= len(s) {
			return paramValue{}, 0, newError(CodeHeaderMalformed, "unterminated param string")
		}
		return paramValue{isString: true, str: s[1:end]}, end + 1, nil
	}
	// Integer or token.
	end := 0
	for end < len(s) && s[end] != ';' && s[end] != ',' {
		end++
	}
	tok := strings.TrimSpace(s[:end])
	if tok == "" {
		return paramValue{}, 0, newError(CodeHeaderMalformed, "empty unquoted param value")
	}
	// Try integer.
	if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
		return paramValue{isInt: true, intv: n}, end, nil
	}
	// Bare token.
	return paramValue{isString: true, str: tok}, end, nil
}

func (si *sigInput) setParam(name string, v paramValue) error {
	check := func(set *bool) error {
		if *set {
			si.dupParam = name
			return newError(CodeHeaderMalformed, fmt.Sprintf("duplicate param %q", name))
		}
		*set = true
		return nil
	}
	switch name {
	case "created":
		if err := check(&si.createdSet); err != nil {
			return err
		}
		if !v.isInt {
			return newError(CodeHeaderMalformed, "created must be integer")
		}
		si.created = v.intv
	case "expires":
		if err := check(&si.expiresSet); err != nil {
			return err
		}
		if !v.isInt {
			return newError(CodeHeaderMalformed, "expires must be integer")
		}
		si.expires = v.intv
	case "nonce":
		if err := check(&si.nonceSet); err != nil {
			return err
		}
		if !v.isString {
			return newError(CodeHeaderMalformed, "nonce must be string")
		}
		if len(v.str) > maxNonceLen {
			return newError(CodeHeaderMalformed, "nonce too long")
		}
		si.nonce = v.str
	case "keyid":
		if err := check(&si.keyIDSet); err != nil {
			return err
		}
		if !v.isString {
			return newError(CodeHeaderMalformed, "keyid must be string")
		}
		if len(v.str) > maxKeyIDLen {
			return newError(CodeHeaderMalformed, "keyid too long")
		}
		si.keyID = v.str
	case "alg":
		if err := check(&si.algSet); err != nil {
			return err
		}
		if !v.isString {
			return newError(CodeHeaderMalformed, "alg must be string")
		}
		si.alg = v.str
	case "tag":
		if err := check(&si.tagSet); err != nil {
			return err
		}
		if !v.isString {
			return newError(CodeHeaderMalformed, "tag must be string")
		}
		si.tag = v.str
	default:
		// Unknown param — ignore. RFC 9421 permits extension parameters; the
		// AdCP profile does not forbid them.
	}
	return nil
}

func isValidLabel(label string) bool {
	if label == "" {
		return false
	}
	// Per RFC 8941 "key" grammar: lcalpha / DIGIT / "_" / "-" / "." / "*", and
	// first char must be lcalpha or "*". We accept a slightly broader set since
	// the only labels we see in practice are sig1, sig2, etc.
	first := label[0]
	if !((first >= 'a' && first <= 'z') || first == '*') {
		return false
	}
	for i := 1; i < len(label); i++ {
		c := label[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == '*') {
			return false
		}
	}
	return true
}

// parseSignature parses a Signature header value and returns the raw signature
// bytes for the given label.
func parseSignature(header, label string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", newError(CodeHeaderMalformed, "empty Signature header")
	}
	if len(header) > maxSignatureLen {
		return "", newError(CodeHeaderMalformed, "Signature header too long")
	}

	// Iterate over dict entries.
	rest := header
	for len(rest) > 0 {
		// Skip leading OWS and commas.
		for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == ',') {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			break
		}
		// Parse label up to '='.
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			return "", newError(CodeHeaderMalformed, "Signature missing =")
		}
		lbl := strings.TrimSpace(rest[:eq])
		rest = rest[eq+1:]
		// Byte sequence is wrapped in ":...:"
		rest = strings.TrimLeft(rest, " \t")
		if len(rest) == 0 || rest[0] != ':' {
			return "", newError(CodeHeaderMalformed, "Signature value missing :")
		}
		end := strings.IndexByte(rest[1:], ':')
		if end < 0 {
			return "", newError(CodeHeaderMalformed, "Signature value unterminated")
		}
		val := rest[1 : 1+end]
		if lbl == label {
			return val, nil
		}
		rest = rest[2+end:]
	}
	return "", newError(CodeHeaderMalformed, "label not found in Signature header")
}
