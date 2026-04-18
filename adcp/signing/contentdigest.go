package signing

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// computeSHA256DigestHeader returns the RFC 9530 Content-Digest header value
// for body computed with SHA-256: `sha-256=:<base64 std>:`.
func computeSHA256DigestHeader(body []byte) string {
	h := sha256.Sum256(body)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(h[:]) + ":"
}

// extractSHA256FromDigestHeader parses a Content-Digest header value and
// returns the sha-256 digest as raw bytes.
//
// Accepts the structured-field dict form:
//   sha-256=:<base64>:, algorithm2=:<base64>:
//
// Returns nil bytes if the sha-256 algorithm is not present.
func extractSHA256FromDigestHeader(header string) ([]byte, bool, error) {
	rest := strings.TrimSpace(header)
	for len(rest) > 0 {
		// strip leading OWS / commas
		for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == ',') {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			break
		}
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			return nil, false, newError(CodeHeaderMalformed, "Content-Digest missing =")
		}
		alg := strings.ToLower(strings.TrimSpace(rest[:eq]))
		rest = rest[eq+1:]
		rest = strings.TrimLeft(rest, " \t")
		if len(rest) == 0 || rest[0] != ':' {
			return nil, false, newError(CodeHeaderMalformed, "Content-Digest value missing :")
		}
		end := strings.IndexByte(rest[1:], ':')
		if end < 0 {
			return nil, false, newError(CodeHeaderMalformed, "Content-Digest value unterminated")
		}
		val := rest[1 : 1+end]
		if alg == "sha-256" {
			raw, err := base64.StdEncoding.DecodeString(val)
			if err != nil {
				return nil, false, newError(CodeHeaderMalformed, "Content-Digest base64 invalid")
			}
			return raw, true, nil
		}
		rest = rest[2+end:]
		// skip trailing params on this alg (";key=value" up to next "," at depth 0)
		rest = skipDigestParams(rest)
	}
	return nil, false, nil
}

// skipDigestParams skips any ";name=value" suffixes up to the next top-level
// "," or end of string.
func skipDigestParams(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == ',':
			return s[i:]
		}
	}
	return ""
}
