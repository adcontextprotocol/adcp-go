// Package scope3 implements an identityconfig.Source backed by a Scope3
// HTTP endpoint that returns the seller-keyed identity configs.
//
// Wire contract:
//
//	POST <configured URL>
//	Authorization: Bearer <configured token>
//	Content-Type: application/json
//
//	{ "after": "2026-05-13T10:42:43.123456789Z" }   (optional)
//
//	→ 200 OK
//	{
//	  "lastUpdatedAt": "2026-05-13T11:00:00.000000000Z",
//	  "targetingConfigs": [
//	    {
//	      "sellerAgentUrl": "https://seller.example.com/agent",
//	      "packageId": "pkg-1",
//	      "targetSegments": { "allOf": [...], "anyOf": [...], "noneOf": [...] }
//	    }
//	  ],
//	  "removedTargetingConfigs": [
//	    { "sellerAgentUrl": "...", "packageId": "..." }
//	  ]
//	}
//
// The endpoint is remote input, so every response is read under two caps: a
// size cap on the wire (DefaultMaxResponseBytes, 32 MiB) and a count cap on
// what it materializes (DefaultMaxConfigEntries, 250k entries and segment
// IDs together). Crossing either fails
// the whole response — *ResponseTooLargeError or *TooManyEntriesError —
// rather than yielding a truncated snapshot, the response header block is
// bounded well below net/http's 10 MiB default, and the unread remainder of
// a response is drained only up to a bounded number of bytes. So neither an
// oversized, a maliciously dense, nor an endless response can exhaust the
// agent.
package scope3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
)

// Source posts identity-config queries to a configurable URL with Bearer
// authentication and parses the JSON response into identityconfig types.
type Source struct {
	url          string
	token        string
	client       *http.Client
	customClient bool // true when client was set via WithHTTPClient
	extraHeaders http.Header

	// httpTimeout is applied to the Source's own client after all options
	// have run, so WithHTTPTimeout and WithHTTPClient compose the same way
	// in either order.
	httpTimeout    time.Duration
	httpTimeoutSet bool

	// maxResponseBytes caps the response body. Resolved in New: zero means
	// DefaultMaxResponseBytes, negative means no cap.
	maxResponseBytes int64

	// maxConfigEntries caps how many entries one response may decode to.
	// Resolved in New: zero means DefaultMaxConfigEntries, negative means
	// no cap.
	maxConfigEntries int
}

// reservedHeaderNames are the headers Source manages itself.
// WithExtraHeaders rejects entries that collide with these — silently
// overriding Authorization would be a surprising footgun.
//
// Accept-Encoding is reserved even though Source never sets it: leaving
// content negotiation to net/http is what makes the response-size cap count
// decompressed bytes (see limitedBody). A caller-supplied Accept-Encoding
// suppresses net/http's transparent gzip and leaves the cap counting wire
// bytes, so it is refused rather than silently weakening the cap.
var reservedHeaderNames = []string{"Authorization", "Content-Type", "Accept", "Accept-Encoding"}

// Option configures the Source at construction time.
type Option func(*Source) error

// WithHTTPClient supplies a pre-configured *http.Client. Useful for custom
// transports, dial timeouts, or test fakes. Suppresses WithHTTPTimeout — the
// caller's client owns its own timeout.
//
// That makes the caller's client the only wall-clock bound on the exchange,
// and a config endpoint that trickles its body one byte at a time is a
// refresh that never returns. Give the client a Timeout (or always call
// through a context that carries a deadline); the Source's own client has
// one, and WithHTTPTimeout refuses to remove it.
//
// The supplied client is also used verbatim, so its transport's
// MaxResponseHeaderBytes is the caller's to bound — the Source only lowers
// that ceiling on the client it builds itself.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Source) error {
		s.client = client
		s.customClient = true
		return nil
	}
}

