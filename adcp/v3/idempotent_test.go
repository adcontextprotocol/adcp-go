package adcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRegisteredSession(t *testing.T, cfg Config) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0.0.1"}, nil)
	Register(server, cfg)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "seller-test-client", Version: "v0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callSession(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return structuredContentMap(t, result)
}

func adcpErrorOf(t *testing.T, wire map[string]any) map[string]any {
	t.Helper()
	e, ok := wire["adcp_error"].(map[string]any)
	require.Truef(t, ok, "expected adcp_error, got %v", wire)
	return e
}

func countingCreateMediaBuy(calls *int32) func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
	return func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
		n := atomic.AddInt32(calls, 1)
		return &CreateMediaBuySuccess{MediaBuyID: fmt.Sprintf("mb-%d", n), Packages: []Package{}}, nil
	}
}

func TestRegisterReplaysCreateMediaBuy(t *testing.T) {
	var calls int32
	cs := newRegisteredSession(t, baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)}))
	key := idempotency.Generate()

	first := callSession(t, cs, "create_media_buy", map[string]any{
		"idempotency_key": key, "context": map[string]any{"attempt": "1"},
	})
	second := callSession(t, cs, "create_media_buy", map[string]any{
		"idempotency_key": key, "context": map[string]any{"attempt": "2"},
	})

	assert.Equal(t, "mb-1", first["media_buy_id"])
	assert.Nil(t, first["replayed"])
	assert.Equal(t, "mb-1", second["media_buy_id"])
	assert.Equal(t, true, second["replayed"])
	assert.Equal(t, map[string]any{"attempt": "2"}, second["context"], "replay echoes the current request's context")
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestRegisterRejectsKeyReuseWithDifferentPayload(t *testing.T) {
	var calls int32
	cs := newRegisteredSession(t, baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)}))
	key := idempotency.Generate()

	callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key, "po_number": "A"})
	wire := callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key, "po_number": "B"})

	e := adcpErrorOf(t, wire)
	assert.Equal(t, "IDEMPOTENCY_CONFLICT", e["code"])
	assert.Equal(t, "correctable", e["recovery"])
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestRegisterRequiresIdempotencyKeyOnMutatingTools(t *testing.T) {
	var calls int32
	cs := newRegisteredSession(t, baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)}))

	e := adcpErrorOf(t, callSession(t, cs, "create_media_buy", map[string]any{}))
	assert.Equal(t, "INVALID_REQUEST", e["code"])
	assert.Equal(t, "idempotency_key", e["field"])
	assert.Equal(t, "correctable", e["recovery"])
	assert.Zero(t, atomic.LoadInt32(&calls))
}

func TestRegisterDoesNotCacheErrorResults(t *testing.T) {
	var calls int32
	cs := newRegisteredSession(t, baseTestConfig(Config{
		CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nil, NewError("SERVICE_UNAVAILABLE", ErrorOptions{Message: "ad server down"})
			}
			return &CreateMediaBuySuccess{MediaBuyID: "mb-ok", Packages: []Package{}}, nil
		},
	}))
	key := idempotency.Generate()

	first := callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key})
	assert.Equal(t, "SERVICE_UNAVAILABLE", adcpErrorOf(t, first)["code"])

	second := callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key})
	assert.Equal(t, "mb-ok", second["media_buy_id"])
	assert.EqualValues(t, 2, atomic.LoadInt32(&calls))
}

func TestRegisterReplayDropsCachedContext(t *testing.T) {
	var calls int32
	cs := newRegisteredSession(t, baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)}))
	key := idempotency.Generate()

	callSession(t, cs, "create_media_buy", map[string]any{
		"idempotency_key": key, "context": map[string]any{"attempt": "1"},
	})
	second := callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key})

	assert.Equal(t, true, second["replayed"])
	assert.Nil(t, second["context"], "replay must not echo the first request's context")
}

func TestRegisterReadToolsAreNotWrapped(t *testing.T) {
	cs := newRegisteredSession(t, baseTestConfig(Config{
		GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) {
			return &ProductsData{Products: []Product{}}, nil
		},
	}))
	wire := callSession(t, cs, "get_products", map[string]any{"buying_mode": "brief"})
	assert.Nil(t, wire["adcp_error"])
}

func TestRegisterWithoutStoreAdvertisesUnsupported(t *testing.T) {
	var calls int32
	cfg := baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)})
	cfg.Idempotency = nil
	cs := newRegisteredSession(t, cfg)

	caps := callSession(t, cs, "get_adcp_capabilities", map[string]any{})
	idem, ok := caps["adcp"].(map[string]any)["idempotency"].(map[string]any)
	require.True(t, ok, "adcp.idempotency must be an object: %v", caps)
	assert.Equal(t, false, idem["supported"])
	assert.NotContains(t, idem, "replay_ttl_seconds")

	assert.Nil(t, callSession(t, cs, "create_media_buy", map[string]any{})["adcp_error"], "keyless call executes")
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))

	key := idempotency.Generate()
	callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key})
	callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key})
	assert.EqualValues(t, 3, atomic.LoadInt32(&calls), "same key executes twice without a store")
}

