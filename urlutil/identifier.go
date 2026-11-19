package urlutil

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/idna"
)

// IdentifierType is a property identifier type from
// enums/identifier-types.json in the AdCP schema bundle.
type IdentifierType string

const (
	IdentifierDomain            IdentifierType = "domain"
	IdentifierSubdomain         IdentifierType = "subdomain"
	IdentifierNetworkID         IdentifierType = "network_id"
	IdentifierIOSBundle         IdentifierType = "ios_bundle"
	IdentifierAndroidPackage    IdentifierType = "android_package"
	IdentifierAppleAppStoreID   IdentifierType = "apple_app_store_id"
	IdentifierGooglePlayID      IdentifierType = "google_play_id"
	IdentifierRokuStoreID       IdentifierType = "roku_store_id"
	IdentifierFireTVASIN        IdentifierType = "fire_tv_asin"
	IdentifierSamsungAppID      IdentifierType = "samsung_app_id"
	IdentifierAppleTVBundle     IdentifierType = "apple_tv_bundle"
	IdentifierBundleID          IdentifierType = "bundle_id"
	IdentifierVenueID           IdentifierType = "venue_id"
	IdentifierScreenID          IdentifierType = "screen_id"
	IdentifierOpenOOHVenueType  IdentifierType = "openooh_venue_type"
	IdentifierRSSURL            IdentifierType = "rss_url"
	IdentifierApplePodcastID    IdentifierType = "apple_podcast_id"
	IdentifierSpotifyCollection IdentifierType = "spotify_collection_id"
	IdentifierPodcastGUID       IdentifierType = "podcast_guid"
	IdentifierStationID         IdentifierType = "station_id"
	IdentifierFacilityID        IdentifierType = "facility_id"
)

// The hostname patterns mirror core/identifier.json exactly: lowercase
// ASCII labels (IDNs in punycode A-label form), at least two labels, no
// scheme, path, port, whitespace, or trailing dot. Domain values may
// carry one leading "*." wildcard; subdomain values may not.
var (
	domainIdentifierPattern    = regexp.MustCompile(`^(?:\*\.)?[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	subdomainIdentifierPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

	// publisherDomainPattern mirrors the publisher_domain pattern in
	// property/base-property-source.json: a bare lowercase hostname,
	// single-label values included, no wildcard and no URL syntax.
	publisherDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

	numericIdentifierPattern = regexp.MustCompile(`^[0-9]+$`)

	// Bundle-style identifiers are reverse-DNS names. Apple bundle IDs
	// allow letters, digits, and hyphens per label; Android package
	// names additionally allow underscores but every label must start
	// with a letter.
	appleBundleLabel     = `[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?`
	androidBundleLabel   = `[A-Za-z][A-Za-z0-9_]*`
	appleBundlePattern   = regexp.MustCompile(`^` + appleBundleLabel + `(\.` + appleBundleLabel + `)+$`)
	androidBundlePattern = regexp.MustCompile(`^` + androidBundleLabel + `(\.` + androidBundleLabel + `)+$`)
	genericBundlePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9_])?)+$`)
)

// IdentifierError reports an identifier value that fails the format rule
// for its type. It wraps ErrInvalid.
type IdentifierError struct {
	Type   IdentifierType
	Value  string
	Reason string
}

func (e *IdentifierError) Error() string {
	return fmt.Sprintf("urlutil: invalid %s identifier %q: %s", e.Type, e.Value, e.Reason)
}

func (e *IdentifierError) Unwrap() error { return ErrInvalid }

func identifierErr(t IdentifierType, value, reason string) error {
	return &IdentifierError{Type: t, Value: value, Reason: reason}
}

// ValidateIdentifier checks value against the format rule the AdCP spec
// defines for its type, without rewriting it: the value must already be
// in canonical form. Types with no defined format pass through as long
// as they are non-empty. An unknown type is an error — the identifier
// type enum is closed.
func ValidateIdentifier(idType IdentifierType, value string) error {
	switch idType {
	case IdentifierDomain:
		if len(value) > 255 {
			return identifierErr(idType, value, "exceeds 255 characters")
		}
		if !strings.HasPrefix(value, "*.") && len(value) > 253 {
			return identifierErr(idType, value, "exceeds 253 characters")
		}
		if !domainIdentifierPattern.MatchString(value) {
			return identifierErr(idType, value, "not a lowercase hostname with optional leading '*.'")
		}
		return nil
	case IdentifierSubdomain:
		if len(value) > 253 {
			return identifierErr(idType, value, "exceeds 253 characters")
		}
		if !subdomainIdentifierPattern.MatchString(value) {
			return identifierErr(idType, value, "not a lowercase hostname")
		}
		return nil
	case IdentifierIOSBundle, IdentifierAppleTVBundle:
		if !appleBundlePattern.MatchString(value) {
			return identifierErr(idType, value, "not a reverse-DNS bundle identifier")
		}
		return nil
	case IdentifierAndroidPackage, IdentifierGooglePlayID:
		if !androidBundlePattern.MatchString(value) {
			return identifierErr(idType, value, "not a reverse-DNS package name")
		}
		return nil
	case IdentifierBundleID:
		if !genericBundlePattern.MatchString(value) {
			return identifierErr(idType, value, "not a reverse-DNS bundle identifier")
		}
		return nil
	case IdentifierAppleAppStoreID, IdentifierApplePodcastID:
		if !numericIdentifierPattern.MatchString(value) {
			return identifierErr(idType, value, "not a numeric store identifier")
		}
		return nil
	case IdentifierRSSURL:
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return identifierErr(idType, value, "not an absolute http(s) URL")
		}
		return nil
	case IdentifierNetworkID, IdentifierRokuStoreID, IdentifierFireTVASIN,
		IdentifierSamsungAppID, IdentifierVenueID, IdentifierScreenID,
		IdentifierOpenOOHVenueType, IdentifierSpotifyCollection,
		IdentifierPodcastGUID, IdentifierStationID, IdentifierFacilityID:
		// No format is defined for these types; pass through.
		if value == "" {
			return identifierErr(idType, value, "empty")
		}
		return nil
	default:
		return identifierErr(idType, value, "unknown identifier type")
	}
}

