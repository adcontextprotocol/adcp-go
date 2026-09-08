// Package urlutil provides helpers for working with URLs and hostnames.
package urlutil

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// schemePrefix matches a leading URL scheme per RFC 3986 (ALPHA followed by
// ALPHA / DIGIT / "+" / "-" / "."), terminated by "://".
var schemePrefix = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+\-.]*://`)

var dottedWireDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

var developmentExactNames = map[string]struct{}{
	"example.com": {},
	"example.net": {},
	"example.org": {},
}

var developmentSuffixes = []string{"localhost", "test", "example", "invalid"}

var specialUseSuffixes = []string{
	"alt", "6tisch.arpa", "eap.arpa", "eap-noob.arpa", "home.arpa",
	"in-addr.arpa", "ip6.arpa", "ipv4only.arpa", "resolver.arpa", "service.arpa",
	"example", "example.com", "example.net", "example.org", "invalid", "local",
	"localhost", "onion", "test",
}

// ErrInvalid wraps every reason Registrable rejects an input — bad URL
// syntax, missing host, or a host the public suffix list can't reduce
// to a registrable domain (raw IPs, single-label hostnames like
// "localhost").
var ErrInvalid = errors.New("urlutil: invalid")

var (
	ErrBrandDomainSyntax      = fmt.Errorf("brand domain syntax: %w", ErrInvalid)
	ErrBrandDomainRegistrable = fmt.Errorf("brand domain is not registrable: %w", ErrInvalid)
	ErrBrandDomainSpecialUse  = fmt.Errorf("special-use brand domain is not allowed: %w", ErrInvalid)
)

// BrandDomainOptions controls BrandRef/BrandKey domain validation.
type BrandDomainOptions struct {
	// AllowDevelopmentDomains admits only subdomains of .localhost, .test,
	// .example, or .invalid, plus example.com/net/org. It is deliberately
	// explicit; callers must not infer it from an environment variable. Bare
	// localhost remains invalid and .local is never admitted.
	AllowDevelopmentDomains bool
}

// ValidateBrandDomain validates and canonicalizes a BrandRef/BrandKey domain.
//
// Production names must use portable dotted wire syntax, have a registrable
// domain in the pinned ICANN+PRIVATE Public Suffix List, and not be an IANA
// special-use name. It accepts a bare domain only, never a URL, port, or path.
func ValidateBrandDomain(domain string, opts BrandDomainOptions) (string, error) {
	if domain == "" || strings.ContainsAny(domain, " \t\r\n/:@?#") {
		return "", fmt.Errorf("%q: %w", domain, ErrBrandDomainSyntax)
	}
	canonical, err := idna.Lookup.ToASCII(strings.TrimSuffix(domain, "."))
	if err != nil {
		return "", fmt.Errorf("%q: %w", domain, errors.Join(err, ErrBrandDomainSyntax))
	}
	canonical = strings.ToLower(canonical)
	if !dottedWireDomain.MatchString(canonical) || net.ParseIP(canonical) != nil {
		return "", fmt.Errorf("%q: %w", domain, ErrBrandDomainSyntax)
	}

	development := IsDevelopmentBrandDomain(canonical)
	if development && opts.AllowDevelopmentDomains {
		return canonical, nil
	}
	if development || isSpecialUseDomain(canonical) {
		return "", fmt.Errorf("%q: %w", domain, ErrBrandDomainSpecialUse)
	}

	suffix, icann := publicsuffix.PublicSuffix(canonical)
	lastLabel := canonical[strings.LastIndexByte(canonical, '.')+1:]
	// x/net/publicsuffix reports icann=false for both PRIVATE rules and unknown
	// TLDs. A PRIVATE rule is still identifiable because its suffix contains a
	// registrable boundary (for example github.io), while an unknown TLD falls
	// back to the final label itself.
	if suffix == "" || (!icann && suffix == lastLabel) {
		return "", fmt.Errorf("%q: %w", domain, ErrBrandDomainRegistrable)
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(canonical); err != nil {
		return "", fmt.Errorf("%q: %w", domain, errors.Join(err, ErrBrandDomainRegistrable))
	}
	return canonical, nil
}

// IsDevelopmentBrandDomain reports whether domain is in the protocol's narrow
// reserved-name set for local development and deterministic fixtures.
func IsDevelopmentBrandDomain(domain string) bool {
	if _, ok := developmentExactNames[domain]; ok {
		return true
	}
	for _, suffix := range developmentSuffixes {
		if strings.HasSuffix(domain, "."+suffix) {
			return true
		}
	}
	return false
}

func isSpecialUseDomain(domain string) bool {
	for _, suffix := range specialUseSuffixes {
		if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
			return true
		}
	}
	return false
}

// Registrable reduces rawURL to its registrable domain (eTLD+1).
//
// Schemeless inputs such as "abc.google.com/test/v1" are accepted; an
// "https://" scheme is assumed when none is present.
func Registrable(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("empty url: %w", ErrInvalid)
	}
	if !schemePrefix.MatchString(rawURL) {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", rawURL, errors.Join(err, ErrInvalid))
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("no host in %q: %w", rawURL, ErrInvalid)
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", fmt.Errorf("registrable domain for %q: %w", host,
			errors.Join(err, ErrInvalid))
	}
	return domain, nil
}
