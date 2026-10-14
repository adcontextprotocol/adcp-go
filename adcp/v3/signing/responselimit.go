package signing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// DefaultMaxResponseBytes is the response-size cap applied to every agent
// response when the caller does not set one: 4 MiB.
//
// Agent responses are untrusted input. A peer that answers a single call with
// a multi-megabyte body can drive a buyer, crawler, or compliance grader into
// an out-of-memory kill long before any JSON decoding finishes, so the cap is
// on by default rather than opt-in. Callers that legitimately talk to agents
// with very large catalogs raise it (see SignedHTTPClientOptions and
// WithResponseLimits); 4 MiB matches the cap the hosted AdCP compliance
// tooling runs with.
const DefaultMaxResponseBytes int64 = 4 << 20

// UnlimitedResponseBytes disables a response-size cap. It is never the
// default — pass it explicitly, and only for a peer you control.
const UnlimitedResponseBytes int64 = -1

// ResponseLimitScope identifies which cap a *ResponseTooLargeError tripped.
type ResponseLimitScope string

const (
	// ScopeResponseBody — the cumulative bytes of one response body
	// (equivalently, one AdCP call) exceeded MaxResponseBytes.
	ScopeResponseBody ResponseLimitScope = "response_body"

	// ScopeSSEEvent — a single Server-Sent Event in a text/event-stream
	// response exceeded MaxSSEEventBytes.
	ScopeSSEEvent ResponseLimitScope = "sse_event"
)

// ErrResponseTooLarge is the sentinel every response-size rejection matches
// with errors.Is. Use errors.As with *ResponseTooLargeError for the details.
var ErrResponseTooLarge = errors.New("adcp: agent response exceeds the configured size limit")

// ResponseTooLargeError reports that an agent response was rejected for
// exceeding a response-size cap. The response is never silently truncated:
// the read that crosses the cap fails with this error, and callers that
// buffer the body (json.Decode, io.ReadAll) surface it.
type ResponseTooLargeError struct {
	// Scope says which cap tripped — the whole body or one SSE event.
	Scope ResponseLimitScope

	// Limit is the cap that was exceeded, in bytes.
	Limit int64

	// Read is the number of bytes counted when the cap tripped. It is 0 when
	// the response was rejected on its declared Content-Length, before the
	// body was read at all.
	Read int64

	// ContentLength is the Content-Length the response declared, or -1 when
	// it declared none (chunked, SSE, or transparently decompressed).
	ContentLength int64

	// Declared is true when the rejection happened up front on
	// ContentLength, before any body bytes were read.
	Declared bool
}

func (e *ResponseTooLargeError) Error() string {
	if e.Declared {
		return fmt.Sprintf(
			"adcp: agent response declared Content-Length %d, over the %d-byte limit",
			e.ContentLength, e.Limit,
		)
	}
	if e.Scope == ScopeSSEEvent {
		return fmt.Sprintf(
			"adcp: agent SSE event exceeded the %d-byte limit after %d bytes",
			e.Limit, e.Read,
		)
	}
	return fmt.Sprintf(
		"adcp: agent response exceeded the %d-byte limit after %d bytes",
		e.Limit, e.Read,
	)
}

// Is makes every ResponseTooLargeError match errors.Is(err, ErrResponseTooLarge).
func (e *ResponseTooLargeError) Is(target error) bool { return target == ErrResponseTooLarge }

// ResponseLimits bounds how much memory one agent response may cost the
// caller. The zero value means "use the defaults" — a 4 MiB body cap, with
// the same number as the per-event cap for SSE responses.
//
// For both fields: zero means "inherit" (the package default on a transport,
// the transport's value on a per-call override) and a negative value —
// UnlimitedResponseBytes — disables that cap.
type ResponseLimits struct {
	// MaxResponseBytes caps the cumulative bytes read from one response
	// body. For a streamed (SSE) response this is the per-call cumulative
	// cap, so a peer cannot escape the limit by answering with
	// Content-Type: text/event-stream and dribbling out events.
	MaxResponseBytes int64

	// MaxSSEEventBytes caps a single event inside a text/event-stream
	// response. Zero inherits MaxResponseBytes. Set it lower than
	// MaxResponseBytes when you need a long-lived stream of small events:
	// each event stays bounded while the call as a whole is allowed to run
	// past the per-event cap.
	MaxSSEEventBytes int64
}

