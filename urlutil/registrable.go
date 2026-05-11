// Package urlutil provides helpers for working with URLs and hostnames.
package urlutil

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// schemePrefix matches a leading URL scheme per RFC 3986 (ALPHA followed by
// ALPHA / DIGIT / "+" / "-" / "."), terminated by "://".
var schemePrefix = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+\-.]*://`)

// ErrInvalid wraps every reason Registrable rejects an input — bad URL
// syntax, missing host, or a host the public suffix list can't reduce
// to a registrable domain (raw IPs, single-label hostnames like
// "localhost").
var ErrInvalid = errors.New("urlutil: invalid")

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
