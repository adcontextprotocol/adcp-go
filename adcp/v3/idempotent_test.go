package adcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// withTestPrincipal stands in for auth middleware: idempotent tools need a
// principal to scope keys.
func withTestPrincipal(principal string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(idempotency.WithPrincipal(ctx, principal), method, req)
		}
	}
}

func newRegisteredSession(t *testing.T, cfg Config) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0.0.1"}, nil)
	server.AddReceivingMiddleware(withTestPrincipal("test-buyer"))
	Register(server, cfg)
	return connectInMemory(t, server)
}

func connectInMemory(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
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

// A typed, non-transient rejection is a known outcome: it is returned as-is
// and the key is released so an exact retry re-executes.
func TestRegisterDoesNotCacheErrorResults(t *testing.T) {
	var calls int32
	cs := newRegisteredSession(t, baseTestConfig(Config{
		CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nil, NewError("BUDGET_TOO_LOW", ErrorOptions{Message: "budget below minimum", Recovery: "correctable"})
			}
			return &CreateMediaBuySuccess{MediaBuyID: "mb-ok", Packages: []Package{}}, nil
		},
	}))
	key := idempotency.Generate()

	first := callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": key})
	assert.Equal(t, "BUDGET_TOO_LOW", adcpErrorOf(t, first)["code"])

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

func TestTransientErrorCodesMatchSchema(t *testing.T) {
	raw, err := os.ReadFile("schemas/enums/error-code.json")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("schemas not downloaded; run adcp/v3/schemas/download.sh")
	}
	require.NoError(t, err)
	var s struct {
		EnumMetadata map[string]json.RawMessage `json:"enumMetadata"`
	}
	require.NoError(t, json.Unmarshal(raw, &s))
	want := map[string]bool{}
	for code, m := range s.EnumMetadata {
		var meta struct {
			Recovery string `json:"recovery"`
		}
		if json.Unmarshal(m, &meta) == nil && meta.Recovery == "transient" {
			want[code] = true
		}
	}
	assert.Equal(t, want, transientErrorCodes)
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
	switch b.fail {
	case "ReplaceIfHash":
		return false, errBackendSecret
	case "ReplaceIfHash:lose":
		// The claim was removed out of band before it could be finalized.
		return false, nil
	}
	return b.MemoryBackend.ReplaceIfHash(ctx, scope, key, oldHash, e)
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
		lostMsg    = "The request lost its idempotency claim before its response could be recorded. Retry safely."
	)
	okResult := func() (*mcp.CallToolResult, any, error) {
		return buildResult("ok", map[string]any{"media_buy_id": "mb-1"}), map[string]any{"media_buy_id": "mb-1"}, nil
	}
	errResult := func() (*mcp.CallToolResult, any, error) {
		return Errorf("INVALID_STATE", ErrorOptions{Message: "nope"})
	}
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
		{"release after error result fails", "ReplaceIfHash", errResult, releaseMsg, 1},
		{"claim lost before record", "ReplaceIfHash:lose", okResult, lostMsg, 1},
		{"claim lost before release", "ReplaceIfHash:lose", errResult, lostMsg, 1},
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
			logs := captureLogs(t)
			result, _, err := callWrapped(ctx, h, "create_media_buy", map[string]any{"idempotency_key": key})
			require.NoError(t, err, "store failures must be AdCP errors, not transport errors")
			if tt.wantCalls > 0 {
				assert.Contains(t, logs.String(), "level=ERROR", "unrecorded outcomes are logged")
				assert.Contains(t, logs.String(), "tool=create_media_buy")
				assert.Contains(t, logs.String(), "key="+idempotency.LogKey(key))
				if tt.fail == "ReplaceIfHash" {
					assert.Contains(t, logs.String(), "db-secret-host", "operators need the backend cause")
				}
			}
			assert.NotContains(t, logs.String(), key)
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

