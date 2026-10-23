package signing

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/safefetch"
)

// CanonicalOrigin returns the canonical origin of rawURL: its scheme and
// authority after AdCP URL canonicalization steps 1 to 4 (lowercase scheme
// and host, one trailing DNS root dot removed, userinfo stripped, default
// port stripped), written scheme://authority with no path, query, or
// fragment. Only absolute http and https URLs have an origin; a URL the
// canonicalization rejects has none.
//
// Under the AdCP 3.3 Web Bot Auth profile an agent's identity is its
// canonical origin (docs/reference/url-canonicalization, Canonical origin).
// Like the rest of this package, it requires A-label (ASCII) hosts.
func CanonicalOrigin(rawURL string) (string, error) {
	canonical, err := canonicalTargetURI(rawURL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(canonical)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("signing: an origin requires an absolute URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

// signatureAgentOrigin returns the origin a Signature-Agent member value
// names. The value must be an https origin with no path (WBA §5.2.1 and
// §5.5), spelled exactly as its canonical origin: a lowercase host, no
// default port, and no trailing slash, path, query, fragment, or userinfo.
// One identity therefore has one spelling on the wire.
func signatureAgentOrigin(value string) (string, error) {
	origin, err := webBotAuthOrigin(value)
	if err != nil {
		return "", err
	}
	if value != origin {
		return "", errors.New("signing: Signature-Agent is not a canonical https origin")
	}
	return origin, nil
}

// webBotAuthOrigin validates a URL naming a Web Bot Auth identity (a
// signer's AgentOrigin, say) and returns its canonical origin: HTTPS, no
// userinfo, no whitespace or control characters, and a host that passes the
// static SSRF rules. A path is permitted and discarded; the identity is the
// origin. A Signature-Agent value must be the origin itself
// (signatureAgentOrigin).
func webBotAuthOrigin(rawURL string) (string, error) {
	for i := 0; i < len(rawURL); i++ {
		if rawURL[i] < 0x21 || rawURL[i] == 0x7f {
			return "", errors.New("signing: origin URL contains whitespace or a control character")
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.User != nil {
		return "", errors.New("signing: Web Bot Auth origins must not carry userinfo")
	}
	origin, err := CanonicalOrigin(rawURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(origin, "https://") {
		return "", errors.New("signing: Web Bot Auth origins require HTTPS")
	}
	// CanonicalOrigin strips only the literal default port "443", so a port
	// written with a leading zero (":0443", ":08443") or out of range
	// (":99999") would survive as a second spelling of an identity, or as
	// one no fetch can reach. Require a port as the WHATWG URL parser
	// serializes it: decimal, no leading zero, 1 to 65535.
	if u, err := url.Parse(origin); err != nil || !canonicalPort(u.Port()) {
		return "", errors.New("signing: Web Bot Auth origin port is not a canonical port number")
	}
	if err := safefetch.ValidateTarget(origin + "/"); err != nil {
		return "", err
	}
	return origin, nil
}

// canonicalPort reports whether port is empty or a decimal port number
// from 1 to 65535 with no leading zero.
func canonicalPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}
