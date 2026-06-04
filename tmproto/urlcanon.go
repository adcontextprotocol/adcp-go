package tmproto

import (
	"encoding/base64"
	"strings"

	"lukechampine.com/blake3"
)

// CanonicalizeURL applies the AdCP `url_hash` canonicalization rules
// (content-deduplication form, distinct from the URL-identifier
// canonicalization documented at docs/reference/url-canonicalization):
//
//   - Strip scheme (`http://`, `https://`)
//   - Strip fragment (`#…`)
//   - Strip query (`?…`)
//   - Lowercase everything
//   - Strip leading `www.`, `m.`, or `amp.` host prefix (so mirrors of
//     the same article hash identically)
//   - Strip trailing slash from the path
//
// The result is the canonical content URL the publisher would hash and
// emit as an `ArtifactRefTypeURLHash` value. The agent uses the same
// canonicalization for `url:blocklist:{package_id}` /
// `url:allowlist:{package_id}` storage keys so a publisher's
// pre-hashed `url_hash` artifact-ref is byte-identical to the
// internally-hashed form of the same canonical URL.
//
// This is NOT the URL-identifier canonicalization used for request
// signing, `adagents.json` lookups, or registry-key comparison. That
// algorithm preserves scheme and full path semantics; see urlutil and
// the AdCP reference URL canonicalization page.
func CanonicalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	if idx := strings.Index(s, "://"); idx != -1 {
		s = s[idx+3:]
	}

	if idx := strings.IndexByte(s, '#'); idx != -1 {
		s = s[:idx]
	}

	if idx := strings.IndexByte(s, '?'); idx != -1 {
		s = s[:idx]
	}

	s = strings.ToLower(s)

	host := s
	path := ""
	if idx := strings.IndexByte(s, '/'); idx != -1 {
		host = s[:idx]
		path = s[idx:]
	}

	for _, prefix := range []string{"www.", "m.", "amp."} {
		if strings.HasPrefix(host, prefix) {
			host = host[len(prefix):]
			break
		}
	}

	path = strings.TrimRight(path, "/")

	return host + path
}

// HashURL returns the AdCP `url_hash` artifact-ref value for raw: the
// standard base64 (RFC 4648 §4) encoding of the Blake3-256 digest of
// CanonicalizeURL(raw). 44 characters fixed (32-byte digest, base64
// padding included). Matches the wire shape publishers use to populate
// `ArtifactRefTypeURLHash`, and the storage shape the context-agent
// uses for url:blocklist:* / url:allowlist:* set members. One hashing
// primitive serves both sides; a publisher's pre-hashed url_hash on
// the wire is byte-equal to what the agent would compute from a raw
// URL of the same canonical content.
//
// Spec reference: AdCP context-match-request schema, `ArtifactRef.value`
// docstring for `url_hash` type.
func HashURL(raw string) string {
	canonical := CanonicalizeURL(raw)
	sum := blake3.Sum256([]byte(canonical))
	return base64.StdEncoding.EncodeToString(sum[:])
}
