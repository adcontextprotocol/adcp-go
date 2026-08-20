# Mock-operator fixtures

These files patch the public
[`ghcr.io/iabtechlab/uid2-operator`](https://github.com/IABTechLab/uid2-operator)
image so it can be booted as a self-contained mock (no `uid2-core`, no
`uid2-optout`, no S3 / localstack) for the `//go:build mock_e2e` test in
`../mock_e2e_test.go`.

The test overlays this directory onto the operator's Java classpath ahead
of the shipped jar. Because `-cp /patched:/app/uid2-operator-*.jar` puts
`/patched` first, any resource path that exists under both is resolved
against the overlay.

Reproducing the fixtures from scratch (only necessary when the image
schema drifts):

```
IMG=ghcr.io/iabtechlab/uid2-operator:latest
# The image ships everything but a top-level local-config.json — the mock
# path expects the caller to supply one. Extract the classpath keysets.json
# and default-config.json to see the schema:
docker run --rm --platform linux/amd64 --entrypoint sh $IMG -c \
  'cd /app && unzip -p uid2-operator-*.jar com.uid2.core/test/keysets/keysets.json' \
  > keysets.json
docker run --rm --platform linux/amd64 --entrypoint sh $IMG -c \
  'cat /app/conf/default-config.json' > default-config.json
```

## What each file changes

### `patched/conf/local-config.json`

Loaded via `VERTX_CONFIG_PATH=/patched/conf/local-config.json`. This is a
full override that puts the operator into standalone mock mode. The
important knobs relative to the shipped `conf/default-config.json`:

- `storage_mock: true` — read sites / clients / salts / keys from the
  classpath instead of S3.
- `enable_v2_encryption: true` — required by
  `UIDOperatorVerticle.handleKeysBidstream` so `/v2/key/bidstream` returns
  the envelope form. With the default (`false`) the operator would return
  a non-envelope response the Go client can't parse.
- `enable_remote_config: false` — with mock storage there is no
  `runtime_config` metadata to fetch remotely; force bootstrap-only.
- `identity_environment: integ` — one of the enum values the operator
  accepts (`test`, `integ`, `prod`). `integ` is the closest analogue to a
  hosted integration environment.
- All `*_metadata_path` values are switched to their classpath resource
  form (`/com.uid2.core/test/...`) because mock mode reads from
  `EmbeddedResourceStorage`, which requires the leading slash + full
  resource path.

### `patched/com.uid2.core/test/keysets/keysets.json`

Verbatim copy of the classpath keysets.json shipped in the jar, with two
edits to `keyset_id: 2` (the "Publisher keyset") and `keyset_id: -2`
(the "Refresh keyset"):

- `allowed_sites` now includes `123` (DSP / ID_READER site) and `124`
  (Publisher / GENERATOR site).

Without these, the operator generates tokens signed by keyset 2 but
`/v2/key/bidstream` does not share keyset 2's key material with the site
123 client, so `Client.Decrypt` fails with `ErrKeyNotFound`.

## Test credentials

The site 123 / site 124 API keys and secrets embedded in
`../mock_e2e_test.go` come from the public
`uid2-operator/src/main/resources/com.uid2.core/test/clients/clients.json`
fixture. They only unlock this mock stack — they are worthless against a
real operator.
