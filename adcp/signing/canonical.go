package signing

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/adcontextprotocol/adcp-go/urlcanon"
)

// canonicalTargetURI applies the AdCP URL-identifier canonicalization to
// produce the RFC 9421 @target-uri component. The algorithm lives in
// urlcanon.Canonicalize, shared with every other AdCP URL-identity comparison.
//
// rawURL is the URL as received on the wire. For outgoing requests the signer
// supplies the URL string it will send; for incoming, the verifier reconstructs
// it from scheme + Host + RequestURI.
func canonicalTargetURI(rawURL string) (string, error) {
	return urlcanon.Canonicalize(rawURL)
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
	if urlcanon.IsDefaultPort(s, port) {
		return urlcanon.BracketIfIPv6(host)
	}
	return urlcanon.BracketIfIPv6(host) + ":" + port
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

// profileComponents are the covered components whose values the verifier
// derives itself (the derived components) or reads under profile-specific
// rules (content-type, content-digest).
var profileComponents = map[string]struct{}{
	componentMethod:       {},
	componentTargetURI:    {},
	componentAuthority:    {},
	componentContentType:  {},
	componentContentDigst: {},
}

// rejectDuplicateComponents enforces RFC 9421 §2.5: a component identifier
// appears at most once in the covered list. The request profile reports
// request_signature_components_unexpected, as the Python SDK does. The
// webhook profile's error table has no components_unexpected code for this
// case, so it reports webhook_signature_header_malformed, also matching the
// Python SDK.
func rejectDuplicateComponents(components []string, profile Profile) error {
	seen := make(map[string]struct{}, len(components))
	for _, name := range components {
		if _, dup := seen[name]; dup {
			code := CodeComponentsUnexpected
			if profile.Tag == ProfileWebhookSigning.Tag {
				code = CodeHeaderMalformed
			}
			return newError(code, "covered component listed more than once")
		}
		seen[name] = struct{}{}
	}
	return nil
}

// additionalComponentValues returns the signature-base values for every
// covered component outside profileComponents.
//
// An additional component must be an HTTP field: a lowercase RFC 9110 token
// with no component parameters (the Signature-Input parser already rejects
// parameters). These all reject with request_signature_header_malformed,
// the code the verifier used before for any unrecognized component, and the
// code the Python SDK uses:
//   - a derived component other than the profile's (@path, @query,
//     @request-target, ...), which AdCP canonicalization does not define;
//   - a name that is not a lowercase field-name token;
//   - a covered field absent from the request (RFC 9421 §2.5: the signature
//     base cannot be built);
//   - a field value carrying CR, LF, or NUL.
func additionalComponentValues(r *http.Request, components []string) (map[string]string, error) {
	var out map[string]string
	for _, name := range components {
		if _, ok := profileComponents[name]; ok {
			continue
		}
		if strings.HasPrefix(name, "@") {
			return nil, newError(CodeHeaderMalformed, "unsupported derived component")
		}
		if !isLowercaseFieldName(name) {
			return nil, newError(CodeHeaderMalformed, "covered component is not a lowercase HTTP field name")
		}
		v, err := coveredFieldValue(r.Header, name)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[name] = v
	}
	return out, nil
}

// coveredFieldValue returns the RFC 9421 §2.1 value of the HTTP field name:
// every field line in order, leading and trailing SP/HTAB removed, joined
// with ", ". Splitting one field line into several, or merging several into
// one, therefore yields the same value, as RFC 9421 intends; a consumer that
// relies on a covered field must read every line (http.Header.Values), not
// just the first.
//
// It rejects an absent field, and a value carrying CR, LF, or NUL, which
// could forge a line in the signature base. net/http's server rejects such
// values, but a hand-built *http.Request may not.
func coveredFieldValue(h http.Header, name string) (string, error) {
	lines := h.Values(name)
	if len(lines) == 0 {
		return "", newError(CodeHeaderMalformed, "covered header field absent from request")
	}
	trimmed := make([]string, len(lines))
	for i, line := range lines {
		if strings.ContainsAny(line, "\r\n\x00") {
			return "", newError(CodeHeaderMalformed, "covered header field value contains a control character")
		}
		trimmed[i] = strings.Trim(line, " \t")
	}
	return strings.Join(trimmed, ", "), nil
}

// isLowercaseFieldName reports whether s is an RFC 9110 §5.1 field-name
// (a token) with no uppercase letters, as RFC 9421 §2.1 requires of an HTTP
// field component name.
func isLowercaseFieldName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