// WithHTTPTimeout sets the total request timeout on the Source's own HTTP
// client. Has no effect when WithHTTPClient was used — that path treats the
// caller's client as authoritative, so its Timeout is not mutated regardless
// of option order.
//
// A non-positive duration is rejected rather than applied. http.Client
// reads it as "no timeout", and that timeout is the only wall-clock bound
// on a peer that trickles its response one byte at a time — a refresh that
// never returns leaves the agent serving a frozen snapshot with no error
// ever logged. Callers that really want an unbounded client build one with
// WithHTTPClient, where the choice is explicit; combined with
// WithHTTPClient this option is ignored whole, validation included, in
// either order.
func WithHTTPTimeout(d time.Duration) Option {
	return func(s *Source) error {
		s.httpTimeout = d
		s.httpTimeoutSet = true
		return nil
	}
}

// WithExtraHeaders adds caller-supplied headers to every outbound request.
// Names are matched case-insensitively against the headers Source manages
// itself (Authorization, Content-Type, Accept) and a collision is rejected
// so a misconfigured value can't silently strip auth. Empty names are also
// rejected. Values are sent verbatim.
//
// Repeated calls merge: later values for the same canonical header name
// overwrite earlier ones.
func WithExtraHeaders(headers map[string]string) Option {
	return func(s *Source) error {
		if len(headers) == 0 {
			return nil
		}
		if s.extraHeaders == nil {
			s.extraHeaders = http.Header{}
		}
		for name, value := range headers {
			if strings.TrimSpace(name) == "" {
				return errors.New("scope3: extra header name must be non-empty")
			}
			canonical := http.CanonicalHeaderKey(name)
			for _, reserved := range reservedHeaderNames {
				if canonical == reserved {
					return fmt.Errorf("scope3: extra header %q collides with reserved header %q", name, reserved)
				}
			}
			s.extraHeaders.Set(canonical, value)
		}
		return nil
	}
}

// WithMaxResponseBytes caps the identity-config response body. A response
// that crosses the cap fails with a *ResponseTooLargeError (matching
// errors.Is(err, ErrResponseTooLarge)) instead of being truncated, so a
// partial snapshot is never applied over good config.
//
// Zero keeps DefaultMaxResponseBytes, so an unset value stays protected.
// UnlimitedResponseBytes — any negative value — opts out entirely; only do
// that for a config endpoint you control.
func WithMaxResponseBytes(n int64) Option {
	return func(s *Source) error {
		s.maxResponseBytes = n
		return nil
	}
}

// WithMaxConfigEntries caps how much one response may materialize: every
// targetingConfigs entry, every removedTargetingConfigs entry, and every
// segment ID inside a targeting rule costs one unit against it. A response
// that runs over fails with a *TooManyEntriesError (matching
// errors.Is(err, ErrTooManyEntries)) and is discarded whole.
//
// This is the cap that bounds memory: the byte cap bounds the wire, and the
// peer chooses how much structure it packs into those bytes. Zero keeps
// DefaultMaxConfigEntries; UnlimitedConfigEntries — any negative value —
// opts out.
func WithMaxConfigEntries(n int) Option {
	return func(s *Source) error {
		s.maxConfigEntries = n
		return nil
	}
}

// New constructs a Source. URL and bearer token are required.
func New(url, bearerToken string, opts ...Option) (*Source, error) {
	if url == "" {
		return nil, errors.New("scope3: URL is required")
	}
	if bearerToken == "" {
		return nil, errors.New("scope3: bearer token is required")
	}
	s := &Source{
		url:   url,
		token: bearerToken,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: boundedHeaderTransport(),
		},
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	if s.httpTimeoutSet && !s.customClient {
		if s.httpTimeout <= 0 {
			return nil, errors.New("scope3: HTTP timeout must be positive")
		}
		s.client.Timeout = s.httpTimeout
	}
	switch {
	case s.maxResponseBytes == 0:
		s.maxResponseBytes = DefaultMaxResponseBytes
	case s.maxResponseBytes < 0:
		s.maxResponseBytes = UnlimitedResponseBytes
	}
	switch {
	case s.maxConfigEntries == 0:
		s.maxConfigEntries = DefaultMaxConfigEntries
	case s.maxConfigEntries < 0:
		s.maxConfigEntries = UnlimitedConfigEntries
	}
	return s, nil
}

