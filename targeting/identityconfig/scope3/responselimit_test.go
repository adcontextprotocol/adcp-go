package scope3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotJSON builds a well-formed config snapshot whose encoded size grows
// with n, so a test can push a response past a cap with a single JSON value
// the decoder has to read all the way through.
func snapshotJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"sellerAgentUrl":"https://seller.example.com/agent","packageId":"pkg-%06d","targetSegments":{"anyOf":["aud-%06d"]}}`, i, i)
	}
	b.WriteString(`],"removedTargetingConfigs":[]}`)
	return b.String()
}

func TestNew_DefaultsToDefaultMaxResponseBytes(t *testing.T) {
	src, err := New("https://example", "tok")
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxResponseBytes, src.maxResponseBytes)

	// Zero keeps the default, so an unset config value stays protected.
	src, err = New("https://example", "tok", WithMaxResponseBytes(0))
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxResponseBytes, src.maxResponseBytes)

	// Any negative value normalizes to the documented opt-out sentinel.
	src, err = New("https://example", "tok", WithMaxResponseBytes(-99))
	require.NoError(t, err)
	assert.Equal(t, UnlimitedResponseBytes, src.maxResponseBytes)
}

func TestLoadAll_RejectsOversizedBody(t *testing.T) {
	payload := snapshotJSON(200)

	// Chunked: no Content-Length, so only the streaming counter can catch
	// this. Flushing before the write keeps the handler from buffering the
	// whole body and re-declaring a length.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	const limit = 1024
	require.Greater(t, len(payload), limit, "fixture must exceed the cap")

	src, err := New(srv.URL, "tok", WithMaxResponseBytes(limit))
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrResponseTooLarge)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.False(t, tooLarge.Declared, "a chunked response is caught on the read, not up front")
	assert.Equal(t, int64(limit), tooLarge.Limit)
	assert.Equal(t, int64(limit+1), tooLarge.Read, "at most one byte past the cap is ever pulled")
}

func TestLoadAll_RejectsOversizedDeclaredContentLength(t *testing.T) {
	payload := snapshotJSON(200)
	var bodyWritten atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		bodyWritten.Store(true)
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	const limit = 1024
	src, err := New(srv.URL, "tok", WithMaxResponseBytes(limit))
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.ErrorIs(t, err, ErrResponseTooLarge)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.True(t, tooLarge.Declared, "an oversized Content-Length is rejected before the body is read")
	assert.Equal(t, int64(limit), tooLarge.Limit)
	assert.Equal(t, int64(len(payload)), tooLarge.ContentLength)
	assert.Zero(t, tooLarge.Read)
}

// An oversized response must fail, never decode to a truncated snapshot:
// a short snapshot applied over good config silently drops live packages.
func TestLoadAll_DoesNotTruncate(t *testing.T) {
	payload := snapshotJSON(50)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok", WithMaxResponseBytes(int64(len(payload)-1)))
	require.NoError(t, err)

	snap, err := src.LoadAll(context.Background())
	require.ErrorIs(t, err, ErrResponseTooLarge)
	assert.Empty(t, snap.Configs, "a rejected response must yield no partial snapshot")
}

func TestLoadAll_AcceptsBodyAtTheCap(t *testing.T) {
	payload := snapshotJSON(3)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	// Exactly at the cap is allowed; the cap is the largest accepted size.
	src, err := New(srv.URL, "tok", WithMaxResponseBytes(int64(len(payload))))
	require.NoError(t, err)

	snap, err := src.LoadAll(context.Background())
	require.NoError(t, err)
	assert.Len(t, snap.Configs, 3)
}

func TestLoadAll_UnlimitedOptsOut(t *testing.T) {
	payload := snapshotJSON(200)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok", WithMaxResponseBytes(UnlimitedResponseBytes))
	require.NoError(t, err)

	snap, err := src.LoadAll(context.Background())
	require.NoError(t, err)
	assert.Len(t, snap.Configs, 200)
}

func TestLoadUpdatedAfter_AppliesTheCap(t *testing.T) {
	payload := snapshotJSON(200)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok", WithMaxResponseBytes(1024))
	require.NoError(t, err)

	_, err = src.LoadUpdatedAfter(context.Background(), time.Unix(0, 0).UTC())
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

// endlessReader stands in for a peer that never stops sending. It fails
// after failAfter bytes so an unbounded drain terminates and reports a
// failed assertion instead of hanging the suite.
type endlessReader struct {
	failAfter int64
	read      int64
	closed    bool
}

func (r *endlessReader) Read(p []byte) (int, error) {
	if r.read >= r.failAfter {
		return 0, errors.New("endlessReader: drain was not bounded")
	}
	for i := range p {
		p[i] = 'x'
	}
	r.read += int64(len(p))
	return len(p), nil
}

func (r *endlessReader) Close() error {
	r.closed = true
	return nil
}

func TestDrainAndClose_IsBounded(t *testing.T) {
	body := &endlessReader{failAfter: 64 << 20}

	require.NoError(t, drainAndClose(body, false))
	assert.True(t, body.closed, "the body must be closed even when the drain stops early")
	// io.CopyN uses a 32 KiB buffer, so the read can overshoot the bound by
	// at most one buffer's worth.
	assert.LessOrEqual(t, body.read, maxDrainBytes+32<<10,
		"the drain must stop near maxDrainBytes rather than reading an endless body")
}

func TestDrainAndClose_SkipReadsNothing(t *testing.T) {
	body := &endlessReader{failAfter: 64 << 20}

	require.NoError(t, drainAndClose(body, true))
	assert.True(t, body.closed)
	assert.Zero(t, body.read, "a disqualified response must not be drained at all")
}

// A peer that keeps sending after a complete JSON value must not hold the
// call open: json.Decoder stops at the end of the value, and the deferred
// drain is bounded, so LoadAll returns rather than following the stream.
func TestLoadAll_ReturnsWhilePeerKeepsSending(t *testing.T) {
	stop := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":[],"removedTargetingConfigs":[]}`)
		w.(http.Flusher).Flush()
		pad := strings.Repeat("x", 32<<10)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := io.WriteString(w, pad); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	// Registered last so it runs first: srv.Close waits for the handler,
	// which only returns once stop is closed.
	defer close(stop)

	src, err := New(srv.URL, "tok", WithHTTPTimeout(30*time.Second))
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, loadErr := src.LoadAll(context.Background())
		done <- loadErr
	}()

	select {
	case loadErr := <-done:
		assert.NoError(t, loadErr)
	case <-time.After(10 * time.Second):
		t.Fatal("LoadAll did not return: the response drain is unbounded")
	}
}