// NormalizeIdentifier returns the canonical form of value for its type
// and whether normalization changed the input. For domain and subdomain
// values — the ones buyers paste as full URLs into exclusion lists — it
// reduces a URL to its host, strips any port, path, and trailing dot,
// lowercases, and converts IDNs to punycode A-labels, then applies the
// same strict validation as ValidateIdentifier. Types whose values are
// case-sensitive identities (bundle and package names, store IDs) are
// only trimmed of surrounding whitespace, never case-folded. Types with
// no defined format pass through unchanged.
func NormalizeIdentifier(idType IdentifierType, value string) (canonical string, rewritten bool, err error) {
	switch idType {
	case IdentifierDomain, IdentifierSubdomain:
		canonical, err = normalizeHostnameIdentifier(idType, value)
		if err != nil {
			return "", false, err
		}
		return canonical, canonical != value, nil
	case IdentifierIOSBundle, IdentifierAppleTVBundle, IdentifierAndroidPackage,
		IdentifierGooglePlayID, IdentifierBundleID, IdentifierAppleAppStoreID,
		IdentifierApplePodcastID, IdentifierRSSURL:
		canonical = strings.TrimSpace(value)
		if err := ValidateIdentifier(idType, canonical); err != nil {
			return "", false, err
		}
		return canonical, canonical != value, nil
	default:
		if err := ValidateIdentifier(idType, value); err != nil {
			return "", false, err
		}
		return value, false, nil
	}
}

func normalizeHostnameIdentifier(idType IdentifierType, value string) (string, error) {
	v := strings.TrimSpace(value)
	wildcard := false
	if idType == IdentifierDomain && strings.HasPrefix(v, "*.") {
		wildcard = true
		v = strings.TrimPrefix(v, "*.")
	}
	// Reduce a pasted URL (or schemeless host/path) to its host.
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return "", identifierErr(idType, value, "cannot extract a host from the URL")
		}
		v = u.Hostname()
	} else {
		if i := strings.IndexByte(v, '/'); i >= 0 {
			v = v[:i]
		}
		if i := strings.LastIndexByte(v, ':'); i >= 0 {
			v = v[:i] // strip port
		}
	}
	v = strings.TrimSuffix(strings.ToLower(v), ".")
	if v == "" {
		return "", identifierErr(idType, value, "empty host")
	}
	ascii, err := idna.Lookup.ToASCII(v)
	if err != nil {
		return "", identifierErr(idType, value, "not a valid hostname")
	}
	if wildcard {
		ascii = "*." + ascii
	}
	if err := ValidateIdentifier(idType, ascii); err != nil {
		return "", err
	}
	return ascii, nil
}

// ValidatePublisherDomain is the strict publisher_domain check to run
// before a registry write: the value must be a bare lowercase ASCII
// hostname matching the publisher_domain pattern in
// property/base-property-source.json — no wildcard, no scheme, path,
// port, userinfo, or trailing dot. It returns the value unchanged when
// valid; there is no rewriting, so a pasted URL is rejected rather than
// silently stored in a form no seller can match. Use NormalizeIdentifier
// first if user input should be repaired instead of rejected.
//
// This is deliberately narrower than ValidateBrandDomain: it applies
// the schema pattern only, with no public-suffix or special-use policy,
// because publisher_domain keys registry records for names (such as
// development and fixture domains) that brand policy would refuse.
func ValidatePublisherDomain(domain string) (string, error) {
	if domain == "" {
		return "", identifierErr("publisher_domain", domain, "empty")
	}
	if len(domain) > 253 {
		return "", identifierErr("publisher_domain", domain, "exceeds 253 characters")
	}
	if !publisherDomainPattern.MatchString(domain) {
		return "", identifierErr("publisher_domain", domain, "not a bare lowercase hostname")
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) > 63 {
			return "", identifierErr("publisher_domain", domain, "label exceeds 63 characters")
		}
	}
	return domain, nil
}