// LoadAll fetches the entire current state of the world.
func (s *Source) LoadAll(ctx context.Context) (identityconfig.Snapshot, error) {
	body, err := s.post(ctx, requestBody{})
	if err != nil {
		return identityconfig.Snapshot{}, err
	}
	return identityconfig.Snapshot{
		Configs:       toEntries(body.TargetingConfigs),
		LastUpdatedAt: body.LastUpdatedAt,
	}, nil
}

// LoadUpdatedAfter fetches the configs changed since `after`.
func (s *Source) LoadUpdatedAfter(ctx context.Context, after time.Time) (identityconfig.Delta, error) {
	body, err := s.post(ctx, requestBody{After: &after})
	if err != nil {
		return identityconfig.Delta{}, err
	}
	return identityconfig.Delta{
		Upserted:      toEntries(body.TargetingConfigs),
		Removed:       toKeys(body.RemovedTargetingConfigs),
		LastUpdatedAt: body.LastUpdatedAt,
	}, nil
}

type requestBody struct {
	After *time.Time `json:"after,omitempty"`
}

type responseBody struct {
	LastUpdatedAt           time.Time          `json:"lastUpdatedAt"`
	TargetingConfigs        []wireConfig       `json:"targetingConfigs"`
	RemovedTargetingConfigs []wireRemovedEntry `json:"removedTargetingConfigs"`
}

type wireConfig struct {
	SellerAgentURL string           `json:"sellerAgentUrl"`
	PackageID      string           `json:"packageId"`
	TargetSegments *wireSegmentRule `json:"targetSegments,omitempty"`
}

type wireRemovedEntry struct {
	SellerAgentURL string `json:"sellerAgentUrl"`
	PackageID      string `json:"packageId"`
}

type wireSegmentRule struct {
	AllOf  []string `json:"allOf,omitempty"`
	AnyOf  []string `json:"anyOf,omitempty"`
	NoneOf []string `json:"noneOf,omitempty"`
}

// toDomain projects the wire rule onto its domain twin. Slice fields alias
// the wire struct rather than deep-copying; this is safe because the wire
// struct is request-scoped and identityconfig.Service.GetBySeller clones the
// rule before exposing it to readers, so external callers never observe the
// alias. Mutating a returned *SegmentRule before that clone would corrupt
// the snapshot.
func (w *wireSegmentRule) toDomain() *targeting.SegmentRule {
	if w == nil {
		return nil
	}
	return &targeting.SegmentRule{
		AllOf:  w.AllOf,
		AnyOf:  w.AnyOf,
		NoneOf: w.NoneOf,
	}
}

