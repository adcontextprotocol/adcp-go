package adcp

import (
	"context"
	"encoding/json"
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

func TestRegisterReadToolsAreNotWrapped(t *testing.T) {
	cs := newRegisteredSession(t, baseTestConfig(Config{
		GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) {
			return &ProductsData{Products: []Product{}}, nil
		},
	}))
	wire := callSession(t, cs, "get_products", map[string]any{"buying_mode": "brief"})
	assert.Nil(t, wire["adcp_error"])
}

func TestRegisterPanicsWithoutIdempotencyStore(t *testing.T) {
	cfg := baseTestConfig(Config{})
	cfg.Idempotency = nil
	server := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "v0"}, nil)
	assert.PanicsWithValue(t,
		"adcp.Register: Config.Idempotency is required — build one store outside your server factory: idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(time.Minute), TTL: cfg.IdempotencyReplayTTL})",
		func() { Register(server, cfg) })
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