// resolve turns the inherit/disable sentinels into concrete byte counts.
// A negative result means "no cap".
func (l ResponseLimits) resolve() ResponseLimits {
	out := l
	switch {
	case out.MaxResponseBytes == 0:
		out.MaxResponseBytes = DefaultMaxResponseBytes
	case out.MaxResponseBytes < 0:
		out.MaxResponseBytes = UnlimitedResponseBytes
	}
	switch {
	case out.MaxSSEEventBytes == 0:
		out.MaxSSEEventBytes = out.MaxResponseBytes
	case out.MaxSSEEventBytes < 0:
		out.MaxSSEEventBytes = UnlimitedResponseBytes
	}
	return out
}

// merge layers a per-call override over the transport's limits: a zero field
// on the override inherits the transport's value, a non-zero field (including
// a negative "unlimited") wins.
func (l ResponseLimits) merge(override ResponseLimits) ResponseLimits {
	out := l
	if override.MaxResponseBytes != 0 {
		out.MaxResponseBytes = override.MaxResponseBytes
	}
	if override.MaxSSEEventBytes != 0 {
		out.MaxSSEEventBytes = override.MaxSSEEventBytes
	}
	return out
}

type responseLimitsKey struct{}

// WithResponseLimits overrides the response-size caps for the calls made with
// the returned context — the per-call escape hatch for an operation you know
// returns a large catalog:
//
//	ctx := signing.WithResponseLimits(r.Context(), signing.ResponseLimits{
//	    MaxResponseBytes: 32 << 20,
//	})
//	req, _ := http.NewRequestWithContext(ctx, "POST", agentURL, body)
//	resp, err := client.Do(req) // 32 MiB for this call only
//
// A zero field inherits the client's configured value; a negative field
// (UnlimitedResponseBytes) disables that cap for the call.
func WithResponseLimits(ctx context.Context, limits ResponseLimits) context.Context {
	return context.WithValue(ctx, responseLimitsKey{}, limits)
}

// responseLimitsFromContext returns the per-call override, if any.
func responseLimitsFromContext(ctx context.Context) (ResponseLimits, bool) {
	limits, ok := ctx.Value(responseLimitsKey{}).(ResponseLimits)
	return limits, ok
}

// ResponseLimitTransport caps the size of every response that passes through
// it, failing with a *ResponseTooLargeError instead of truncating.
//
// It enforces the cap while the body streams: the declared Content-Length is
// rejected up front, and otherwise the counter trips on the read that crosses
// the limit, so no more than limit+1 bytes are ever pulled from the peer. A
// check run after the body is buffered would not prevent the out-of-memory
// kill this guards against.
//
// Wrap it around the outermost transport so it counts the bytes the caller
// actually reads. With net/http's default transparent gzip (the caller sets
// no Accept-Encoding header), that is the *decompressed* byte count, so a
// compressed bomb is caught at its inflated size. A caller that sets
// Accept-Encoding itself owns the decoding, and therefore owns bounding it —
// the cap then applies to the encoded bytes it reads off the wire.
type ResponseLimitTransport struct {
	// Inner is the transport to wrap. Nil means http.DefaultTransport.
	Inner http.RoundTripper

	// Limits are the default caps for requests without a per-call override.
	// The zero value means DefaultMaxResponseBytes.
	Limits ResponseLimits
}

// NewResponseLimitTransport wraps inner so every response it returns is
// capped. Pass the zero ResponseLimits for the 4 MiB default.
func NewResponseLimitTransport(inner http.RoundTripper, limits ResponseLimits) *ResponseLimitTransport {
	return &ResponseLimitTransport{Inner: inner, Limits: limits}
}

func (t *ResponseLimitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}

	limits := t.Limits
	if override, ok := responseLimitsFromContext(r.Context()); ok {
		limits = limits.merge(override)
	}
	limits = limits.resolve()

	resp, err := inner.RoundTrip(r)
	if err != nil {
		return nil, err
	}

	// A response that carries no body has nothing to cap. HEAD, 204, and 304
	// still echo the resource's Content-Length without sending those bytes,
	// so pre-rejecting on it would fail a transfer that never happens; a 101
	// hands back the raw upgraded connection, which is not ours to limit.
	if !responseCarriesBody(r, resp) {
		return resp, nil
	}

	// Pre-reject on the declared length so an oversized body is never read.
	// resp.ContentLength is -1 when none was declared, and net/http already
	// clears it on responses it transparently decompressed.
	if limits.MaxResponseBytes >= 0 && resp.ContentLength > limits.MaxResponseBytes {
		_ = resp.Body.Close()
		return nil, &ResponseTooLargeError{
			Scope:         ScopeResponseBody,
			Limit:         limits.MaxResponseBytes,
			ContentLength: resp.ContentLength,
			Declared:      true,
		}
	}

	maxEvent := UnlimitedResponseBytes
	if isEventStream(resp.Header.Get("Content-Type")) {
		maxEvent = limits.MaxSSEEventBytes
	}
	if limits.MaxResponseBytes < 0 && maxEvent < 0 {
		return resp, nil
	}

	resp.Body = &limitedResponseBody{
		inner:         resp.Body,
		maxBody:       limits.MaxResponseBytes,
		maxEvent:      maxEvent,
		contentLength: resp.ContentLength,
	}
	return resp, nil
}