func (s *Source) post(ctx context.Context, body requestBody) (out *responseBody, retErr error) {
	// Set on the paths where draining is pointless: the response has
	// already disqualified itself, so its connection is not going back in
	// the pool no matter how many bytes we read off it.
	skipDrain := false
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("scope3: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("scope3: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	maps.Copy(req.Header, s.extraHeaders)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scope3: POST %s: %w", s.url, err)
	}
	defer func() {
		// Drain a bounded number of unread bytes so the underlying
		// connection can be returned to the pool, then close. Close
		// errors are usually downstream effects of whatever produced
		// retErr on the read path — but on the rare path where the read
		// succeeded and Close still fails (or both fail for independent
		// reasons), joining preserves both for diagnostics.
		if closeErr := drainAndClose(resp.Body, skipDrain); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("scope3: close response body: %w", closeErr))
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
		if readErr != nil {
			return nil, fmt.Errorf("scope3: POST %s returned %d (body read failed: %v)", s.url, resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("scope3: POST %s returned %d: %s", s.url, resp.StatusCode, sanitizeSnippet(snippet))
	}

	// Reject on the declared length before reading a byte, then cap the
	// read itself: a chunked or transparently-decompressed response
	// declares no length (ContentLength -1), so the streaming counter is
	// what actually bounds the memory an endless body can cost.
	// With Accept-Encoding reserved, the peer was only ever offered the
	// gzip net/http negotiates and inflates itself. Anything still encoded
	// here is unsolicited: nothing downstream will inflate it, so it could
	// only ever fail the decode with a confusing syntax error — and under
	// a byte cap its inflated size is unknowable anyway. Say what actually
	// happened instead.
	if !resp.Uncompressed {
		if coding := unsupportedContentCoding(resp.Header.Get("Content-Encoding")); coding != "" {
			skipDrain = true
			return nil, &UnsupportedContentEncodingError{Encoding: coding}
		}
	}

	var reader io.Reader = resp.Body
	if s.maxResponseBytes >= 0 {
		if resp.ContentLength > s.maxResponseBytes {
			skipDrain = true
			return nil, &ResponseTooLargeError{
				Limit:         s.maxResponseBytes,
				ContentLength: resp.ContentLength,
				Declared:      true,
			}
		}
		reader = &limitedBody{
			inner:         resp.Body,
			limit:         s.maxResponseBytes,
			contentLength: resp.ContentLength,
		}
	}

	decoded, err := decodeResponse(reader, s.maxConfigEntries)
	if err != nil {
		// A body that tripped a cap has nothing left worth draining, and
		// its connection is not going back in the pool either way.
		skipDrain = errors.Is(err, ErrResponseTooLarge) || errors.Is(err, ErrTooManyEntries)
		return nil, err
	}
	return decoded, nil
}

// budget is the shared allowance for everything one response may
// materialize: every config entry, every removed-entry key, and every
// segment ID inside a targeting rule costs one unit. A negative max
// disables it.
//
// Segment IDs are charged because entries alone are the wrong unit:
// wireSegmentRule's lists are unbounded, so a peer that cannot afford many
// entries can spend the whole byte budget on one entry whose anyOf holds
// millions of strings. Measured, that shape turned 30 MiB of wire into a
// 650 MiB peak with the entry counter still reading 1 — and every later
// GetBySeller deep-copies the rule. One unit per string costs about what
// an entry does, so a flat charge needs no second knob.
type budget struct {
	max  int
	used int
}

// charge takes one unit, or reports the response as too large to accept.
func (b *budget) charge() error {
	if b.max >= 0 && b.used >= b.max {
		return &TooManyEntriesError{Limit: b.max}
	}
	b.used++
	return nil
}

// refund returns units spent on a value that was replaced by a duplicate
// key, so last-one-wins does not charge twice for entries nobody kept.
func (b *budget) refund(n int) { b.used -= n }

// decodeResponse parses the response by streaming it, materializing no more
// than maxEntries units (negative disables the cap) and never buffering a
// whole element.
//
// json.Decoder.Decode into responseBody would read the whole document
// first, and the byte cap does not bound what that costs: the peer picks
// how much structure it packs into those bytes, and each config entry
// becomes an identityconfig.Entry plus its slots in the service's indexes.
// See DefaultMaxConfigEntries for the measured amplification.
func decodeResponse(r io.Reader, maxEntries int) (*responseBody, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, wrapDecode(err)
	}

	var (
		out responseBody
		b   = budget{max: maxEntries}
	)
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, wrapDecode(err)
		}
		// Keys are matched case-insensitively, because that is what
		// encoding/json does when it matches an object key against a
		// struct tag. Matching exactly would turn a casing change at the
		// endpoint into a successful empty snapshot — the quietest way
		// there is to drop every live package.
		switch {
		case strings.EqualFold(key, "lastUpdatedAt"):
			if out.LastUpdatedAt, err = decodeTimestamp(dec); err != nil {
				return nil, err
			}
		case strings.EqualFold(key, "targetingConfigs"):
			// A repeated key replaces rather than appends, matching
			// the last-one-wins semantics of a whole-document decode.
			b.refund(countConfigUnits(out.TargetingConfigs))
			out.TargetingConfigs = nil
			if err := decodeArray(dec, &b, &out.TargetingConfigs, decodeConfig); err != nil {
				return nil, err
			}
		case strings.EqualFold(key, "removedTargetingConfigs"):
			b.refund(len(out.RemovedTargetingConfigs))
			out.RemovedTargetingConfigs = nil
			if err := decodeArray(dec, &b, &out.RemovedTargetingConfigs, decodeRemovedEntry); err != nil {
				return nil, err
			}
		default:
			// Unknown fields stay tolerated, as they were under the
			// whole-document decode — but they are walked token by
			// token rather than buffered into a json.RawMessage.
			if err := skipValue(dec); err != nil {
				return nil, wrapDecode(err)
			}
		}
	}
	// Consume the closing brace so a truncated document is an error
	// rather than a silently accepted prefix.
	if _, err := dec.Token(); err != nil {
		return nil, wrapDecode(err)
	}
	return &out, nil
}