func TestBuildCapabilitiesPresetCannotClaimSupportWithoutStore(t *testing.T) {
	caps := buildCapabilities(Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		Capabilities: &CapabilitiesData{
			SupportedProtocols: []string{"media_buy"},
			ADCP:               &ADCPVersion{Idempotency: IdempotencyCaps{Supported: true, ReplayTTLSeconds: 86400}},
		},
	})
	assert.False(t, caps.ADCP.Idempotency.Supported)
	assert.Zero(t, caps.ADCP.Idempotency.ReplayTTLSeconds)
}

func TestRegisterPanicsOnIdempotencyTTLMismatch(t *testing.T) {
	cfg := baseTestConfig(Config{})
	cfg.Idempotency = idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: time.Hour})
	server := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "v0"}, nil)
	assert.Panics(t, func() { Register(server, cfg) })
}

func TestIdempotencyErrorResultInFlight(t *testing.T) {
	result, _, err := idempotencyErrorResult(&idempotency.InFlightError{Key: "k", RetryAfter: 7 * time.Second})
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "IDEMPOTENCY_IN_FLIGHT", e["code"])
	assert.Equal(t, "transient", e["recovery"])
	assert.EqualValues(t, 7, e["retry_after"])
}

func TestIdempotencyErrorResultRecovery(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		code     string
		recovery string
	}{
		{
			name:     "in-flight error",
			err:      &idempotency.InFlightError{Key: "k", RetryAfter: time.Second},
			code:     "IDEMPOTENCY_IN_FLIGHT",
			recovery: "transient",
		},
		{
			name:     "conflict error",
			err:      &idempotency.ConflictError{Key: "k"},
			code:     "IDEMPOTENCY_CONFLICT",
			recovery: "correctable",
		},
		{
			name:     "expired error",
			err:      &idempotency.ExpiredError{Key: "k"},
			code:     "IDEMPOTENCY_EXPIRED",
			recovery: "correctable",
		},
		{
			name:     "missing key error",
			err:      &idempotency.MissingKeyError{},
			code:     "INVALID_REQUEST",
			recovery: "correctable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _, err := idempotencyErrorResult(tt.err)
			require.NoError(t, err)
			e := adcpErrorOf(t, structuredContentMap(t, result))
			assert.Equal(t, tt.code, e["code"])
			assert.Equal(t, tt.recovery, e["recovery"])
		})
	}
}

// TestMutatingToolsMatchSchemas keeps mutatingTools in sync with the bundle:
// every request schema that lists idempotency_key as required is mutating.
func TestMutatingToolsMatchSchemas(t *testing.T) {
	files, err := filepath.Glob("schemas/*/*-request.json")
	require.NoError(t, err)
	if len(files) == 0 {
		t.Skip("schemas not downloaded; run adcp/v3/schemas/download.sh")
	}
	want := map[string]bool{}
	for _, f := range files {
		// #nosec G304 -- test reads request schemas matched by a fixed glob under adcp/v3/schemas.
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var s struct {
			Required []string `json:"required"`
		}
		require.NoError(t, json.Unmarshal(raw, &s))
		for _, r := range s.Required {
			if r == "idempotency_key" {
				name := strings.TrimSuffix(filepath.Base(f), "-request.json")
				want[strings.ReplaceAll(name, "-", "_")] = true
			}
		}
	}
	assert.Equal(t, want, mutatingTools)
}

func TestRegisterReportsInFlightForConcurrentDuplicate(t *testing.T) {
	var calls int32
	started, release := make(chan struct{}), make(chan struct{})
	cs := newRegisteredSession(t, baseTestConfig(Config{
		CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				close(started)
				<-release
			}
			return &CreateMediaBuySuccess{MediaBuyID: "mb-1", Packages: []Package{}}, nil
		},
	}))
	args := map[string]any{"idempotency_key": idempotency.Generate()}

	firstDone := make(chan map[string]any, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_media_buy", Arguments: args})
		if err != nil {
			firstDone <- map[string]any{"transport_error": err.Error()}
			return
		}
		firstDone <- structuredContentMap(t, res)
	}()
	<-started

	second := adcpErrorOf(t, callSession(t, cs, "create_media_buy", args))
	assert.Equal(t, "IDEMPOTENCY_IN_FLIGHT", second["code"])
	assert.Equal(t, "transient", second["recovery"])
	retryAfter, ok := second["retry_after"].(float64)
	require.True(t, ok, "retry_after missing: %v", second)
	assert.GreaterOrEqual(t, retryAfter, 1.0)
	assert.LessOrEqual(t, retryAfter, 30.0)

	close(release)
	first := <-firstDone
	assert.Equal(t, "mb-1", first["media_buy_id"])
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