// A handler Go error means the outcome is unknown: the claim stays fenced so
// a retry cannot execute twice, and the raw error never reaches the wire.
func TestWithIdempotencyHandlerErrorKeepsKeyFenced(t *testing.T) {
	store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
	var calls int32
	h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil, errors.New("handler boom db-secret-host")
	})
	ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
	key := idempotency.Generate()
	args := map[string]any{"idempotency_key": key}
	logs := captureLogs(t)

	result, _, err := callWrapped(ctx, h, "create_media_buy", args)
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
	assert.Equal(t, "transient", e["recovery"])
	assert.Equal(t, "The request outcome is unknown. Reconcile by natural key before retrying.", e["message"])
	wire, _ := json.Marshal(result)
	assert.NotContains(t, string(wire), "db-secret-host")
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.Contains(t, logs.String(), `msg="adcp: handler outcome unknown; idempotency key fenced"`)
	assert.Contains(t, logs.String(), "tool=create_media_buy")
	assert.Contains(t, logs.String(), "key="+idempotency.LogKey(key))
	assert.Contains(t, logs.String(), "handler boom db-secret-host", "operators need the cause")
	assert.NotContains(t, logs.String(), key)

	result, _, err = callWrapped(ctx, h, "create_media_buy", args)
	require.NoError(t, err)
	assert.Equal(t, "IDEMPOTENCY_IN_FLIGHT", adcpErrorOf(t, structuredContentMap(t, result))["code"])
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

// A Config handler error whose outcome is unknown (untyped, or typed
// transient) keeps the key fenced like a WithIdempotency handler Go error.
func TestRegisterFencesUnknownMutationOutcome(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("ad server db-secret-host timed out")},
		{"wrapped typed transient code", fmt.Errorf("wrap: %w", NewError("SERVICE_UNAVAILABLE", ErrorOptions{Message: "db-secret-host down"}))},
		{"explicit transient recovery", NewError("VENDOR_TIMEOUT", ErrorOptions{Message: "db-secret-host slow", Recovery: "transient"})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			var calls int32
			cs := newRegisteredSession(t, baseTestConfig(Config{
				CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
					atomic.AddInt32(&calls, 1)
					return nil, tt.err
				},
			}))
			key := idempotency.Generate()
			args := map[string]any{"idempotency_key": key}

			result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_media_buy", Arguments: args})
			require.NoError(t, err)
			e := adcpErrorOf(t, structuredContentMap(t, result))
			assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
			assert.Equal(t, "transient", e["recovery"])
			assert.Equal(t, outcomeUnknownMsg, e["message"])
			wire, _ := json.Marshal(result)
			assert.NotContains(t, string(wire), "db-secret-host")

			assert.Equal(t, "IDEMPOTENCY_IN_FLIGHT", adcpErrorOf(t, callSession(t, cs, "create_media_buy", args))["code"])
			assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
			assert.Contains(t, logs.String(), "level=ERROR")
			assert.Contains(t, logs.String(), "tool=create_media_buy")
			assert.NotContains(t, logs.String(), key, "full keys never reach logs")
		})
	}
}

// Without a store nothing is fenced: handler errors stay results, as before.
func TestRegisterWithoutStoreKeepsHandlerErrorsAsResults(t *testing.T) {
	cfg := baseTestConfig(Config{
		CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
			return nil, errors.New("db-secret-host down")
		},
	})
	cfg.Idempotency = nil
	cs := newRegisteredSession(t, cfg)

	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_media_buy", Arguments: map[string]any{"idempotency_key": idempotency.Generate()}})
	require.NoError(t, err)
	assert.Equal(t, "INTERNAL_ERROR", adcpErrorOf(t, structuredContentMap(t, result))["code"])
	wire, _ := json.Marshal(result)
	assert.NotContains(t, string(wire), "db-secret-host")
}

// With an optional key omitted there is no claim to fence, but the handler
// error must still not reach the wire as raw text.
func TestRegisterUnkeyedMutationErrorIsOutcomeUnknown(t *testing.T) {
	required := false
	cs := newRegisteredSession(t, baseTestConfig(Config{
		Idempotency: idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour, KeyRequired: &required}),
		CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
			return nil, errors.New("db-secret-host down")
		},
	}))

	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_media_buy", Arguments: map[string]any{}})
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
	assert.Equal(t, outcomeUnknownMsg, e["message"])
	wire, _ := json.Marshal(result)
	assert.NotContains(t, string(wire), "db-secret-host")
}

// captureLogs routes the default slog logger into a buffer for one test.
// Tests in this package do not run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const noPrincipalMsg = "Idempotency principal could not be resolved; authenticate callers (e.g. bearer auth) before enabling idempotency."

