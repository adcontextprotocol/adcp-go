// Package urlcanon implements AdCP URL-identifier canonicalization, the
// single normalization an AdCP implementation applies before comparing two
// URLs for identity (e.g. matching a seller_agent_url to its registered
// active package set, or computing the RFC 9421 @target-uri signature
// component).
//
// The algorithm is defined in the AdCP reference documentation at
// docs/reference/url-canonicalization. It is intentionally distinct from
// tmproto.CanonicalizeURL, which produces the url_hash content-dedup form
// (lowercased, query-sorted, fragment-stripped) used for artifact hashing —
// do not substitute one for the other.
package urlcanon

import (
	"fmt"
	"net/url"
	"strings"
)

// Canonicalize applies the AdCP URL-identifier canonicalization
// (docs/reference/url-canonicalization):
//
//  1. Lowercase scheme.
//  2. Lowercase host; IDN → Punycode (url.Parse handles Punycode already).
//  3. Strip userinfo.
//  4. Strip default ports (:443 for https, :80 for http).
//  5. remove_dot_segments on path; empty path with authority becomes "/".
//  6. Normalize percent-encoding: decode unreserved octets, uppercase the
//     hex of reserved ones.
//  7. Preserve query byte-for-byte.
//  8. Strip fragment.
//
// rawURL is the URL as received on the wire. For outgoing signed requests the
// signer supplies the URL string it will send; for incoming, the verifier
// reconstructs it from scheme + Host + RequestURI.
func Canonicalize(rawURL string) (string, error) {
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
	authority := BracketIfIPv6(host)
	if port != "" && !IsDefaultPort(scheme, port) {
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

// BracketIfIPv6 wraps host in brackets when it parses as an IPv6 address
// literal. net.SplitHostPort strips the brackets; canonical authority keeps
// them.
func BracketIfIPv6(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// IsDefaultPort reports whether port is the default for scheme (443 for https,
// 80 for http) and may therefore be stripped from a canonical authority.
func IsDefaultPort(scheme, port string) bool {
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
