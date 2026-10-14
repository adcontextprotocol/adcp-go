package signing

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBody records how many bytes a reader actually pulled, so the tests
// can assert the cap stops the transfer instead of letting the whole body
// land in memory first.
type countingBody struct {
	r      io.Reader
	read   int64
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read += int64(n)
	return n, err
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

// stubTransport returns a canned response, bypassing the network so the
// tests can control Content-Length and Content-Type exactly.
type stubTransport struct {
	body          *countingBody
	contentLength int64
	contentType   string
}

func (t *stubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	header := http.Header{}
	if t.contentType != "" {
		header.Set("Content-Type", t.contentType)
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          t.body,
		ContentLength: t.contentLength,
	}, nil
}

func newStub(payload []byte, contentLength int64, contentType string) *stubTransport {
	return &stubTransport{
		body:          &countingBody{r: bytes.NewReader(payload)},
		contentLength: contentLength,
		contentType:   contentType,
	}
}

func doGET(t *testing.T, rt http.RoundTripper, ctx context.Context) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://seller.example.com/adcp/get_products", nil)
	require.NoError(t, err)
	return rt.RoundTrip(req)
}

// jsonPayload builds a JSON body of at least n bytes — the shape of the
// oversized get_products response that motivated the cap.
func jsonPayload(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"products":["`)
	for buf.Len() < n {
		buf.WriteString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	buf.WriteString(`"]}`)
	return buf.Bytes()
}

func TestResponseLimitTransportRejectsOversizedBodyWithoutContentLength(t *testing.T) {
	payload := jsonPayload(1024)
	stub := newStub(payload, -1, "application/json")
	rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 512})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, ScopeResponseBody, tooLarge.Scope)
	assert.Equal(t, int64(512), tooLarge.Limit)
	assert.False(t, tooLarge.Declared)

	// The cap is enforced while reading: at most one byte past the limit is
	// ever pulled off the wire.
	assert.LessOrEqual(t, stub.body.read, int64(513),
		"body kept streaming past the cap instead of failing on the crossing read")
}

func TestResponseLimitTransportPreRejectsDeclaredContentLength(t *testing.T) {
	payload := jsonPayload(4096)
	stub := newStub(payload, int64(len(payload)), "application/json")
	rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 512})

	resp, err := doGET(t, rt, context.Background())
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	require.Error(t, err)
	assert.Nil(t, resp, "a pre-rejected response must not reach the caller")
	assert.ErrorIs(t, err, ErrResponseTooLarge)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.True(t, tooLarge.Declared)
	assert.Equal(t, int64(len(payload)), tooLarge.ContentLength)
	assert.Equal(t, int64(0), tooLarge.Read)

	assert.Equal(t, int64(0), stub.body.read, "pre-reject must not read the body")
	assert.True(t, stub.body.closed, "pre-reject must close the body")
}

func TestResponseLimitTransportAllowsUnderCapResponse(t *testing.T) {
	payload := jsonPayload(256)
	stub := newStub(payload, int64(len(payload)), "application/json")
	rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 4096})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestResponseLimitTransportAllowsBodyExactlyAtCap(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1024)
	stub := newStub(payload, -1, "application/json")
	rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 1024})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Len(t, got, 1024)
}

func TestResponseLimitTransportDefaultsToFourMiB(t *testing.T) {
	// One byte over the documented default must fail with the zero-value
	// ResponseLimits, i.e. with nobody opting in.
	payload := bytes.Repeat([]byte("x"), int(DefaultMaxResponseBytes)+1)
	stub := newStub(payload, -1, "application/json")
	rt := NewResponseLimitTransport(stub, ResponseLimits{})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, DefaultMaxResponseBytes, tooLarge.Limit)
	assert.Equal(t, int64(4*1024*1024), DefaultMaxResponseBytes)
}

func TestResponseLimitTransportRejectsOversizedSSEEvent(t *testing.T) {
	// A single event larger than the per-event cap, inside a stream whose
	// cumulative size is still under the body cap.
	var sse bytes.Buffer
	sse.WriteString("event: message\ndata: " + strings.Repeat("y", 4096) + "\n\n")
	stub := newStub(sse.Bytes(), -1, "text/event-stream")
	rt := NewResponseLimitTransport(stub, ResponseLimits{
		MaxResponseBytes: 1 << 20,
		MaxSSEEventBytes: 1024,
	})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, ScopeSSEEvent, tooLarge.Scope)
	assert.Equal(t, int64(1024), tooLarge.Limit)
}