func TestResponseTooLargeError_Message(t *testing.T) {
	declared := &ResponseTooLargeError{Limit: 1024, ContentLength: 4096, Declared: true}
	assert.Contains(t, declared.Error(), "declared Content-Length 4096")
	assert.Contains(t, declared.Error(), "1024-byte limit")

	streamed := &ResponseTooLargeError{Limit: 1024, Read: 1025, ContentLength: -1}
	assert.Contains(t, streamed.Error(), "exceeded the 1024-byte limit after 1025 bytes")

	assert.ErrorIs(t, streamed, ErrResponseTooLarge)
	assert.NotErrorIs(t, streamed, errors.New("other"))
}

// minimalEntriesJSON is what a hostile endpoint sends: entries that cost
// three bytes on the wire and a full identityconfig.Entry (plus its slots in
// the service's byKey and bySeller indexes) once decoded.
func minimalEntriesJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{}`)
	}
	b.WriteString(`],"removedTargetingConfigs":[]}`)
	return b.String()
}

func TestNew_DefaultsToDefaultMaxConfigEntries(t *testing.T) {
	src, err := New("https://example", "tok")
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxConfigEntries, src.maxConfigEntries)

	src, err = New("https://example", "tok", WithMaxConfigEntries(0))
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxConfigEntries, src.maxConfigEntries)

	src, err = New("https://example", "tok", WithMaxConfigEntries(-7))
	require.NoError(t, err)
	assert.Equal(t, UnlimitedConfigEntries, src.maxConfigEntries)
}

// The byte cap bounds the wire; this one bounds the heap. A response of
// minimal entries sails under any byte cap and still decodes to a snapshot
// tens of times its wire size.
func TestLoadAll_RejectsTooManyEntries(t *testing.T) {
	payload := minimalEntriesJSON(500)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	// Generous byte cap: only the entry cap can catch this.
	src, err := New(srv.URL, "tok",
		WithMaxResponseBytes(DefaultMaxResponseBytes),
		WithMaxConfigEntries(100),
	)
	require.NoError(t, err)

	snap, err := src.LoadAll(context.Background())
	require.ErrorIs(t, err, ErrTooManyEntries)

	var tooMany *TooManyEntriesError
	require.ErrorAs(t, err, &tooMany)
	assert.Equal(t, 100, tooMany.Limit)
	assert.Empty(t, snap.Configs, "a rejected response must yield no partial snapshot")
}

// The two arrays share one budget: a peer must not get 2x the cap by
// splitting its entries across targetingConfigs and removedTargetingConfigs.
func TestLoadUpdatedAfter_EntryCapCoversBothArrays(t *testing.T) {
	body := `{"lastUpdatedAt":"2026-05-13T10:42:43Z",` +
		`"targetingConfigs":[{"sellerAgentUrl":"https://s/a","packageId":"p1"},{"sellerAgentUrl":"https://s/a","packageId":"p2"}],` +
		`"removedTargetingConfigs":[{"sellerAgentUrl":"https://s/a","packageId":"p3"},{"sellerAgentUrl":"https://s/a","packageId":"p4"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	// Four entries total. Three is over budget; four is exactly at it.
	src, err := New(srv.URL, "tok", WithMaxConfigEntries(3))
	require.NoError(t, err)
	_, err = src.LoadUpdatedAfter(context.Background(), time.Unix(0, 0).UTC())
	assert.ErrorIs(t, err, ErrTooManyEntries)

	src, err = New(srv.URL, "tok", WithMaxConfigEntries(4))
	require.NoError(t, err)
	delta, err := src.LoadUpdatedAfter(context.Background(), time.Unix(0, 0).UTC())
	require.NoError(t, err)
	assert.Len(t, delta.Upserted, 2)
	assert.Len(t, delta.Removed, 2)
}

func TestLoadAll_UnlimitedConfigEntriesOptsOut(t *testing.T) {
	payload := minimalEntriesJSON(500)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok", WithMaxConfigEntries(UnlimitedConfigEntries))
	require.NoError(t, err)

	snap, err := src.LoadAll(context.Background())
	require.NoError(t, err)
	assert.Len(t, snap.Configs, 500)
}

// The streaming decoder has to keep the lenience of the whole-document
// decode it replaced, and stay strict where that decode was strict.
func TestDecodeResponse_WireCompatibility(t *testing.T) {
	t.Run("unknown fields are tolerated without being buffered", func(t *testing.T) {
		body := `{"futureField":{"deep":[1,2,{"x":null}]},"lastUpdatedAt":"2026-05-13T10:42:43Z",` +
			`"targetingConfigs":[{"sellerAgentUrl":"https://s/a","packageId":"p1"}],"extra":"ignored"}`
		out, err := decodeResponse(strings.NewReader(body), DefaultMaxConfigEntries)
		require.NoError(t, err)
		require.Len(t, out.TargetingConfigs, 1)
		assert.Equal(t, "p1", out.TargetingConfigs[0].PackageID)
	})

	t.Run("null arrays decode as empty", func(t *testing.T) {
		out, err := decodeResponse(strings.NewReader(
			`{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":null,"removedTargetingConfigs":null}`),
			DefaultMaxConfigEntries)
		require.NoError(t, err)
		assert.Empty(t, out.TargetingConfigs)
		assert.Empty(t, out.RemovedTargetingConfigs)
	})

	t.Run("an empty object decodes to an empty snapshot", func(t *testing.T) {
		out, err := decodeResponse(strings.NewReader(`{}`), DefaultMaxConfigEntries)
		require.NoError(t, err)
		assert.Empty(t, out.TargetingConfigs)
	})

	t.Run("a repeated key replaces rather than appends", func(t *testing.T) {
		body := `{"targetingConfigs":[{"packageId":"first"}],"targetingConfigs":[{"packageId":"second"}]}`
		out, err := decodeResponse(strings.NewReader(body), DefaultMaxConfigEntries)
		require.NoError(t, err)
		require.Len(t, out.TargetingConfigs, 1)
		assert.Equal(t, "second", out.TargetingConfigs[0].PackageID)
	})

	t.Run("segment rules survive the element-wise decode", func(t *testing.T) {
		body := `{"targetingConfigs":[{"sellerAgentUrl":"https://s/a","packageId":"p1",` +
			`"targetSegments":{"allOf":["a"],"anyOf":["b","c"],"noneOf":["d"]}}]}`
		out, err := decodeResponse(strings.NewReader(body), DefaultMaxConfigEntries)
		require.NoError(t, err)
		rule := out.TargetingConfigs[0].TargetSegments
		require.NotNil(t, rule)
		assert.Equal(t, []string{"a"}, rule.AllOf)
		assert.Equal(t, []string{"b", "c"}, rule.AnyOf)
		assert.Equal(t, []string{"d"}, rule.NoneOf)
	})

	t.Run("a truncated document is an error, not a short snapshot", func(t *testing.T) {
		body := `{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":[{"packageId":"p1"},`
		_, err := decodeResponse(strings.NewReader(body), DefaultMaxConfigEntries)
		assert.Error(t, err)
	})

	t.Run("a non-object body is an error", func(t *testing.T) {
		for _, body := range []string{`[]`, `"nope"`, `null`, `7`} {
			_, err := decodeResponse(strings.NewReader(body), DefaultMaxConfigEntries)
			assert.Error(t, err, "body %q", body)
		}
	})

	t.Run("a non-array config field is an error", func(t *testing.T) {
		_, err := decodeResponse(strings.NewReader(`{"targetingConfigs":{"packageId":"p1"}}`), DefaultMaxConfigEntries)
		assert.Error(t, err)
	})
}

// With Accept-Encoding reserved, net/http negotiates gzip and inflates it
// itself. Anything still encoded on arrival was never asked for, and
// nothing downstream will inflate it.
func TestLoadAll_RejectsUnsolicitedContentEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "zstd")
		_, _ = io.WriteString(w, "\x28\xb5\x2f\xfd not really zstd")
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok")
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.ErrorIs(t, err, ErrUnsupportedContentEncoding)

	var unsupported *UnsupportedContentEncodingError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "zstd", unsupported.Encoding)
}

