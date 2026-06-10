package signing

import (
	"fmt"
	"net"
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
