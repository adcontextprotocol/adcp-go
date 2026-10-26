package scope3

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultMaxResponseBytes is the size cap applied to an identity-config
// response when the caller does not set one: 32 MiB.
//
// The config endpoint is remote input, and the identity agent runs inside a
// TEE with a fixed memory budget — a single oversized (or endless) response
// is enough to OOM-kill the agent before the JSON decoder finishes, so the
// cap is on by default rather than opt-in.
//
// 32 MiB is sized for this payload with room to spare: a config entry costs
// roughly 200 bytes on the wire (seller agent URL, package ID, and a handful
// of audience segment IDs), so the cap clears ~160k entries against the
// hundreds-to-low-thousands that deployments actually carry. Operators whose
// snapshot legitimately outgrows it raise the cap with WithMaxResponseBytes
// (CONFIG_SOURCE_MAX_RESPONSE_BYTES on the reference identity agent) rather
// than running unbounded.
//
// A byte cap alone is not a memory budget: see DefaultMaxConfigEntries for
// the cap that actually bounds the decoded object graph.
const DefaultMaxResponseBytes int64 = 32 << 20

// DefaultMaxConfigEntries is the cap on how much one response may
// materialize: 250k units, where a unit is one targetingConfigs entry, one
// removedTargetingConfigs entry, or one segment ID inside a targeting rule.
//
// The byte cap does not bound what a response costs once decoded, because
// the wire cost of that structure is the peer's to choose:
//
//   - A real config entry spends ~200 bytes; a hostile one spends three
//     ("{},"), and each still becomes an identityconfig.Entry plus its
//     slots in the service's byKey and bySeller indexes. Measured, 2 MiB of
//     minimal entries decodes to ~156 MiB live — ~78x.
//   - A rule's segment lists are unbounded, so a peer that cannot afford
//     many entries spends the whole byte budget on one entry whose anyOf
//     holds millions of IDs. Measured, 30 MiB of wire peaked at ~650 MiB
//     with one entry decoded — and Service.GetBySeller deep-copies that
//     rule on every request that omits package_ids.
//
// So the response is streamed and every unit charged as it arrives (see
// decodeResponse), which also keeps the decoder from ever buffering a whole
// element. 250k units measures ~60 MiB of live snapshot — byKey, bySeller,
// and the prior snapshot held live during the swap — and is two orders of
// magnitude above what deployments carry, while a legitimate snapshot that
// large would hit the byte cap first. Raise both together with
// WithMaxConfigEntries / WithMaxResponseBytes if a deployment needs it.
//
// The cap is per response. identityconfig.Service accumulates deltas, so
// the installed snapshot is the sum of what the endpoint has sent over the
// process's life; bounding that is the service's job, not this one's.
const DefaultMaxConfigEntries int = 250_000

// UnlimitedConfigEntries disables the entry cap. Pass it explicitly, and
// only for a config endpoint you control.
const UnlimitedConfigEntries int = -1

// UnlimitedResponseBytes disables the response-size cap. It is never the
// default — pass it explicitly, and only for a config endpoint you control.
const UnlimitedResponseBytes int64 = -1

// maxDrainBytes bounds the post-response drain. Draining unread bytes lets
// the connection go back to the pool, but that is only worth a small, finite
// read: an unbounded drain hands a peer that never stops sending a way to
// stall every refresh. Past this bound the connection is dropped instead.
const maxDrainBytes int64 = 64 << 10

// ErrResponseTooLarge is the sentinel every response-size rejection matches
// with errors.Is. Use errors.As with *ResponseTooLargeError for the details.
var ErrResponseTooLarge = errors.New("scope3: identity-config response exceeds the configured size limit")

