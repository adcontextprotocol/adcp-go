package signing

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// canonicalTargetURI applies the AdCP @target-uri canonicalization:
//
//  1. Lowercase scheme.
//  2. Lowercase host; IDN → Punycode (url.Parse handles Punycode already).
//  3. Strip userinfo.
//  4. Strip default ports (:443 for https, :80 for http).
//  5. remove_dot_segments on path; empty path with authority becomes "/".
//  6. Normalize percent-encoding: uppercase hex; leave reserved encoded.
//  7. Preserve query byte-for-byte.
//  8. Strip fragment.
//
// rawURL is the URL as received on the wire. For outgoing requests the signer
// supplies the URL string it will send; for incoming, the verifier reconstructs
// it from scheme + Host + RequestURI.
func canonicalTargetURI(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("scheme %q not supported", u.Scheme)
	}

	host := u.Hostname()
	if err := validateASCIIHost(host); err != nil {
		return "", err
	}
	host = strings.ToLower(host)
	port := u.Port()
	authority := bracketIfIPv6(host)
	if port != "" && !isDefaultPort(scheme, port) {
		authority = authority + ":" + port
	}

	// Prefer RawPath when it differs from Path (i.e., original had
	// percent-encoded bytes we must not lose). Otherwise use Path.
	pathRaw := u.RawPath
	if pathRaw == "" {
		pathRaw = u.Path
	}
	normalizedPath := normalizePercentEncoding(removeDotSegments(pathRaw))
	if normalizedPath == "" {
		normalizedPath = "/"
	}

	canonical := scheme + "://" + authority + normalizedPath
	if u.RawQuery != "" {
		canonical += "?" + u.RawQuery
	}
	// Fragment intentionally dropped.
	return canonical, nil
}

// canonicalAuthority returns lowercased host[:port] with default ports stripped.
// For http.Request verifiers, pass r.Host.
//
// IPv6 literal hosts are preserved in bracketed form (e.g. "[::1]:8443").
func canonicalAuthority(hostHeader, scheme string) string {
	if hostHeader == "" {
		return ""
	}
	s := strings.ToLower(scheme)

	// net.SplitHostPort handles IPv6 bracketing correctly.
	host, port, err := net.SplitHostPort(hostHeader)
	if err != nil {
		// No port, or malformed. Treat as bare host.
		return strings.ToLower(hostHeader)
	}
	host = strings.ToLower(host)
	if isDefaultPort(s, port) {
		return bracketIfIPv6(host)
	}
	return bracketIfIPv6(host) + ":" + port
}

// bracketIfIPv6 wraps host in brackets when it parses as an IPv6 address
// literal. net.SplitHostPort strips the brackets; canonical authority keeps
// them.
func bracketIfIPv6(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "https" && port == "443") || (scheme == "http" && port == "80")
}

// validateASCIIHost rejects host strings containing non-ASCII bytes. Non-ASCII
// U-labels (e.g., raw IDN) on the wire would produce canonical bases that don't
// match verifiers which convert to A-labels first. The AdCP profile requires
// A-label form; callers that work with internationalized names MUST apply
// `idna.Lookup.ToASCII` before signing or verifying.
func validateASCIIHost(host string) error {
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return fmt.Errorf("non-ASCII host %q: convert to Punycode A-label before signing", host)
		}
	}
	return nil
}

// removeDotSegments implements RFC 3986 §5.2.4.
func removeDotSegments(input string) string {
	var out strings.Builder
	rest := input
	for len(rest) > 0 {
		switch {
		case strings.HasPrefix(rest, "../"):
			rest = rest[3:]
		case strings.HasPrefix(rest, "./"):
			rest = rest[2:]
		case strings.HasPrefix(rest, "/./"):
			rest = "/" + rest[3:]
		case rest == "/.":
			rest = "/"
		case strings.HasPrefix(rest, "/../"):
			rest = "/" + rest[4:]
			truncateLastSegment(&out)
		case rest == "/..":
			rest = "/"
			truncateLastSegment(&out)
		case rest == "." || rest == "..":
			rest = ""
		default:
			// Move first segment (including leading /) from rest to out.
			// Find next "/" not at position 0.
			end := len(rest)
			if i := strings.IndexByte(rest[1:], '/'); i >= 0 {
				end = i + 1
			}
			out.WriteString(rest[:end])
			rest = rest[end:]
		}
	}
	return out.String()
}

func truncateLastSegment(b *strings.Builder) {
	s := b.String()
	i := strings.LastIndexByte(s, '/')
	b.Reset()
	if i >= 0 {
		b.WriteString(s[:i])
	}
}

// normalizePercentEncoding applies RFC 3986 §6.2.2.2: percent-encoded octets
// corresponding to unreserved characters (ALPHA / DIGIT / "-" / "." / "_" / "~")
// are decoded; all other percent-encoded octets have their hex digits
// uppercased.
//
// This normalization must run identically on signers and verifiers across all
// implementations or canonical bases drift. The AdCP profile defers to RFC
// 3986 here; conformance vector 008 exercises the reserved-byte case
// (%e2%98%83 → %E2%98%83) but not the unreserved case (%7E → ~).
func normalizePercentEncoding(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			octet := hexValue(s[i+1])<<4 | hexValue(s[i+2])
			if isUnreserved(octet) {
				b.WriteByte(octet)
			} else {
				b.WriteByte('%')
				b.WriteByte(upperHex(s[i+1]))
				b.WriteByte(upperHex(s[i+2]))
			}
			i += 2
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func upperHex(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 'A'
	}
	return c
}

// isUnreserved reports whether b is an RFC 3986 unreserved character.
func isUnreserved(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z':
		return true
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-' || b == '.' || b == '_' || b == '~':
		return true
	}
	return false
}

// buildSignatureBase assembles the RFC 9421 §2.5 signature base string.
//
// Lines are joined with a single \n (LF), and there is no trailing newline.
// Components appear in the order given by covered, followed by @signature-params
// as the last line.
//
// componentValues maps each covered component name to its canonicalized value.
// sigParamsValue is the literal `(...)` + `;param=...` string that appears on
// the @signature-params line AND as the right-hand side of the Signature-Input
// header value.
func buildSignatureBase(covered []string, componentValues map[string]string, sigParamsValue string) (string, error) {
	var b strings.Builder
	for _, name := range covered {
		v, ok := componentValues[name]
		if !ok {
			return "", fmt.Errorf("component %q missing value", name)
		}
		b.WriteString("\"")
		b.WriteString(name)
		b.WriteString("\": ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	b.WriteString("\"")
	b.WriteString(sigParamsComponent)
	b.WriteString("\": ")
	b.WriteString(sigParamsValue)
	return b.String(), nil
}
