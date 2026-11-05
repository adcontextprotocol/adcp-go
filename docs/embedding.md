# Embedding the TMP Router

The router and targeting engines (context and identity) are designed to be embedded in existing ad tech infrastructure. This guide covers integration patterns for systems like Prebid Server, custom SSPs, or any Go HTTP service.

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

## Context Match response caching

The response cache keys each entry by `{provider_id, cache_namespace,
context_hash}`. `context_hash` is SHA-256 over RFC 8785 JCS of the exact
validated provider-forwarded request after removing only `$schema` and
`request_id`; array order and every other field remain significant.

The router rejects malformed raw Context Match representations before typed
decoding (invalid UTF-8, unpaired surrogates, duplicate member names, or
trailing data), then hashes the validated, enriched, credential-stripped, and
provider-filtered body actually forwarded. Unicode noncharacters are preserved
deterministically. To bound the maintained JCS implementation's sorting work,
objects above 64 members, documents above 2,048 total members, or nesting above
64 levels bypass response caching only; a valid request is still forwarded and
does not become a client or provider error.

Caching fails closed. A provider without a safe namespace always fans out. For
static deployments, set `ProviderConfig.CacheNamespace` to an opaque generation
token and rotate it whenever any result-affecting state outside the request
changes: outbound auth/tenant, authorization or entitlement revision, active
packages/provider configuration, endpoint, deployed model, or targeting rules.
Never use credentials, hashes derived from credentials, direct principal or
tenant IDs, request values, viewer data, or Identity Match data. Tokens must not
be reused after rotation.

Static namespaces are safe only when the host enforces current caller and
property authorization before invoking `HandleContextMatch`. Deployments whose
outbound auth or entitlement scope can vary per call should resolve a trusted
generation dynamically:

```go
r, err := router.NewRouter(providers, registry, health,
    router.WithContextCache(router.NewContextCache(5*time.Minute)),
    router.WithContextCacheNamespaceResolver(
        func(ctx context.Context, p router.ProviderConfig) router.ContextCacheNamespaceResolution {
            if !currentRequestAuthorized(ctx, p.ID) {
                // Request-local denial bypasses without flushing entries that
                // remain valid for other authorized callers.
                return router.ContextCacheNamespaceResolution{
                    Status: router.ContextCacheNamespaceBypass,
                }
            }
            generation, valid := trustedDeploymentState.CacheGeneration(ctx, p.ID)
            if !valid {
                // Provider-wide uncertainty invalidates the old generation and
                // requires a novel token before caching resumes.
                return router.ContextCacheNamespaceResolution{
                    Status: router.ContextCacheNamespaceUnknown,
                }
            }
            return router.ContextCacheNamespaceResolution{
                Namespace: generation,
                Status:    router.ContextCacheNamespaceReady,
            }
        },
    ),
)
```

The resolver receives no Context Match or Identity Match object. It may consult
authenticated state placed in `ctx` by trusted middleware. It runs on warm hits
and again before insertion; the router retains the originally captured
generation across lookup, outbound call, and insertion so an old in-flight
response cannot populate a newer namespace. Return
`ContextCacheNamespaceBypass` for request-specific authorization denial or
cache ineligibility; this never mutates provider-wide cache state. Return
`ContextCacheNamespaceUnknown` only when the current global generation cannot
be established; this purges and blocks reuse of the prior generation.

The cache retains a bounded history of 1,024 namespace generations per
provider so an old token can never be accepted again. Exhausting that history
permanently bypasses caching for the provider until process restart rather than
discarding replay protection. The reference router exports
`tmp_context_cache_generation_exhausted_total{provider=...}`; embedders can
implement the optional `ContextCacheGenerationMetrics` extension. The metric
contains only the stable provider ID, never namespace material.

## Injecting your logger

The router logs at `Debug` level for protocol errors and write failures. By default it uses `slog.Default()`. Use `WithLogger` to route logs into your own system:

```go
r := router.NewRouter(providers, registry, nil, health,
    router.WithLogger(slog.New(myHandler)),
)
```

## Implementing targeting.Metrics

The `targeting.Metrics` interface is the observability hook for the evaluation engine. If your host application already has Prometheus (or any metrics system), implement the interface directly:

```go
type MyMetrics struct {
    contextEval *prometheus.CounterVec
    latency     *prometheus.HistogramVec
    // ...
}

func (m *MyMetrics) ContextEvaluated(packageID, stage string, passed bool) {
    m.contextEval.WithLabelValues(stage, strconv.FormatBool(passed)).Inc()
}

func (m *MyMetrics) Latency(stage string, d time.Duration) {
    m.latency.WithLabelValues(stage).Observe(d.Seconds())
}

// ... implement remaining methods
```

Then pass it to whichever engine you're constructing:

```go
ctxEngine := targeting.NewContextEngine(targeting.ContextEngineConfig{
    Metrics: &MyMetrics{...},
    // ...
})

idEngine := targeting.NewIdentityEngine(targeting.IdentityEngineConfig{
    Metrics: &MyMetrics{...},
    // ...
})
```

The `targeting/prommetrics` sub-module is a reference implementation with zero external dependencies. Use it directly or as a template for your own.

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
