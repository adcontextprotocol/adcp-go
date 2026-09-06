# hello-seller

The smallest thing that actually implements the AdCP seller protocol in Go —
a starting point to copy and fork, not a fully-worked reference. If you need
more of the protocol surface (test controller, forced states, signals,
collections, governance, structured actions, etc.), see
[`reference/seller-agent`](../seller-agent) in this repo instead.

## What's here

- **`main.go`** — core protocol surface: account resolution, capabilities,
  `get_products`, `create_media_buy`, `get_media_buys`.
- **`extensions.go`** — extension surface: `update_media_buy`,
  `sync_creatives`, `list_creatives`, `get_media_buy_delivery`. Each handler
  here is independent — wire only the ones your backend actually supports.
- All state is one in-memory `backend` struct, guarded by a single mutex.

Every integration point where a real implementation replaces demo behavior
is marked `// SWAP:` in the source — grep for it.

## Run it

```sh
cd reference/hello-seller
go run .
```

This starts an HTTP server at `http://localhost:3001/mcp` (override the port
with `PORT=4000 go run .`, or the whole URL with `ADCP_AGENT_URL`). Test it
with the AdCP client:

```sh
npx @adcp/client http://localhost:3001/mcp
```

**State is in-memory and resets every time the process restarts.** There is
no database, no persistence, and no authentication — anyone who can reach the
port can call every tool. This is intentional for a minimal example; see the
fork checklist below before running anything like this in production.

`Config.Sandbox: true` marks every response as sandbox/test data (the
`sandbox` field you'll see in `create_media_buy`/`update_media_buy`
responses) — it does not gate authentication or add any real safety
boundary by itself.

## Sample payloads

`get_products`:

```json
{"buying_mode": "brief", "brief": "premium display inventory"}
```

`create_media_buy` (use a `product_id` and `pricing_option_id` from the
`get_products` response above):

```json
{
  "account": {"brand": {"domain": "advertiser.example"}, "operator": "agency.example"},
  "brand": {"domain": "advertiser.example"},
  "start_time": "2026-01-01T00:00:00Z",
  "end_time": "2026-02-01T00:00:00Z",
  "idempotency_key": "11111111-1111-1111-1111-111111111111",
  "packages": [{"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 1000}]
}
```

## Validate against the storyboard harness

`scripts/ci/run_storyboard_reference_seller.sh` (repo root) is the full AdCP
conformance suite, but it's hardwired to boot `reference/seller-agent`
specifically and requires its test controller — it cannot point at
`hello-seller` as-is, since this example deliberately has no test controller
(see the fork checklist). Once you've built out enough of your fork to add
one (following `reference/seller-agent`'s pattern), that script becomes your
real conformance check. Until then, `go test ./...` here and manual
`npx @adcp/client` calls (above) are the validation available for this
minimal surface.

## Production fork checklist

Every item below is marked `// SWAP:` at its integration point in the source.

1. **Sandbox flag** — `Config.Sandbox: true` in `main.go` marks every
   response as sandbox data. A production seller sets this from real
   environment/deploy configuration, not a hardcoded literal — same for the
   `"sandbox": true` in `extensions.go`'s `updateMediaBuyResult`.
2. **`resolveAccount`** — replace the in-memory map + auto-create-on-miss
   with a real lookup against your account/CRM/billing system. Return
   `(nil, nil)` for a genuinely unknown account (not an error) so the SDK
   returns `ACCOUNT_NOT_FOUND` and the buyer calls `sync_accounts` first.
3. **In-memory maps → durable storage.** `backend`'s maps (`products`,
   `mediaBuys`, `creatives`) are lost on every restart. Replace with your
   real ad server / OMS / database.
4. **Wire request signing.** This example has no authentication at all.
   Production sellers should verify inbound request signatures — see
   [`adcp/v3/signing`](../../adcp/v3/signing) in this repo (RFC 9421 request
   signing, `SigningProvider` for KMS/HSM-backed keys, and a Postgres-backed
   replay store for multi-instance deployments).
5. **`agentURL`** — set `ADCP_AGENT_URL` to your real public URL. Buyers
   dereference this to resolve your creative formats (`FormatRef.AgentURL`).
6. **`getDelivery`** — this example always reports zero delivery, since it
   never actually serves impressions. Replace with a real pull from your ad
   server's reporting API.
7. **Idempotency replay store.** `Config.IdempotencyReplayTTL` declares the
   replay window, but durability of that replay record across restarts is up
   to your backend — an in-memory-only implementation (like this example)
   loses replay protection on restart, same caveat as state above.