func TestResponseLimitTransportRejectsCumulativeSSEOverCap(t *testing.T) {
	// Every event is small, but the stream as a whole blows the per-call
	// cumulative cap — the "answer with text/event-stream to escape the
	// limit" evasion.
	var sse bytes.Buffer
	for i := 0; sse.Len() < 8192; i++ {
		fmt.Fprintf(&sse, "data: %s\n\n", strings.Repeat("z", 100))
	}
	stub := newStub(sse.Bytes(), -1, "text/event-stream")
	rt := NewResponseLimitTransport(stub, ResponseLimits{
		MaxResponseBytes: 2048,
		MaxSSEEventBytes: 512,
	})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, ScopeResponseBody, tooLarge.Scope)
	assert.Equal(t, int64(2048), tooLarge.Limit)
	assert.LessOrEqual(t, stub.body.read, int64(2049))
}

func TestResponseLimitTransportAllowsManySmallSSEEvents(t *testing.T) {
	var sse bytes.Buffer
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sse, "event: message\r\ndata: chunk-%d\r\n\r\n", i)
	}
	stub := newStub(sse.Bytes(), -1, "text/event-stream; charset=utf-8")
	rt := NewResponseLimitTransport(stub, ResponseLimits{
		MaxResponseBytes: 1 << 20,
		MaxSSEEventBytes: 64,
	})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "CRLF-delimited events must reset the per-event counter")
	assert.Equal(t, sse.Bytes(), got)
}

func TestResponseLimitTransportSSEEventBoundariesSpanReads(t *testing.T) {
	// Feed the body one byte at a time so every event terminator straddles
	// a Read boundary; the counter state has to survive that.
	var sse bytes.Buffer
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sse, "data: chunk-%d\n\n", i)
	}
	stub := newStub(sse.Bytes(), -1, "text/event-stream")
	rt := NewResponseLimitTransport(stub, ResponseLimits{
		MaxResponseBytes: 1 << 20,
		MaxSSEEventBytes: 32,
	})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	one := make([]byte, 1)
	total := 0
	for {
		n, readErr := resp.Body.Read(one)
		total += n
		if readErr == io.EOF {
			break
		}
		require.NoError(t, readErr)
	}
	assert.Equal(t, sse.Len(), total)
}

func TestResponseLimitTransportSSEEventCapInheritsBodyCap(t *testing.T) {
	// MaxSSEEventBytes left at zero: the per-event cap is the body cap, so
	// one giant event still fails.
	sse := []byte("data: " + strings.Repeat("y", 4096) + "\n\n")
	stub := newStub(sse, -1, "text/event-stream")
	rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 1024})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	_, err = io.ReadAll(resp.Body)
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestResponseLimitTransportPerCallOverride(t *testing.T) {
	payload := jsonPayload(4096)

	t.Run("raises the cap for one call", func(t *testing.T) {
		stub := newStub(payload, int64(len(payload)), "application/json")
		rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 512})

		ctx := WithResponseLimits(context.Background(), ResponseLimits{MaxResponseBytes: 1 << 20})
		resp, err := doGET(t, rt, ctx)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("lowers the cap for one call", func(t *testing.T) {
		stub := newStub(payload, -1, "application/json")
		rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 1 << 20})

		ctx := WithResponseLimits(context.Background(), ResponseLimits{MaxResponseBytes: 256})
		resp, err := doGET(t, rt, ctx)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		_, err = io.ReadAll(resp.Body)
		var tooLarge *ResponseTooLargeError
		require.ErrorAs(t, err, &tooLarge)
		assert.Equal(t, int64(256), tooLarge.Limit)
	})

	t.Run("zero fields inherit the transport limits", func(t *testing.T) {
		stub := newStub(payload, -1, "text/event-stream")
		rt := NewResponseLimitTransport(stub, ResponseLimits{
			MaxResponseBytes: 1 << 20,
			MaxSSEEventBytes: 128,
		})

		// Override only the body cap; the per-event cap must survive.
		ctx := WithResponseLimits(context.Background(), ResponseLimits{MaxResponseBytes: 2 << 20})
		resp, err := doGET(t, rt, ctx)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		_, err = io.ReadAll(resp.Body)
		var tooLarge *ResponseTooLargeError
		require.ErrorAs(t, err, &tooLarge)
		assert.Equal(t, ScopeSSEEvent, tooLarge.Scope)
		assert.Equal(t, int64(128), tooLarge.Limit)
	})
}

func TestResponseLimitTransportExplicitOptOut(t *testing.T) {
	payload := jsonPayload(8192)

	t.Run("transport level", func(t *testing.T) {
		stub := newStub(payload, int64(len(payload)), "application/json")
		rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: UnlimitedResponseBytes})

		resp, err := doGET(t, rt, context.Background())
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("per call", func(t *testing.T) {
		stub := newStub(payload, int64(len(payload)), "application/json")
		rt := NewResponseLimitTransport(stub, ResponseLimits{MaxResponseBytes: 512})

		ctx := WithResponseLimits(context.Background(), ResponseLimits{
			MaxResponseBytes: UnlimitedResponseBytes,
		})
		resp, err := doGET(t, rt, ctx)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})
}

