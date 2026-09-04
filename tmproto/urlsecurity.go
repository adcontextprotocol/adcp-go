package tmproto

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// MaxContentURLLength caps every URL field this file validates (Artifact.URL,
// ImageAsset.URL, VideoAsset.URL, VideoAsset.ThumbnailURL, AudioAsset.URL,
// ArtifactRef.Value when Type == url). The value is copied into structured
// logs and (for ArtifactRef.Value) into content-hash inputs; an unbounded
// string is a log-size and allocation DoS vector the same way
// MaxSellerAgentURLLength bounds seller_agent_url.
const MaxContentURLLength = 2048

// ErrUnsafeURL is wrapped by ValidateFetchableURL when raw parses as a
// well-formed URL but is refused on SSRF grounds — as opposed to a plain
// parse/format failure. Callers that want to distinguish "malformed" from
// "syntactically fine but points somewhere it must not" can match on this
// with errors.Is.
var ErrUnsafeURL = errors.New("tmproto: url is not safe to fetch")

// ValidateFetchableURL is the SDK's shared, SSRF-safe validator for
// publisher-supplied URLs that a buyer agent (or any embedding application)
// may dereference over HTTP(S): Artifact.URL, ImageAsset.URL, VideoAsset.URL,
// VideoAsset.ThumbnailURL, AudioAsset.URL, and ArtifactRef.Value when
// Type == ArtifactRefTypeURL. See the doc comment on Artifact for the full
// MUST-validate-before-fetch contract these fields carry.
//
// Checks performed (all synchronous, no DNS lookups or network I/O):
//   - raw parses as an absolute URL and is within MaxContentURLLength.
//   - Scheme is http or https (rejects file://, javascript://, data:, etc).
//   - No userinfo / embedded credentials (rejects "https://user:pass@host/...").
//   - Host is present and is not a known-internal hostname pattern
//     (localhost, *.localhost, *.local, *.internal, cloud metadata hostnames).
//   - When the host is an IP literal, the address is not in a private,
//     loopback, link-local, CGNAT, benchmark, unique-local, or otherwise
//     reserved range — see isDisallowedIP.
//
// Rule source: the AdCP webhook SSRF validation rules
// (https://adcontextprotocol.org/docs/building/implementation/security#webhook-url-validation-ssrf),
// cross-checked against the canonical implementation in the main AdCP
// repository, adcontextprotocol/adcp server/src/utils/url-security.ts
// (isPrivateHostname / normalizeExternalHostname / validateExternalUrl as of
// adcontextprotocol/adcp#7091, which closed a CGNAT 100.64.0.0/10 gap in that
// same validator — the range list here was built to include it from the
// start, along with the IPv6 6to4/NAT64-tunneled-address checks the TS
// implementation carries that this SDK's other SSRF guard,
// adcp/signing.NewSafeHTTPClient's isDisallowedIP, does not yet have).
//
// What this function deliberately does NOT do: resolve DNS. A hostname that
// is not an IP literal and does not match a known-internal suffix passes
// here even if it will later resolve to a private address (or is rebound to
// one between validation and fetch). tmproto has no HTTP client of its own —
// any embedding application that actually performs the fetch MUST layer a
// dial-time guard that re-checks the resolved address at TCP-connect time
// (as adcp/signing.NewSafeHTTPClient does), not rely on this pre-flight
// check alone. Treat ValidateFetchableURL as the fast, always-applicable
// first gate; the dial-time guard closes the DNS-rebind TOCTOU window this
// function cannot.
func ValidateFetchableURL(raw string) error {
	if raw == "" {
		return errors.New("url: empty")
	}
	if len(raw) > MaxContentURLLength {
		return fmt.Errorf("url: exceeds maximum length of %d", MaxContentURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url: does not parse: %w", err)
	}
	if !u.IsAbs() {
		return errors.New("url: must be absolute")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: url: scheme must be http or https, got %q", ErrUnsafeURL, scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: url: must not contain userinfo (credentials)", ErrUnsafeURL)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: url: missing host", ErrUnsafeURL)
	}
	if err := checkHostnameNotInternal(host); err != nil {
		return fmt.Errorf("%w: url: %s", ErrUnsafeURL, err.Error())
	}
	return nil
}