// failingBackend wraps MemoryBackend and fails the named method with an error
// whose text must never reach the wire.
type failingBackend struct {
	*idempotency.MemoryBackend
	fail string
}

var errBackendSecret = errors.New("pg: dial tcp db-secret-host:5432: connection refused")

func (b *failingBackend) Get(ctx context.Context, scope, key string) (*idempotency.Entry, error) {
	if b.fail == "Get" {
		return nil, errBackendSecret
	}
	return b.MemoryBackend.Get(ctx, scope, key)
}

func (b *failingBackend) PutIfAbsent(ctx context.Context, scope, key string, e *idempotency.Entry) (*idempotency.Entry, bool, error) {
	if b.fail == "PutIfAbsent" {
		return nil, false, errBackendSecret
	}
	return b.MemoryBackend.PutIfAbsent(ctx, scope, key, e)
}

func (b *failingBackend) ReplaceIfHash(ctx context.Context, scope, key, oldHash string, e *idempotency.Entry) (bool, error) {
	if b.fail == "ReplaceIfHash" {
		return false, errBackendSecret
	}
	return b.MemoryBackend.ReplaceIfHash(ctx, scope, key, oldHash, e)
}

func (b *failingBackend) DeleteIfHash(ctx context.Context, scope, key, hash string) (bool, error) {
	if b.fail == "DeleteIfHash" {
		return false, errBackendSecret
	}
	return b.MemoryBackend.DeleteIfHash(ctx, scope, key, hash)
}

type wrappedHandler = func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error)

func callWrapped(ctx context.Context, h wrappedHandler, name string, args map[string]any) (*mcp.CallToolResult, any, error) {
	raw, _ := json.Marshal(args)
	return h(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: raw}}, args)
}

func TestWithIdempotencyStoreFailuresAreServiceUnavailable(t *testing.T) {
	const (
		checkMsg   = "Idempotency check failed"
		recordMsg  = "The response could not be recorded in the idempotency store. Reconcile by natural key before retrying."
		releaseMsg = "The idempotency claim could not be released. Reconcile by natural key before retrying."
	)
	okResult := func() (*mcp.CallToolResult, any, error) {
		return buildResult("ok", map[string]any{"media_buy_id": "mb-1"}), map[string]any{"media_buy_id": "mb-1"}, nil
	}
	errResult := func() (*mcp.CallToolResult, any, error) {
		return Errorf("INVALID_STATE", ErrorOptions{Message: "nope"})
	}
	goErr := func() (*mcp.CallToolResult, any, error) { return nil, nil, errors.New("handler boom") }
	tests := []struct {
		name      string
		fail      string
		handler   func() (*mcp.CallToolResult, any, error)
		message   string
		wantCalls int32
	}{
		{"get fails", "Get", okResult, checkMsg, 0},
		{"claim fails", "PutIfAbsent", okResult, checkMsg, 0},
		{"record fails", "ReplaceIfHash", okResult, recordMsg, 1},
		{"release after error result fails", "DeleteIfHash", errResult, releaseMsg, 1},
		{"release after handler error fails", "DeleteIfHash", goErr, releaseMsg, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fb := &failingBackend{MemoryBackend: idempotency.NewMemoryBackend(0), fail: tt.fail}
			store := idempotency.New(idempotency.Options{Backend: fb, TTL: 24 * time.Hour})
			var calls int32
			h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
				atomic.AddInt32(&calls, 1)
				return tt.handler()
			})
			key := idempotency.Generate()
			ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
			result, _, err := callWrapped(ctx, h, "create_media_buy", map[string]any{"idempotency_key": key})
			require.NoError(t, err, "store failures must be AdCP errors, not transport errors")
			e := adcpErrorOf(t, structuredContentMap(t, result))
			assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
			assert.Equal(t, "transient", e["recovery"])
			assert.Equal(t, tt.message, e["message"])
			wire, _ := json.Marshal(result)
			assert.NotContains(t, string(wire), "db-secret-host")
			assert.NotContains(t, string(wire), key)
			assert.Equal(t, tt.wantCalls, atomic.LoadInt32(&calls))
		})
	}
}

func TestWithIdempotencyPassesThroughHandlerError(t *testing.T) {
	store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
	boom := errors.New("handler boom")
	h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, nil, boom
	})
	ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
	_, _, err := callWrapped(ctx, h, "create_media_buy", map[string]any{"idempotency_key": idempotency.Generate()})
	assert.ErrorIs(t, err, boom)
}
