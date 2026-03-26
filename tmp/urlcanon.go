package tmp

import (
	"hash/fnv"
	"strings"
)

// CanonicalizeURL normalizes a URL for consistent hashing:
//   - Strip scheme (http://, https://)
//   - Strip www., m., amp. prefixes from hostname
//   - Lowercase everything
//   - Strip trailing slash
//   - Strip query params and fragments
func CanonicalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// Strip scheme
	if idx := strings.Index(s, "://"); idx != -1 {
		s = s[idx+3:]
	}

	// Strip fragment
	if idx := strings.IndexByte(s, '#'); idx != -1 {
		s = s[:idx]
	}

	// Strip query
	if idx := strings.IndexByte(s, '?'); idx != -1 {
		s = s[:idx]
	}

	// Lowercase
	s = strings.ToLower(s)

	// Split host and path
	host := s
	path := ""
	if idx := strings.IndexByte(s, '/'); idx != -1 {
		host = s[:idx]
		path = s[idx:]
	}

	// Strip www., m., amp. prefixes from host
	for _, prefix := range []string{"www.", "m.", "amp."} {
		if strings.HasPrefix(host, prefix) {
			host = host[len(prefix):]
			break
		}
	}

	// Strip trailing slash from path
	path = strings.TrimRight(path, "/")

	return host + path
}

// HashURL returns a uint64 FNV-1a hash of the canonicalized URL.
func HashURL(raw string) uint64 {
	return HashCanonical(CanonicalizeURL(raw))
}

// HashCanonical returns a uint64 FNV-1a hash of an already-canonicalized string.
func HashCanonical(canonical string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(canonical))
	return h.Sum64()
}