// decodeArray appends the elements of a JSON array to out, decoding each
// with elem. A JSON null is accepted as an empty array, matching
// encoding/json.
func decodeArray[T any](dec *json.Decoder, b *budget, out *[]T, elem func(*json.Decoder, *budget) (T, error)) error {
	tok, err := dec.Token()
	if err != nil {
		return wrapDecode(err)
	}
	if tok == nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return errors.New("scope3: decode response: expected a JSON array")
	}
	for dec.More() {
		if err := b.charge(); err != nil {
			return err
		}
		v, err := elem(dec, b)
		if err != nil {
			return err
		}
		*out = append(*out, v)
	}
	if _, err := dec.Token(); err != nil {
		return wrapDecode(err)
	}
	return nil
}

// decodeConfig reads one targetingConfigs element field by field, so an
// unbounded segment list inside it is charged as it streams rather than
// materialized first.
func decodeConfig(dec *json.Decoder, b *budget) (wireConfig, error) {
	var cfg wireConfig
	if err := expectDelim(dec, '{'); err != nil {
		return cfg, wrapDecode(err)
	}
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return cfg, wrapDecode(err)
		}
		switch {
		case strings.EqualFold(key, "sellerAgentUrl"):
			if cfg.SellerAgentURL, err = decodeString(dec); err != nil {
				return cfg, err
			}
		case strings.EqualFold(key, "packageId"):
			if cfg.PackageID, err = decodeString(dec); err != nil {
				return cfg, err
			}
		case strings.EqualFold(key, "targetSegments"):
			if cfg.TargetSegments != nil {
				b.refund(cfg.TargetSegments.count())
			}
			if cfg.TargetSegments, err = decodeSegmentRule(dec, b); err != nil {
				return cfg, err
			}
		default:
			if err := skipValue(dec); err != nil {
				return cfg, wrapDecode(err)
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return cfg, wrapDecode(err)
	}
	return cfg, nil
}

// decodeRemovedEntry reads one removedTargetingConfigs element.
func decodeRemovedEntry(dec *json.Decoder, _ *budget) (wireRemovedEntry, error) {
	var entry wireRemovedEntry
	if err := expectDelim(dec, '{'); err != nil {
		return entry, wrapDecode(err)
	}
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return entry, wrapDecode(err)
		}
		switch {
		case strings.EqualFold(key, "sellerAgentUrl"):
			if entry.SellerAgentURL, err = decodeString(dec); err != nil {
				return entry, err
			}
		case strings.EqualFold(key, "packageId"):
			if entry.PackageID, err = decodeString(dec); err != nil {
				return entry, err
			}
		default:
			if err := skipValue(dec); err != nil {
				return entry, wrapDecode(err)
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return entry, wrapDecode(err)
	}
	return entry, nil
}

// decodeSegmentRule reads a targetSegments object, charging every segment
// ID against the shared budget. A JSON null yields a nil rule, as it did
// under the whole-document decode.
func decodeSegmentRule(dec *json.Decoder, b *budget) (*wireSegmentRule, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, wrapDecode(err)
	}
	if tok == nil {
		return nil, nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("scope3: decode response: targetSegments is not a JSON object")
	}
	rule := &wireSegmentRule{}
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, wrapDecode(err)
		}
		switch {
		case strings.EqualFold(key, "allOf"):
			b.refund(len(rule.AllOf))
			rule.AllOf = nil
			err = decodeSegmentIDs(dec, b, &rule.AllOf)
		case strings.EqualFold(key, "anyOf"):
			b.refund(len(rule.AnyOf))
			rule.AnyOf = nil
			err = decodeSegmentIDs(dec, b, &rule.AnyOf)
		case strings.EqualFold(key, "noneOf"):
			b.refund(len(rule.NoneOf))
			rule.NoneOf = nil
			err = decodeSegmentIDs(dec, b, &rule.NoneOf)
		default:
			err = wrapDecodeOrNil(skipValue(dec))
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, wrapDecode(err)
	}
	return rule, nil
}

