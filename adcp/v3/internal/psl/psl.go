// Package psl derives registrable domains (eTLD+1) from the Public Suffix
// List snapshot pinned by this module's golang.org/x/net version. The
// snapshot includes both the ICANN and PRIVATE sections, so platform
// suffixes such as github.io or vercel.app are suffixes: two tenants on one
// platform never share a registrable domain. Nothing is fetched at run time;
// bumping golang.org/x/net in go.mod is how the snapshot is refreshed.
package psl

import (
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// RegistrableDomain returns the eTLD+1 of a URL or a bare host, lowercased,
// with one trailing root dot removed. It reports false, failing closed,
// when there is none:
//   - an IP literal, or a single-label host;
//   - a host that is itself a public suffix;
//   - a host under a TLD the snapshot does not list;
//   - a host that is not a valid A-label DNS name (empty labels, labels
//     over 63 bytes, leading or trailing hyphens, underscores, non-ASCII).
func RegistrableDomain(hostOrURL string) (string, bool) {
	host := hostOrURL
	if strings.Contains(hostOrURL, "://") {
		u, err := url.Parse(hostOrURL)
		if err != nil {
			return "", false
		}
		host = u.Hostname()
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || net.ParseIP(host) != nil || !validHost(host) {
		return "", false
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	if !icann && !strings.Contains(suffix, ".") {
		// The default "*" rule matched: the TLD is not in the snapshot.
		// PRIVATE-section suffixes always have at least two labels.
		return "", false
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", false
	}
	return domain, true
}

func validHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// QuotaKey returns the key under which per-operator quotas count a host:
// its registrable domain, except that every tenant of a PRIVATE-section
// suffix (a hosting platform such as github.io or pages.dev, where anyone
// can get a free registrable domain) counts under the platform suffix.
// One party with many free subdomains therefore shares one quota. It
// reports false when the host has no registrable domain.
func QuotaKey(hostOrURL string) (string, bool) {
	domain, ok := RegistrableDomain(hostOrURL)
	if !ok {
		return "", false
	}
	if suffix, icann := publicsuffix.PublicSuffix(domain); !icann && strings.Contains(suffix, ".") {
		return suffix, true
	}
	return domain, true
}
