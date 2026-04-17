# Embedding the TMP Router

The router and targeting engine are designed to be embedded in existing ad tech infrastructure. This guide covers integration patterns for systems like Prebid Server, custom SSPs, or any Go HTTP service.

## Basic embedding

```go
import (
    "github.com/adcontextprotocol/adcp-go/router"
    "github.com/adcontextprotocol/adcp-go/targeting"
)

// Create the router with your own HTTP client and logger.
r := router.NewRouter(providers, registry, sigCache, health,
    router.WithHTTPClient(myHTTPClient),
    router.WithLogger(myLogger.With("component", "tmp")),
)

// Mount handlers on your existing mux.
mux.HandleFunc("POST /tmp/context", r.HandleContextMatch)
mux.HandleFunc("POST /tmp/identity", r.HandleIdentityMatch)
```

## Injecting your HTTP client

The router makes HTTP calls to TMP providers during fan-out. By default it creates its own `http.Client` with connection pooling. Use `WithHTTPClient` to inject your own:

```go
client := &http.Client{
    Transport: otelhttp.NewTransport(&http.Transport{
        TLSClientConfig: myTLSConfig,
        MaxIdleConns:    200,
    }),
    Timeout: 100 * time.Millisecond,
}

r := router.NewRouter(providers, registry, nil, health,
    router.WithHTTPClient(client),
)
```

This lets you add OpenTelemetry tracing, custom TLS, mutual TLS, or any `http.RoundTripper` middleware to provider calls.

## Injecting your logger

The router logs at `Debug` level for protocol errors and write failures. By default it uses `slog.Default()`. Use `WithLogger` to route logs into your own system:

```go
r := router.NewRouter(providers, registry, nil, health,
    router.WithLogger(slog.New(myHandler)),
)
```

## Implementing targeting.Metrics and exposure.Metrics

Observability is split across two interfaces. The evaluation engine emits
`targeting.Metrics` callbacks (context/identity evaluation outcomes, stage
latency, store errors). The impression recorder in the `exposure` package
emits `exposure.Metrics` callbacks (exposure recorded, store errors during
write). A single type can satisfy both — that is what `targeting/prommetrics`
does.

```go
type MyMetrics struct {
    contextEval     *prometheus.CounterVec
    identityEval    *prometheus.CounterVec
    exposureRecord  *prometheus.CounterVec
    storeErrors     *prometheus.CounterVec
    latency         *prometheus.HistogramVec
}

// targeting.Metrics — read path (evaluation).
func (m *MyMetrics) ContextEvaluated(packageID, stage string, passed bool) {
    m.contextEval.WithLabelValues(stage, strconv.FormatBool(passed)).Inc()
}
func (m *MyMetrics) IdentityEvaluated(packageID, stage string, passed bool) { /* ... */ }
func (m *MyMetrics) Latency(stage string, d time.Duration)                  { /* ... */ }

// Shared with exposure.Metrics — both interfaces include StoreError.
func (m *MyMetrics) StoreError(operation string, err error) { /* ... */ }

// exposure.Metrics — write path (impression recording).
func (m *MyMetrics) ExposureRecorded(packageID string) {
    m.exposureRecord.WithLabelValues().Inc()
}
```

Wire each interface where it's consumed:

```go
engine := targeting.NewEngine(targeting.EngineConfig{
    Metrics: &MyMetrics{...}, // satisfies targeting.Metrics
    // ...
})

recorder := exposure.NewRecorder(exposure.RecorderConfig{
    Metrics: &MyMetrics{...}, // satisfies exposure.Metrics
    // ...
})
```

The `targeting/prommetrics` sub-module is a reference implementation with
zero external dependencies that satisfies both interfaces on a single struct.
Use it directly or as a template.

### Breaking change in this split

Prior versions exposed `ExposureRecorded` on `targeting.Metrics` because the
engine owned impression recording. Recording is now owned by
`exposure.Recorder`, and the callback lives on `exposure.Metrics`. External
implementations that defined `ExposureRecorded` on their `targeting.Metrics`
type still compile (extra methods don't break satisfaction), but the engine
no longer invokes that method. To continue receiving the callback, pass the
same implementation as `exposure.RecorderConfig.Metrics` when constructing
the recorder.

## Security considerations for embedders

### Metrics labels

Never put user-controlled values in Prometheus labels. The `targeting.Metrics` interface passes `packageID` to `ContextEvaluated` and `IdentityEvaluated`, but the reference `prommetrics` implementation discards it (uses `_` for that parameter). If you implement your own, either:
- Discard `packageID` (recommended)
- Validate it against a known set before using it as a label

Unbounded label cardinality is a denial-of-service vector.

### Error messages

The router returns generic error messages to callers. If you wrap the handlers with your own middleware, don't add error details to HTTP responses. Log them server-side.

### The pinhole

If you're running the identity agent in a TEE, the wire response defines the pinhole. Only these fields should leave the enclave:

- `eligible_package_ids` ([]string) — package IDs the user is eligible for
- `ttl_sec` (int) — caching duration
- `tmpx` (string) — HPKE-encrypted exposure token, opaque to the router and publisher

The TMPX token is encrypted by the read replica inside the TEE using HPKE. The router and publisher pass it through without decryption. Only the buyer's cluster master can decrypt it.

If you add fields to the wire response, you widen the pinhole. Review carefully.

### Dependencies

The root module (`github.com/adcontextprotocol/adcp-go`) has zero external dependencies. When embedding, you import it without pulling in any transitive deps. Sub-modules like `prommetrics` and `valkeystore` add deps only where needed.

The `targeting/prommetrics` module is stdlib-only (~250 lines). It does not use `prometheus/client_golang` (which pulls in protobuf and `/proc` readers). If your host already has the Prometheus client, implement `targeting.Metrics` against your own registry instead of using `prommetrics`.