// ResponseTooLargeError reports that an identity-config response was rejected
// for exceeding the size cap. The response is never silently truncated: the
// read that crosses the cap fails with this error, so a short snapshot can
// never be mistaken for a complete one and applied over good config.
type ResponseTooLargeError struct {
	// Limit is the cap that was exceeded, in bytes.
	Limit int64

	// Read is the number of bytes counted when the cap tripped. It is 0 when
	// the response was rejected on its declared Content-Length, before the
	// body was read at all.
	Read int64

	// ContentLength is the Content-Length the response declared, or -1 when
	// it declared none (chunked, or transparently decompressed).
	ContentLength int64

	// Declared is true when the rejection happened up front on
	// ContentLength, before any body bytes were read.
	Declared bool
}

func (e *ResponseTooLargeError) Error() string {
	if e.Declared {
		return fmt.Sprintf(
			"scope3: identity-config response declared Content-Length %d, over the %d-byte limit",
			e.ContentLength, e.Limit,
		)
	}
	return fmt.Sprintf(
		"scope3: identity-config response exceeded the %d-byte limit after %d bytes",
		e.Limit, e.Read,
	)
}

// Is makes every ResponseTooLargeError match errors.Is(err, ErrResponseTooLarge).
func (e *ResponseTooLargeError) Is(target error) bool { return target == ErrResponseTooLarge }

// ErrTooManyEntries is the sentinel every entry-count rejection matches with
// errors.Is. Use errors.As with *TooManyEntriesError for the details.
var ErrTooManyEntries = errors.New("scope3: identity-config response exceeds the configured entry limit")

// TooManyEntriesError reports that a response was refused because it
// carried more than the decoder is willing to materialize — entries and
// segment IDs together. Like the byte cap, it fails the decode rather than
// keeping what it already read: a half-read snapshot applied over good
// config silently drops packages.
type TooManyEntriesError struct {
	// Limit is the entry cap that was exceeded.
	Limit int
}

func (e *TooManyEntriesError) Error() string {
	return fmt.Sprintf("scope3: identity-config response exceeds the %d-entry limit", e.Limit)
}

// Is makes every TooManyEntriesError match errors.Is(err, ErrTooManyEntries).
func (e *TooManyEntriesError) Is(target error) bool { return target == ErrTooManyEntries }

// limitedBody counts bytes as they stream in and fails the read that crosses
// the cap. It never reports a short read as a success, so json.Decoder
// surfaces the cap error rather than a truncated value.
//
// The bytes counted are the ones the caller reads. Source sends no
// Accept-Encoding of its own, so net/http negotiates gzip and inflates
// transparently, and the counter sees the decompressed size — a compressed
// bomb trips the cap at its inflated size, not its wire size.
type limitedBody struct {
	inner         io.Reader
	limit         int64
	contentLength int64

	read int64
	err  error // sticky: the cap error, or the inner reader's error
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	// Never pull more than one byte past the cap: that byte is what proves
	// the body is oversized, and nothing beyond it is ever buffered. The
	// comparison is written to keep allowed+1 from overflowing on a caller
	// who passed a limit near math.MaxInt64.
	if allowed := b.limit - b.read; allowed < int64(len(p))-1 {
		p = p[:allowed+1]
	}

	n, err := b.inner.Read(p)
	if n > 0 {
		b.read += int64(n)
		if b.read > b.limit {
			b.err = &ResponseTooLargeError{
				Limit:         b.limit,
				Read:          b.read,
				ContentLength: b.contentLength,
			}
			return 0, b.err
		}
	}
	if err != nil {
		b.err = err
	}
	return n, err
}

// drainAndClose discards up to maxDrainBytes of unread body so the
// connection can be pooled, then closes it. The byte bound keeps a peer that
// answers with an endless body from stalling the refresh here; a peer that
// instead trickles bytes slowly is bounded by the HTTP client's timeout
// (WithHTTPTimeout rejects a non-positive one, and WithHTTPClient hands that
// choice to the caller).
//
// skip goes straight to Close for a response that already disqualified
// itself — there is no connection left worth saving, so spending reads on it
// buys nothing.
func drainAndClose(body io.ReadCloser, skip bool) error {
	if !skip {
		_, _ = io.CopyN(io.Discard, body, maxDrainBytes)
	}
	return body.Close()
}