// checkHostnameNotInternal rejects host strings that identify a
// known-internal target without needing DNS resolution: literal loopback /
// private / reserved IP addresses, "localhost" and its subdomains, and the
// .local / .internal reserved TLDs (mDNS and RFC 6762 §2 / cloud-provider
// internal-DNS conventions respectively — e.g. AWS/GCP internal service
// discovery and metadata.google.internal both live under .internal or a
// link-local IP literal, both covered here).
func checkHostnameNotInternal(host string) error {
	lower := strings.ToLower(host)
	// A single trailing dot is the canonical DNS root label; strip it before
	// classifying so "localhost." isn't treated as some other TLD.
	lower = strings.TrimSuffix(lower, ".")
	if lower == "" {
		return errors.New("empty host")
	}
	for _, label := range strings.Split(lower, ".") {
		if label == "" {
			// Empty label anywhere else (e.g. "example..com") is malformed;
			// fail closed rather than let a resolver or proxy interpret it
			// differently than this check did.
			return errors.New("host contains an empty label")
		}
	}
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return errors.New("host is localhost")
	}
	if strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return errors.New("host uses a reserved internal-only TLD (.local / .internal)")
	}

	// IP literal (v4, v6, or v6 with brackets already stripped by
	// url.URL.Hostname): classify by range.
	if ip := net.ParseIP(lower); ip != nil {
		if isDisallowedIP(ip) {
			return errors.New("host resolves to a disallowed private/reserved address")
		}
		return nil
	}

	// Not an IP literal and not a known-internal suffix: this is where
	// syntactic validation stops. See the "deliberately does NOT" paragraph
	// on ValidateFetchableURL — a dial-time guard must re-check the
	// resolved address before connecting.
	return nil
}

// isDisallowedIP reports whether ip must not be dialed per the AdCP webhook
// SSRF rules. Mirrors adcp/signing.isDisallowedIP (a separate Go module, so
// duplicated rather than imported — tmproto has zero external deps by
// design) and additionally covers the IPv6 tunneling forms
// (6to4 2002::/16, NAT64 64:ff9b::/96) that the canonical TypeScript
// validator (adcontextprotocol/adcp server/src/utils/url-security.ts,
// isPrivateHostname) checks and adcp/signing's port does not yet have.
func isDisallowedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0: // 0.0.0.0/8 — "this network"; routes to loopback on Linux.
			return true
		case ip4[0] == 10: // 10.0.0.0/8 RFC 1918 private.
			return true
		case ip4[0] == 172 && ip4[1]&0xf0 == 16: // 172.16.0.0/12 RFC 1918 private.
			return true
		case ip4[0] == 192 && ip4[1] == 168: // 192.168.0.0/16 RFC 1918 private.
			return true
		case ip4[0] == 169 && ip4[1] == 254: // 169.254.0.0/16 link-local — covers the
			// 169.254.169.254 cloud-metadata endpoint (AWS/GCP/Azure IMDS).
			return true
		case ip4[0] == 100 && ip4[1]&0xc0 == 64: // 100.64.0.0/10 CGNAT (RFC 6598).
			return true
		case ip4[0] == 198 && ip4[1]&0xfe == 18: // 198.18.0.0/15 benchmarking (RFC 2544).
			return true
		}
		return false
	}
	if len(ip) != net.IPv6len {
		return false
	}
	if ip[0]&0xfe == 0xfc { // fc00::/7 unique local address (RFC 4193).
		return true
	}
	if ip[0] == 0xfe && ip[1]&0xc0 == 0xc0 { // fec0::/10 deprecated site-local (RFC 3879).
		return true
	}
	// 6to4 2002::/16 (RFC 3056): bytes[2:6] carry the embedded IPv4 address.
	if ip[0] == 0x20 && ip[1] == 0x02 {
		if isDisallowedIP(net.IPv4(ip[2], ip[3], ip[4], ip[5])) {
			return true
		}
	}
	// NAT64 well-known prefix 64:ff9b::/96 (RFC 6052): bytes[12:16] carry the
	// embedded IPv4 address; bytes[4:12] must be zero for the /96 to match.
	if ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b {
		allZero := true
		for _, b := range ip[4:12] {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero && isDisallowedIP(net.IPv4(ip[12], ip[13], ip[14], ip[15])) {
			return true
		}
	}
	return false
}

// validateArtifactRefURL enforces the ArtifactRefTypeURL contract from the
// TMP spec's ArtifactRef.value docstring for type=url: the value MUST be the
// bare canonical content URL, with no user-specific path segments, query
// parameters, or fragments — carrying any of those turns a shareable content
// identifier into an identity-leak vector (e.g. a per-user tracking query
// param would let every buyer who resolves the ref correlate the same user
// across requests).
//
// This enforces the two components that are structurally identifiable as
// user/session-specific carriers — query string and fragment — plus the
// userinfo and SSRF checks from ValidateFetchableURL, since a type=url
// ArtifactRef is by definition "a public handle the buyer can resolve
// independently" (see the ArtifactRefType doc comment) and so is fetched the
// same way Artifact/asset URLs are. "No user-specific path segments" is a
// content judgment this function cannot make structurally — a path can't be
// told apart from a legitimate article slug by shape alone — so that half of
// the spec rule is a publisher obligation this SDK cannot enforce mechanically.
func validateArtifactRefURL(raw string) error {
	if len(raw) > MaxContentURLLength {
		return fmt.Errorf("exceeds maximum length of %d", MaxContentURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("does not parse: %w", err)
	}
	if u.RawQuery != "" {
		return errors.New("must not contain a query string (type=url forbids query parameters)")
	}
	if u.Fragment != "" || u.EscapedFragment() != "" {
		return errors.New("must not contain a fragment (type=url forbids fragments)")
	}
	if err := ValidateFetchableURL(raw); err != nil {
		return err
	}
	return nil
}