// decodeSegmentIDs reads one segment-ID list, charging a unit per ID.
func decodeSegmentIDs(dec *json.Decoder, b *budget, out *[]string) error {
	tok, err := dec.Token()
	if err != nil {
		return wrapDecode(err)
	}
	if tok == nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return errors.New("scope3: decode response: segment list is not a JSON array")
	}
	for dec.More() {
		if err := b.charge(); err != nil {
			return err
		}
		id, err := decodeString(dec)
		if err != nil {
			return err
		}
		*out = append(*out, id)
	}
	if _, err := dec.Token(); err != nil {
		return wrapDecode(err)
	}
	return nil
}

// countConfigUnits reports what a decoded config slice cost the budget:
// one unit per entry plus one per segment ID.
func countConfigUnits(cfgs []wireConfig) int {
	units := len(cfgs)
	for _, c := range cfgs {
		units += c.TargetSegments.count()
	}
	return units
}

// count reports how many segment IDs a rule holds. The nil receiver is the
// "no rule" case.
func (w *wireSegmentRule) count() int {
	if w == nil {
		return 0
	}
	return len(w.AllOf) + len(w.AnyOf) + len(w.NoneOf)
}

// decodeTimestamp reads lastUpdatedAt from a bounded string.
//
// Decoding straight into a time.Time would hand *time.ParseError the whole
// offending value — twice, by its format — and that error is logged. A peer
// that answers with a 30 MiB "timestamp" would then write a 60 MiB line
// into the operator's log pipeline every refresh. Read it as a string,
// refuse anything no timestamp could be, and never wrap the parse error.
func decodeTimestamp(dec *json.Decoder) (time.Time, error) {
	const maxTimestampLen = 64
	raw, err := decodeString(dec)
	if err != nil {
		return time.Time{}, err
	}
	if len(raw) > maxTimestampLen {
		return time.Time{}, errors.New("scope3: decode response: lastUpdatedAt is too long to be a timestamp")
	}
	ts, parseErr := time.Parse(time.RFC3339, raw)
	if parseErr != nil {
		return time.Time{}, errors.New("scope3: decode response: lastUpdatedAt is not an RFC 3339 timestamp")
	}
	return ts, nil
}

// decodeString reads one JSON string token.
func decodeString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", wrapDecode(err)
	}
	if tok == nil {
		// encoding/json leaves a string field untouched on null.
		return "", nil
	}
	v, ok := tok.(string)
	if !ok {
		return "", errors.New("scope3: decode response: expected a JSON string")
	}
	return v, nil
}

// expectDelim consumes one token and requires it to be want.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != want {
		return fmt.Errorf("expected %q", want)
	}
	return nil
}

// objectKey consumes one object key.
func objectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	// Defensive: in key position Token yields a string or an error.
	key, ok := tok.(string)
	if !ok {
		return "", errors.New("malformed object key")
	}
	return key, nil
}

// skipValue walks one JSON value without retaining it. encoding/json's
// scanner caps nesting at 10k, so the token stack cannot be grown without
// bound by a wall of opening brackets.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

// wrapDecode labels a decoder error without echoing anything the peer sent:
// encoding/json's syntax and type errors carry offsets and Go type names,
// not response bytes.
func wrapDecode(err error) error {
	return fmt.Errorf("scope3: decode response: %w", err)
}

// wrapDecodeOrNil is wrapDecode for a call whose error may be nil.
func wrapDecodeOrNil(err error) error {
	if err == nil {
		return nil
	}
	return wrapDecode(err)
}

func toEntries(cfgs []wireConfig) []identityconfig.Entry {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]identityconfig.Entry, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, identityconfig.Entry{
			Key:            identityconfig.Key{SellerAgentURL: c.SellerAgentURL, PackageID: c.PackageID},
			TargetSegments: c.TargetSegments.toDomain(),
		})
	}
	return out
}

func toKeys(items []wireRemovedEntry) []identityconfig.Key {
	if len(items) == 0 {
		return nil
	}
	out := make([]identityconfig.Key, 0, len(items))
	for _, it := range items {
		out = append(out, identityconfig.Key{SellerAgentURL: it.SellerAgentURL, PackageID: it.PackageID})
	}
	return out
}
