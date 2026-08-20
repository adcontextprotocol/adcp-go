# uid2client

A native Go client for the [UID2](https://unifiedid.com/) and
[EUID](https://euid.eu/) operator services.

`uid2client` speaks the AES-GCM request/response envelope documented at
<https://unifiedid.com/docs/getting-started/gs-encryption-decryption>,
caches operator keys in memory with a background refresh goroutine, and
decrypts advertising tokens locally — no HTTP call on the decrypt hot
path.

## Install

```
go get github.com/adcontextprotocol/adcp-go/uid2client
```

## Usage

```go
import (
    "context"
    "github.com/adcontextprotocol/adcp-go/uid2client"
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel() // stops the background refresh goroutine

cfg := uid2client.NewUID2Config(os.Getenv("UID2_API_KEY"), os.Getenv("UID2_CLIENT_SECRET"))
cfg.KeyRefreshInterval = 5 * time.Minute
client, err := uid2client.New(ctx, cfg)
if err != nil {
    return err
}

raw, err := client.Decrypt(ctx, advertisingToken)
switch {
case errors.Is(err, uid2client.ErrTokenExpired),
     errors.Is(err, uid2client.ErrKeyNotFound),
     errors.Is(err, uid2client.ErrInvalidToken):
    // drop this identity silently
case err != nil:
    return err
default:
    // raw is the 32-byte raw UID2 identity
}
```

`raw` is the byte payload of the underlying UID2 (or EUID). Base64-encode
it with `encoding/base64.StdEncoding` if you need the string form that
appears in bid requests.

## Configuration

- **Operator URL** — defaults to `https://prod.uidapi.com` for UID2 and
  `https://prod.euid.eu` for EUID. Override via `Config.OperatorURL` for
  integration environments.
- **HTTP timeout** — bounds each key-refresh call. Defaults to 5s.
  Ignored when a caller-supplied `Config.HTTPClient` is used; that
  client is expected to manage its own timeouts.
- **Key refresh interval** — how often the background goroutine polls
  `/v2/key/bidstream`. Defaults to 5 minutes; the reference Java SDK
  uses 1 hour.
- **Recorder** — optional observability hook (`KeyRefresh`,
  `TokenDecrypt`). Implementations should be lightweight (counter
  increments, no I/O).

## Errors

`Decrypt` returns typed sentinel errors — see [`errors.go`](./errors.go):

- `ErrInvalidToken` — malformed / tampered token. Drop.
- `ErrVersionUnsupported` — token version the SDK does not know. Drop.
- `ErrTokenExpired` — token past its expiry. Drop.
- `ErrKeyNotFound` — token references a key not in the cache. Usually
  transient (recent rotation); may be permanent for a credential
  misconfiguration. Drop; alert if persistent.
- `ErrKeysStale` — every cached key has expired. Background refresh is
  broken. Alert.
- `ErrScopeMismatch` — decrypting a UID2 token with an EUID-scoped
  client, or vice versa. Configuration bug.

## Wire compatibility

Token layouts and key-refresh envelope semantics follow
[uid2-client-java](https://github.com/IABTechLab/uid2-client-java) —
specifically `Uid2Encryption.decryptV2` / `decryptV3` and the
`BidstreamClient` refresh path. Supported token versions: V2, V3, V4.

## Concurrency

- `Client.Decrypt` is safe for concurrent use.
- The key store is an atomic snapshot — refresh swaps it in as a whole,
  so readers never see a half-updated map.
- The background refresh goroutine is bound to the `ctx` passed to
  `New`; cancel it to drain.

## Mock-operator e2e tests

A build-tagged, opt-in end-to-end test at
[`mock_e2e_test.go`](./mock_e2e_test.go) validates wire correctness of
`Client.Decrypt` (and, transitively, the request-envelope helpers) against
a real UID2 operator running locally in mock mode. It boots the public
`ghcr.io/iabtechlab/uid2-operator:latest` container, mints an advertising
token with the operator's own `/v2/token/generate`, asks the operator
what raw UID that token should map to via `/v2/identity/map`, and asserts
`Client.Decrypt` returns the same 32 bytes.

Run it manually:

```
MOCK_E2E=1 go test -tags mock_e2e -v -run TestMockE2E ./...
```

Prerequisites:

- Docker daemon reachable (the test skips cleanly if `docker ps` fails).
- ~1 GB free disk (the operator image is ~600 MB compressed) plus a
  couple hundred MB of runtime memory.
- Outbound access to `ghcr.io` on the first run — the image is cached
  after that. On Apple Silicon the image is pulled `--platform
  linux/amd64` and runs under emulation.

The test self-cleans: `t.Cleanup` runs `docker rm -f` on the container
even when the test fails. The container name and host port are
randomized so parallel runs don't collide. The image itself and any
pulled layers are left behind — `docker image rm
ghcr.io/iabtechlab/uid2-operator:latest` will reclaim them.

Both the `//go:build mock_e2e` tag AND the `MOCK_E2E` env var must be
present to run the test — belt and suspenders so a stray `go test ./...`
never boots Docker in CI. This test is deliberately not wired into the
default CI matrix; run it locally before merging changes that touch the
envelope, token-decode, key-refresh, or operator-URL code paths.

## Versioning

Pre-1.0. The public surface (`New`, `Config`, `Client`, sentinel errors,
`ScopeUID2` / `ScopeEUID`, `NewUID2Config` / `NewEUIDConfig`) is
stable, but crypto/testing may still evolve before 1.0.