func TestUnsupportedContentCoding_SanitizesThePeersToken(t *testing.T) {
	assert.Empty(t, unsupportedContentCoding(""))
	assert.Empty(t, unsupportedContentCoding("identity"))
	assert.Empty(t, unsupportedContentCoding("  IDENTITY "))
	assert.Equal(t, "br", unsupportedContentCoding("br"))
	// A peer must not be able to write arbitrary bytes into an operator's
	// logs through the error string.
	assert.Equal(t, "unrecognized", unsupportedContentCoding("gzip\n\nINJECTED LOG LINE"))
	assert.Len(t, unsupportedContentCoding(strings.Repeat("a", 200)), 32)
}

// Accept-Encoding is reserved: setting it would suppress net/http's
// transparent gzip and leave the byte cap counting wire bytes.
func TestWithExtraHeaders_RejectsAcceptEncoding(t *testing.T) {
	_, err := New("https://example", "tok", WithExtraHeaders(map[string]string{"accept-encoding": "gzip"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Accept-Encoding")
}

// http.Client reads a non-positive timeout as "no timeout", and that
// timeout is the only wall-clock bound on a trickling peer.
func TestWithHTTPTimeout_RejectsNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		_, err := New("https://example", "tok", WithHTTPTimeout(d))
		assert.Error(t, err, "duration %s", d)
	}
}

// Response headers are read before any body cap applies; net/http's default
// ceiling is 10 MiB, which this endpoint has no business spending.
func TestLoadAll_RejectsOversizedResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pad := strings.Repeat("p", 4096)
		for i := range 64 {
			w.Header().Set(fmt.Sprintf("X-Pad-%04d", i), pad)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":[]}`)
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok")
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.Error(t, err)
	// net/http's own message, which names the ceiling this Source set —
	// the 10 MiB default would have accepted these headers.
	assert.Contains(t, err.Error(), "server response headers exceeded 65536 bytes")
}

func TestTooManyEntriesError_Message(t *testing.T) {
	err := &TooManyEntriesError{Limit: 250000}
	assert.Contains(t, err.Error(), "250000-entry limit")
	assert.ErrorIs(t, err, ErrTooManyEntries)
	assert.NotErrorIs(t, err, ErrResponseTooLarge)
}

// An entry counter alone is the wrong unit: a rule's segment lists are
// unbounded, so one entry can carry the whole byte budget as segment IDs.
func TestLoadAll_ChargesSegmentIDsAgainstTheEntryBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"lastUpdatedAt":"2026-05-13T10:42:43Z","targetingConfigs":[{"sellerAgentUrl":"https://s/a","packageId":"p1","targetSegments":{"anyOf":[`)
	for i := range 500 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"aud-%d"`, i)
	}
	b.WriteString(`]}}],"removedTargetingConfigs":[]}`)
	payload := b.String()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	// One entry — an entries-only counter would never trip. The 500
	// segment IDs are what has to be charged.
	src, err := New(srv.URL, "tok", WithMaxConfigEntries(100))
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.ErrorIs(t, err, ErrTooManyEntries)

	// Raised past the entry plus its segments, the same response is fine.
	src, err = New(srv.URL, "tok", WithMaxConfigEntries(501))
	require.NoError(t, err)
	snap, err := src.LoadAll(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Configs, 1)
	assert.Len(t, snap.Configs[0].TargetSegments.AnyOf, 500)
}

func TestDecodeResponse_BudgetAccounting(t *testing.T) {
	t.Run("entries and segment IDs share one budget", func(t *testing.T) {
		// 2 entries + 3 segment IDs = 5 units.
		body := `{"targetingConfigs":[` +
			`{"packageId":"p1","targetSegments":{"allOf":["a"],"anyOf":["b","c"]}},` +
			`{"packageId":"p2"}]}`
		_, err := decodeResponse(strings.NewReader(body), 4)
		assert.ErrorIs(t, err, ErrTooManyEntries)

		out, err := decodeResponse(strings.NewReader(body), 5)
		require.NoError(t, err)
		assert.Len(t, out.TargetingConfigs, 2)
	})

	t.Run("a replaced duplicate key refunds what it cost", func(t *testing.T) {
		// Without the refund the first array's 3 units would still be
		// charged, and a budget of 3 would trip on the replacement.
		body := `{"targetingConfigs":[{"packageId":"p1","targetSegments":{"anyOf":["a","b"]}}],` +
			`"targetingConfigs":[{"packageId":"p2","targetSegments":{"anyOf":["c","d"]}}]}`
		out, err := decodeResponse(strings.NewReader(body), 3)
		require.NoError(t, err)
		require.Len(t, out.TargetingConfigs, 1)
		assert.Equal(t, "p2", out.TargetingConfigs[0].PackageID)
	})

	t.Run("a replaced segment list refunds what it cost", func(t *testing.T) {
		// 1 entry + 2 IDs fills a budget of 3; without the refund the
		// replacement's single ID would push it over.
		body := `{"targetingConfigs":[{"packageId":"p1","targetSegments":{"anyOf":["a","b"]},"targetSegments":{"anyOf":["c"]}}]}`
		out, err := decodeResponse(strings.NewReader(body), 3)
		require.NoError(t, err)
		assert.Equal(t, []string{"c"}, out.TargetingConfigs[0].TargetSegments.AnyOf)
	})
}

// encoding/json matches an object key against a struct tag
// case-insensitively. Matching exactly would turn a casing change at the
// endpoint into a successful empty snapshot applied over good config.
func TestDecodeResponse_MatchesKeysCaseInsensitively(t *testing.T) {
	bodies := []string{
		`{"LastUpdatedAt":"2026-05-13T10:42:43Z","TargetingConfigs":[{"SellerAgentUrl":"https://s/a","PackageId":"p1","TargetSegments":{"AnyOf":["aud"]}}]}`,
		`{"lastupdatedat":"2026-05-13T10:42:43Z","targetingconfigs":[{"selleragenturl":"https://s/a","packageid":"p1","targetsegments":{"anyof":["aud"]}}]}`,
	}
	for _, body := range bodies {
		out, err := decodeResponse(strings.NewReader(body), DefaultMaxConfigEntries)
		require.NoError(t, err)
		require.Len(t, out.TargetingConfigs, 1, "body %q decoded to an empty snapshot", body)
		assert.Equal(t, "p1", out.TargetingConfigs[0].PackageID)
		assert.Equal(t, "https://s/a", out.TargetingConfigs[0].SellerAgentURL)
		require.NotNil(t, out.TargetingConfigs[0].TargetSegments)
		assert.Equal(t, []string{"aud"}, out.TargetingConfigs[0].TargetSegments.AnyOf)
		assert.False(t, out.LastUpdatedAt.IsZero())
	}
}

func TestDecodeResponse_RemovedEntriesAndTrailingContent(t *testing.T) {
	t.Run("removed entries decode", func(t *testing.T) {
		out, err := decodeResponse(strings.NewReader(
			`{"removedTargetingConfigs":[{"sellerAgentUrl":"https://s/a","packageId":"p1"},{"sellerAgentUrl":"https://s/b","packageId":"p2","unknown":{"x":[1]}}]}`),
			DefaultMaxConfigEntries)
		require.NoError(t, err)
		require.Len(t, out.RemovedTargetingConfigs, 2)
		assert.Equal(t, "p2", out.RemovedTargetingConfigs[1].PackageID)
	})

	t.Run("content after the closing brace is ignored, as it was before", func(t *testing.T) {
		out, err := decodeResponse(strings.NewReader(
			`{"targetingConfigs":[{"packageId":"p1"}]} trailing garbage`), DefaultMaxConfigEntries)
		require.NoError(t, err)
		assert.Len(t, out.TargetingConfigs, 1)
	})

	t.Run("a null segment rule stays nil", func(t *testing.T) {
		out, err := decodeResponse(strings.NewReader(
			`{"targetingConfigs":[{"packageId":"p1","targetSegments":null}]}`), DefaultMaxConfigEntries)
		require.NoError(t, err)
		assert.Nil(t, out.TargetingConfigs[0].TargetSegments)
	})
}

// time.ParseError embeds the offending value twice, and that error is
// logged. A peer must not be able to write a multi-megabyte line into an
// operator's log pipeline on every refresh.
func TestDecodeResponse_LastUpdatedAtErrorsDoNotEchoThePeer(t *testing.T) {
	huge := strings.Repeat("A", 1<<20)
	_, err := decodeResponse(strings.NewReader(`{"lastUpdatedAt":"`+huge+`"}`), DefaultMaxConfigEntries)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "AAAA")
	assert.Less(t, len(err.Error()), 200)

	_, err = decodeResponse(strings.NewReader(`{"lastUpdatedAt":"not-a-timestamp"}`), DefaultMaxConfigEntries)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not-a-timestamp")

	out, err := decodeResponse(strings.NewReader(`{"lastUpdatedAt":"2026-05-13T10:42:43.123456789Z"}`), DefaultMaxConfigEntries)
	require.NoError(t, err)
	assert.Equal(t, 2026, out.LastUpdatedAt.Year())
}

// The error body of a non-2xx response is worth logging, but not verbatim:
// a peer that can write newlines into an operator's log can forge records.
func TestLoadAll_SanitizesTheErrorBodySnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "token expired\n\nFAKE LOG LINE\x1b[31m")
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok")
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token expired", "the snippet is still useful")
	assert.NotContains(t, err.Error(), "\n")
	assert.NotContains(t, err.Error(), "\x1b")
}