func TestRegisterRefusesIdempotentCallWithoutPrincipal(t *testing.T) {
	var calls int32
	server := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0.0.1"}, nil)
	Register(server, baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)}))
	cs := connectInMemory(t, server)

	e := adcpErrorOf(t, callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": idempotency.Generate()}))
	assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
	assert.Equal(t, "transient", e["recovery"])
	assert.Equal(t, noPrincipalMsg, e["message"])
	assert.Zero(t, atomic.LoadInt32(&calls))
}

// sessionHeader forges the Mcp-Session-Id header on every request.
type sessionHeader struct{ id string }

func (h sessionHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Mcp-Session-Id", h.id)
	return http.DefaultTransport.RoundTrip(r)
}

// The MCP transport session ID is never an idempotency scope: stateless
// servers accept a client-chosen ID (cross-client replay) and stateful ones
// break reconnect retries. Without a principal, keyed calls are refused.
func TestRegisterRefusesIdempotentCallOverHTTPWithoutPrincipal(t *testing.T) {
	for _, tt := range []struct {
		name      string
		stateless bool
		forgedID  string
	}{
		{"stateless", true, ""},
		{"stateless forged session id", true, "victim-session"},
		{"stateful", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32
			server := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0.0.1"}, nil)
			Register(server, baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)}))
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: tt.stateless})
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)

			transport := &mcp.StreamableClientTransport{Endpoint: httpServer.URL}
			if tt.forgedID != "" {
				transport.HTTPClient = &http.Client{Transport: sessionHeader{tt.forgedID}}
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "seller-test-client", Version: "v0.0.1"}, nil)
			cs, err := client.Connect(context.Background(), transport, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = cs.Close() })

			args := map[string]any{"idempotency_key": idempotency.Generate()}
			for range 2 {
				e := adcpErrorOf(t, callSession(t, cs, "create_media_buy", args))
				assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
				assert.Equal(t, "transient", e["recovery"])
				assert.Equal(t, noPrincipalMsg, e["message"])
			}
			assert.Zero(t, atomic.LoadInt32(&calls))
		})
	}
}

func TestWithIdempotencyBindsToolNameIntoHash(t *testing.T) {
	store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
	var createCalls, updateCalls int32
	counting := func(calls *int32) wrappedHandler {
		return WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			atomic.AddInt32(calls, 1)
			return buildResult("ok", map[string]any{"list_id": "l-1"}), map[string]any{"list_id": "l-1"}, nil
		})
	}
	ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
	args := map[string]any{"idempotency_key": idempotency.Generate(), "name": "same"}

	first, _, err := callWrapped(ctx, counting(&createCalls), "create_collection_list", args)
	require.NoError(t, err)
	assert.Nil(t, structuredContentMap(t, first)["adcp_error"])

	second, _, err := callWrapped(ctx, counting(&updateCalls), "update_collection_list", args)
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, second))
	assert.Equal(t, "IDEMPOTENCY_CONFLICT", e["code"])
	assert.EqualValues(t, 1, atomic.LoadInt32(&createCalls))
	assert.Zero(t, atomic.LoadInt32(&updateCalls))
}

func TestWithIdempotencyRejectsNonObjectArguments(t *testing.T) {
	store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
	var calls int32
	h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		atomic.AddInt32(&calls, 1)
		return buildResult("ok", map[string]any{}), nil, nil
	})
	ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
	result, _, err := h(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "create_media_buy", Arguments: json.RawMessage(`[1]`)}}, nil)
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "INVALID_REQUEST", e["code"])
	assert.Equal(t, "correctable", e["recovery"])
	assert.Zero(t, atomic.LoadInt32(&calls))
}

func TestRegisterWithoutStoreValidatesKeyFormat(t *testing.T) {
	var calls int32
	cfg := baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)})
	cfg.Idempotency = nil
	cs := newRegisteredSession(t, cfg)

	e := adcpErrorOf(t, callSession(t, cs, "create_media_buy", map[string]any{"idempotency_key": "short"}))
	assert.Equal(t, "INVALID_REQUEST", e["code"])
	assert.Equal(t, "idempotency_key", e["field"])
	assert.Equal(t, "correctable", e["recovery"])
	assert.Zero(t, atomic.LoadInt32(&calls))

	assert.Nil(t, callSession(t, cs, "create_media_buy", map[string]any{})["adcp_error"])
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

