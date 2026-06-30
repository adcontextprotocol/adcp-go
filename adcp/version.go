package adcp

import (
	"strconv"
	"strings"
)

const (
	// ADCPProtocolVersion30 is the 3.0 release-precision wire version.
	ADCPProtocolVersion30 = "3.0"
	// ADCPProtocolVersion31 is the 3.1 release-precision wire version.
	ADCPProtocolVersion31 = "3.1"
	// ADCPProtocolVersion32 is the 3.2 release-precision wire version.
	ADCPProtocolVersion32 = "3.2"
	// ADCPMajorVersion3 is the legacy major-version value for all AdCP 3.x releases.
	ADCPMajorVersion3 = 3
)

// SupportedADCPVersions returns the 3.x release-precision versions this SDK
// supports on the wire. Callers receive a fresh slice.
func SupportedADCPVersions() []string {
	return []string{ADCPProtocolVersion30, ADCPProtocolVersion31, ADCPProtocolVersion32}
}

// DefaultADCPVersion returns the highest 3.x release-precision version this
// SDK emits when a request does not pin adcp_version.
func DefaultADCPVersion() string {
	return ADCPProtocolVersion32
}

// VersionEnvelopeFor returns a request/response version envelope for a
// release-precision or full-semver AdCP version.
func VersionEnvelopeFor(version string) (VersionEnvelope, bool) {
	normalized, ok := NormalizeADCPVersion(version)
	if !ok {
		return VersionEnvelope{}, false
	}
	major, ok := MajorFromADCPVersion(normalized)
	if !ok {
		return VersionEnvelope{}, false
	}
	return VersionEnvelope{AdcpVersion: normalized, AdcpMajorVersion: major}, true
}

// NormalizeADCPVersion converts full semver bundle/build versions such as
// "3.1.0-rc.3" into release-precision wire versions such as "3.1-rc.3".
func NormalizeADCPVersion(version string) (string, bool) {
	release, ok := parseADCPRelease(version)
	if !ok {
		return "", false
	}
	return release.version, true
}

// MajorFromADCPVersion extracts the major component from an AdCP wire version.
func MajorFromADCPVersion(version string) (int, bool) {
	release, ok := parseADCPRelease(version)
	if !ok {
		return 0, false
	}
	return release.major, true
}

// NegotiateADCPVersion selects the version a server should serve for a request.
//
// If requestedVersion is present, it must be the buyer's release pin. If only
// requestedMajor is present, the highest supported release in that major is
// selected for 3.x backward compatibility. If neither is present, the highest
// supported release is selected.
func NegotiateADCPVersion(requestedVersion string, requestedMajor int, supported []string) (string, bool) {
	return negotiateADCPVersion(adcpVersionRequest{
		version:       requestedVersion,
		major:         requestedMajor,
		majorProvided: requestedMajor != 0,
	}, supported)
}

type adcpVersionRequest struct {
	version       string
	major         int
	majorProvided bool
}

func negotiateADCPVersion(request adcpVersionRequest, supported []string) (string, bool) {
	supportedReleases := parseSupportedADCPReleases(supported)
	if len(supportedReleases) == 0 {
		supportedReleases = parseSupportedADCPReleases(SupportedADCPVersions())
	}

	if strings.TrimSpace(request.version) != "" {
		requested, ok := parseADCPRelease(request.version)
		if !ok {
			return "", false
		}
		if requested.prerelease != "" {
			if stable, ok := findSupportedADCPRelease(supportedReleases, requested.major, requested.minor, ""); ok {
				return stable.version, true
			}
		}
		return highestSupportedADCPRelease(supportedReleases, requested.major, &requested)
	}

	if request.majorProvided {
		if request.major < 1 {
			return "", false
		}
		return highestSupportedADCPRelease(supportedReleases, request.major, nil)
	}

	return highestSupportedADCPRelease(supportedReleases, 0, nil)
}

type adcpRelease struct {
	major      int
	minor      int
	prerelease string
	version    string
}

func parseSupportedADCPReleases(versions []string) []adcpRelease {
	releases := make([]adcpRelease, 0, len(versions))
	seen := map[string]bool{}
	for _, version := range versions {
		release, ok := parseADCPRelease(version)
		if !ok || seen[release.version] {
			continue
		}
		seen[release.version] = true
		releases = append(releases, release)
	}
	return releases
}

func highestSupportedADCPRelease(supported []adcpRelease, major int, max *adcpRelease) (string, bool) {
	var best adcpRelease
	found := false
	for _, release := range supported {
		if major != 0 && release.major != major {
			continue
		}
		if max != nil && compareADCPRelease(release, *max) > 0 {
			continue
		}
		if !found || compareADCPRelease(release, best) > 0 {
			best = release
			found = true
		}
	}
	if !found {
		return "", false
	}
	return best.version, true
}

func findSupportedADCPRelease(supported []adcpRelease, major, minor int, prerelease string) (adcpRelease, bool) {
	for _, release := range supported {
		if release.major == major && release.minor == minor && release.prerelease == prerelease {
			return release, true
		}
	}
	return adcpRelease{}, false
}

func parseADCPRelease(version string) (adcpRelease, bool) {
	version = strings.TrimSpace(version)
	if version == "" {
		return adcpRelease{}, false
	}
	if i := strings.IndexByte(version, '+'); i >= 0 {
		version = version[:i]
	}

	core, prerelease, _ := strings.Cut(version, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return adcpRelease{}, false
	}
	major, ok := parseNonNegativeInt(parts[0])
	if !ok {
		return adcpRelease{}, false
	}
	minor, ok := parseNonNegativeInt(parts[1])
	if !ok {
		return adcpRelease{}, false
	}
	if len(parts) == 3 {
		if _, ok := parseNonNegativeInt(parts[2]); !ok {
			return adcpRelease{}, false
		}
	}
	if prerelease != "" && !validPrerelease(prerelease) {
		return adcpRelease{}, false
	}

	normalized := strconv.Itoa(major) + "." + strconv.Itoa(minor)
	if prerelease != "" {
		normalized += "-" + prerelease
	}
	return adcpRelease{major: major, minor: minor, prerelease: prerelease, version: normalized}, true
}

func parseNonNegativeInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func validPrerelease(s string) bool {
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func compareADCPRelease(a, b adcpRelease) int {
	switch {
	case a.major != b.major:
		return compareInt(a.major, b.major)
	case a.minor != b.minor:
		return compareInt(a.minor, b.minor)
	case a.prerelease == "" && b.prerelease != "":
		return 1
	case a.prerelease != "" && b.prerelease == "":
		return -1
	case a.prerelease == "" && b.prerelease == "":
		return 0
	default:
		return comparePrerelease(a.prerelease, b.prerelease)
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func comparePrerelease(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] == bParts[i] {
			continue
		}
		aNum, aIsNum := parseNonNegativeInt(aParts[i])
		bNum, bIsNum := parseNonNegativeInt(bParts[i])
		switch {
		case aIsNum && bIsNum:
			if c := compareInt(aNum, bNum); c != 0 {
				return c
			}
		case aIsNum:
			return -1
		case bIsNum:
			return 1
		default:
			if aParts[i] < bParts[i] {
				return -1
			}
			return 1
		}
	}
	return compareInt(len(aParts), len(bParts))
}