// responseCarriesBody reports whether the response can carry a body this
// transport is responsible for bounding.
func responseCarriesBody(req *http.Request, resp *http.Response) bool {
	if resp.Body == nil || resp.Body == http.NoBody {
		return false
	}
	if req != nil && req.Method == http.MethodHead {
		return false
	}
	// 1xx (including a 101 upgrade's raw connection), 204, and 304 never
	// carry a response body.
	if resp.StatusCode < 200 {
		return false
	}
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotModified:
		return false
	}
	return true
}

// isEventStream reports whether the Content-Type is SSE.
func isEventStream(contentType string) bool {
	if contentType == "" {
		return false
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		return mediaType == "text/event-stream"
	}
	// Unparseable Content-Type: fall back to a prefix match rather than
	// letting a malformed header slip the per-event cap.
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(contentType)), "text/event-stream")
}

// limitedResponseBody counts bytes as they stream in and fails the read that
// crosses a cap. It never returns a short read as a success, so a caller
// cannot mistake a truncated body for a complete one.
type limitedResponseBody struct {
	inner         io.ReadCloser
	maxBody       int64 // negative: no cumulative cap
	maxEvent      int64 // negative: not SSE, or no per-event cap
	contentLength int64

	read      int64 // cumulative bytes read from inner
	eventRead int64 // bytes read since the last SSE event boundary

	// SSE line-terminator state, carried across Read boundaries. SSE treats
	// CRLF, CR, and LF alike, and two in a row end an event.
	newlineRun int
	afterCR    bool

	err error // sticky: the cap error, or the inner body's error
}

func (b *limitedResponseBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	// Never pull more than one byte past the cap: that byte is what proves
	// the body is oversized, and nothing beyond it is ever buffered.
	if b.maxBody >= 0 {
		if allowed := b.maxBody - b.read + 1; int64(len(p)) > allowed {
			p = p[:allowed]
		}
	}

	n, err := b.inner.Read(p)
	if n > 0 {
		b.read += int64(n)
		if b.maxBody >= 0 && b.read > b.maxBody {
			b.err = &ResponseTooLargeError{
				Scope:         ScopeResponseBody,
				Limit:         b.maxBody,
				Read:          b.read,
				ContentLength: b.contentLength,
			}
			return 0, b.err
		}
		if b.maxEvent >= 0 {
			if eventErr := b.scanEvents(p[:n]); eventErr != nil {
				b.err = eventErr
				return 0, b.err
			}
		}
	}
	if err != nil {
		b.err = err
	}
	return n, err
}

// scanEvents tracks SSE event boundaries across the chunk and fails as soon
// as one event grows past maxEvent.
func (b *limitedResponseBody) scanEvents(chunk []byte) error {
	for _, c := range chunk {
		b.eventRead++
		switch c {
		case '\r':
			b.newlineRun++
			b.afterCR = true
		case '\n':
			// The LF of a CRLF pair is part of the terminator the CR
			// already counted.
			if !b.afterCR {
				b.newlineRun++
			}
			b.afterCR = false
		default:
			b.newlineRun = 0
			b.afterCR = false
		}
		if b.newlineRun >= 2 {
			// Blank line: the event ended. Start counting the next one.
			b.newlineRun = 0
			b.eventRead = 0
			continue
		}
		if b.eventRead > b.maxEvent {
			return &ResponseTooLargeError{
				Scope:         ScopeSSEEvent,
				Limit:         b.maxEvent,
				Read:          b.eventRead,
				ContentLength: b.contentLength,
			}
		}
	}
	return nil
}

func (b *limitedResponseBody) Close() error { return b.inner.Close() }