func TestSanitizeSnippet(t *testing.T) {
	assert.Equal(t, "ok", sanitizeSnippet([]byte("ok")))
	assert.Equal(t, "a..b", sanitizeSnippet([]byte("a\r\nb")))
	// Non-ASCII bytes are replaced one for one, not decoded.
	assert.Equal(t, "h..", sanitizeSnippet([]byte("héy")[:3]))
	assert.Len(t, sanitizeSnippet([]byte(strings.Repeat("x", 512))), 256)
}

// WithHTTPClient hands the timeout decision to the caller, so pairing it
// with WithHTTPTimeout is accepted in either order and simply ignored.
func TestWithHTTPTimeout_IgnoredWithCustomClientInEitherOrder(t *testing.T) {
	custom := &http.Client{Timeout: 7 * time.Second}

	src, err := New("https://example", "tok", WithHTTPClient(custom), WithHTTPTimeout(0))
	require.NoError(t, err)
	assert.Equal(t, 7*time.Second, custom.Timeout)
	assert.Same(t, custom, src.client)

	src, err = New("https://example", "tok", WithHTTPTimeout(0), WithHTTPClient(custom))
	require.NoError(t, err)
	assert.Equal(t, 7*time.Second, custom.Timeout)
	assert.Same(t, custom, src.client)
}