// The key is validated before the principal is resolved (TS order), so a
// bad or missing key is correctable even when the caller is unauthenticated.
func TestWithIdempotencyValidatesKeyBeforePrincipal(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		code    string
		message string
	}{
		{"missing key", map[string]any{}, "INVALID_REQUEST", "idempotency_key is required on state-changing requests"},
		{"malformed key", map[string]any{"idempotency_key": "short"}, "INVALID_REQUEST", ""},
		{"valid key", map[string]any{"idempotency_key": idempotency.Generate()}, "SERVICE_UNAVAILABLE", noPrincipalMsg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
			var calls int32
			h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
				atomic.AddInt32(&calls, 1)
				return buildResult("ok", map[string]any{}), nil, nil
			})
			result, _, err := callWrapped(context.Background(), h, "create_media_buy", tt.args)
			require.NoError(t, err)
			e := adcpErrorOf(t, structuredContentMap(t, result))
			assert.Equal(t, tt.code, e["code"])
			if tt.code == "INVALID_REQUEST" {
				assert.Equal(t, "idempotency_key", e["field"])
				assert.Equal(t, "correctable", e["recovery"])
			}
			if tt.message != "" {
				assert.Equal(t, tt.message, e["message"])
			}
			assert.Zero(t, atomic.LoadInt32(&calls))
		})
	}
}

func TestWithIdempotencyOptionalKeyRunsWithoutPrincipal(t *testing.T) {
	optional := false
	store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour, KeyRequired: &optional})
	var calls int32
	h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		atomic.AddInt32(&calls, 1)
		return buildResult("ok", map[string]any{"ok": true}), nil, nil
	})
	result, _, err := callWrapped(context.Background(), h, "si_terminate_session", map[string]any{})
	require.NoError(t, err)
	assert.Nil(t, structuredContentMap(t, result)["adcp_error"])
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestWithIdempotencyReclaimFailureIsServiceUnavailable(t *testing.T) {
	fb := &failingBackend{MemoryBackend: idempotency.NewMemoryBackend(0)}
	store := idempotency.New(idempotency.Options{Backend: fb, TTL: 24 * time.Hour})
	var calls int32
	h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		atomic.AddInt32(&calls, 1)
		return Errorf("INVALID_STATE", ErrorOptions{Message: "nope"})
	})
	ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
	args := map[string]any{"idempotency_key": idempotency.Generate()}
	_, _, err := callWrapped(ctx, h, "create_media_buy", args)
	require.NoError(t, err)

	fb.fail = "ReplaceIfHash" // the retry's marker reclaim fails
	result, _, err := callWrapped(ctx, h, "create_media_buy", args)
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "SERVICE_UNAVAILABLE", e["code"])
	assert.Equal(t, "transient", e["recovery"])
	wire, _ := json.Marshal(result)
	assert.NotContains(t, string(wire), "db-secret-host")
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestWithIdempotencyNilStoreValidatesKeyFormat(t *testing.T) {
	var calls int32
	h := WithIdempotency(nil, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		atomic.AddInt32(&calls, 1)
		return buildResult("ok", map[string]any{}), nil, nil
	})
	result, _, err := callWrapped(context.Background(), h, "update_media_buy", map[string]any{"idempotency_key": "short"})
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "INVALID_REQUEST", e["code"])
	assert.Equal(t, "idempotency_key", e["field"])
	assert.Zero(t, atomic.LoadInt32(&calls))

	_, _, err = callWrapped(context.Background(), h, "update_media_buy", map[string]any{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestWithIdempotencyRejectsReservedToolField(t *testing.T) {
	store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
	var calls int32
	h := WithIdempotency(store, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		atomic.AddInt32(&calls, 1)
		return buildResult("ok", map[string]any{}), nil, nil
	})
	ctx := idempotency.WithPrincipal(context.Background(), "buyer-1")
	result, _, err := callWrapped(ctx, h, "create_media_buy", map[string]any{"idempotency_key": idempotency.Generate(), "$adcp_tool": "update_media_buy"})
	require.NoError(t, err)
	e := adcpErrorOf(t, structuredContentMap(t, result))
	assert.Equal(t, "INVALID_REQUEST", e["code"])
	assert.Equal(t, "$adcp_tool", e["field"])
	assert.Equal(t, "correctable", e["recovery"])
	assert.Zero(t, atomic.LoadInt32(&calls))
}