// ErrUnsupportedContentEncoding is the sentinel for a response whose content
// coding the size cap cannot account for. Match it with errors.Is.
var ErrUnsupportedContentEncoding = errors.New("scope3: identity-config response uses a content coding the size cap cannot account for")

// UnsupportedContentEncodingError reports that the config endpoint answered
// with a Content-Encoding that was never offered and that net/http did not
// decode. Nothing downstream inflates it, so its size on the wire says
// nothing about what it would cost — the response is refused rather than
// counted at a meaningless size.
type UnsupportedContentEncodingError struct {
	// Encoding is the coding the peer declared, validated against the HTTP
	// token charset and truncated. It is "unrecognized" when the peer's
	// value was not a well-formed token, so a peer cannot smuggle arbitrary
	// bytes into an operator's logs through this error.
	Encoding string
}

func (e *UnsupportedContentEncodingError) Error() string {
	return "scope3: identity-config response declared unsupported Content-Encoding " + e.Encoding
}

// Is makes every UnsupportedContentEncodingError match
// errors.Is(err, ErrUnsupportedContentEncoding).
func (e *UnsupportedContentEncodingError) Is(target error) bool {
	return target == ErrUnsupportedContentEncoding
}

// unsupportedContentCoding returns the content coding of a response the cap
// cannot size-account for, or "" when the response is safe (absent, empty,
// or identity).
func unsupportedContentCoding(contentEncoding string) string {
	coding := strings.TrimSpace(contentEncoding)
	if coding == "" || strings.EqualFold(coding, "identity") {
		return ""
	}
	const maxTokenLen = 32
	if len(coding) > maxTokenLen {
		coding = coding[:maxTokenLen]
	}
	for i := range len(coding) {
		if !isTokenByte(coding[i]) {
			return "unrecognized"
		}
	}
	return coding
}

// sanitizeSnippet makes a prefix of a peer-controlled error body safe to
// put in a log line: control bytes (newlines above all, which would let the
// peer forge log records) and non-ASCII become '.', and the result is
// truncated. The body of an error response is worth keeping — it is how an
// operator tells an expired token from a bad path — but not verbatim.
func sanitizeSnippet(b []byte) string {
	const maxSnippetLen = 256
	if len(b) > maxSnippetLen {
		b = b[:maxSnippetLen]
	}
	out := make([]byte, len(b))
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			out[i] = '.'
			continue
		}
		out[i] = c
	}
	return string(out)
}

// isTokenByte reports whether c is allowed in an RFC 9110 token.
func isTokenByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// maxResponseHeaderBytes bounds the response header block: 64 KiB.
// net/http's own default is 10 MiB, which a hostile endpoint can spend in
// full on headers — memory read before any body cap is in play. 64 KiB is
// far above anything this endpoint legitimately needs.
const maxResponseHeaderBytes int64 = 64 << 10

// boundedHeaderTransport clones http.DefaultTransport and lowers its
// response-header ceiling. Cloning rather than mutating is mandatory:
// http.DefaultTransport is process-global shared state. Callers who want a
// different bound supply their own client with WithHTTPClient.
//
// The clone brings its own idle-connection pool, which nothing here ever
// closes. That suits the one-Source-per-process lifecycle the reference
// agent has; an embedder that rebuilds Sources (per-tenant reload, say)
// should supply a shared client with WithHTTPClient instead of leaving a
// pool behind on every rebuild.
func boundedHeaderTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// A host application replaced http.DefaultTransport. Honor it
		// rather than second-guessing, and leave its limits alone.
		return http.DefaultTransport
	}
	cloned := base.Clone()
	cloned.MaxResponseHeaderBytes = maxResponseHeaderBytes
	return cloned
}