func TestResponseLimitTransportCountsDecompressedBytes(t *testing.T) {
	// A gzip bomb: a few KiB on the wire, far past the cap once inflated.
	const inflated = 8 << 20
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, err := io.Copy(zw, io.LimitReader(zeroReader{}, inflated))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, compressed.Len(), 1<<20, "test fixture should be small on the wire")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http only decompresses transparently when it added the
		// Accept-Encoding header itself, which it does here.
		require.Contains(t, r.Header.Get("Accept-Encoding"), "gzip")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: NewResponseLimitTransport(srv.Client().Transport, ResponseLimits{}),
	}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.True(t, resp.Uncompressed, "transport should have decompressed the body")

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, DefaultMaxResponseBytes, tooLarge.Limit)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestResponseLimitTransportClosePropagates(t *testing.T) {
	stub := newStub(jsonPayload(64), -1, "application/json")
	rt := NewResponseLimitTransport(stub, ResponseLimits{})

	resp, err := doGET(t, rt, context.Background())
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.True(t, stub.body.closed)
}

func TestResponseLimitsResolve(t *testing.T) {
	tests := []struct {
		name          string
		in            ResponseLimits
		wantBody      int64
		wantSSEEvents int64
	}{
		{"zero value defaults", ResponseLimits{}, DefaultMaxResponseBytes, DefaultMaxResponseBytes},
		{"event inherits body", ResponseLimits{MaxResponseBytes: 1024}, 1024, 1024},
		{"explicit event cap", ResponseLimits{MaxResponseBytes: 1024, MaxSSEEventBytes: 64}, 1024, 64},
		{
			"unlimited body keeps event cap",
			ResponseLimits{MaxResponseBytes: UnlimitedResponseBytes, MaxSSEEventBytes: 64},
			UnlimitedResponseBytes, 64,
		},
		{
			"unlimited event keeps body cap",
			ResponseLimits{MaxResponseBytes: 1024, MaxSSEEventBytes: UnlimitedResponseBytes},
			1024, UnlimitedResponseBytes,
		},
		{
			"negative normalizes to the sentinel",
			ResponseLimits{MaxResponseBytes: -42},
			UnlimitedResponseBytes, UnlimitedResponseBytes,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.resolve()
			assert.Equal(t, tc.wantBody, got.MaxResponseBytes)
			assert.Equal(t, tc.wantSSEEvents, got.MaxSSEEventBytes)
		})
	}
}

func TestIsEventStream(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"application/json", false},
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"TEXT/EVENT-STREAM", true},
		{"text/event-stream;;", true}, // unparseable params, prefix fallback
		{"text/plain", false},
	} {
		assert.Equal(t, tc.want, isEventStream(tc.in), "Content-Type %q", tc.in)
	}
}

// TestNewSignedHTTPClientCapsResponsesByDefault is the end-to-end check: a
// caller who only asks for a signed client still gets the cap.
func TestNewSignedHTTPClientCapsResponsesByDefault(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), int(DefaultMaxResponseBytes)+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:      "test-kid",
		PrivateKey: priv,
		Inner:      srv.Client().Transport,
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	err = getAndDrain(t, client, srv.URL+"/adcp/get_products")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)

	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, DefaultMaxResponseBytes, tooLarge.Limit)
}

func TestNewSignedHTTPClientRespectsConfiguredLimit(t *testing.T) {
	payload := jsonPayload(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:            "test-kid",
		PrivateKey:       priv,
		Inner:            srv.Client().Transport,
		MaxResponseBytes: 1024,
	})
	require.NoError(t, err)

	err = getAndDrain(t, client, srv.URL+"/adcp/get_products")
	require.Error(t, err)
	var tooLarge *ResponseTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	assert.Equal(t, int64(1024), tooLarge.Limit)

	// And the same client honors a per-call override.
	ctx := WithResponseLimits(context.Background(), ResponseLimits{MaxResponseBytes: 1 << 20})
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/adcp/get_products", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer closeResponseBody(t, resp)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestNewSignedHTTPClientUnsignedPathIsAlsoCapped(t *testing.T) {
	// Capability-aware mode with a provider that declines to sign: the
	// response still has to be capped.
	payload := jsonPayload(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Signature"), "request should not have been signed")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "test-kid",
		PrivateKey:         priv,
		Inner:              srv.Client().Transport,
		MaxResponseBytes:   1024,
		CapabilityProvider: func(*http.Request) *adcp.RequestSigningCapabilities { return nil },
	})
	require.NoError(t, err)

	err = getAndDrain(t, client, srv.URL+"/adcp/get_products")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

// getAndDrain issues a GET and reads the body to completion, returning
// whichever error surfaced first. The cap can trip either in RoundTrip (the
// response declared an oversized Content-Length) or mid-read (chunked or
// decompressed), and a caller sees both the same way: the call fails.
func getAndDrain(t *testing.T, client *http.Client, url string) error {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer closeResponseBody(t, resp)
	_, err = io.ReadAll(resp.Body)
	return err
}
