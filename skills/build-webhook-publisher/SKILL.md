---
name: build-webhook-publisher
description: Use when a Go agent needs to send async notifications (task status changes, list changes, artifact batches, revocations) as AdCP-compliant signed webhooks. Covers RFC 9421 webhook-signing, idempotency_key generation, retry, and push_notification_config decoding via the adcp/webhook package.
---

# Emit AdCP Webhooks (Go)

## Overview

AdCP 3.0 webhooks are RFC 9421-signed POSTs carrying a required `idempotency_key` so receivers can dedupe retries. Publishers use two pieces from `github.com/adcontextprotocol/adcp-go/adcp/webhook`: a signer (one-time setup) and a publisher (one call per event).

## When to Use

Any time an AdCP agent reports state asynchronously after the originating request returned:

- **Seller**: task status transitions (`create_media_buy`, `sync_creatives`, `get_media_buy_delivery`)
- **Signals / collection / property**: resolved-list-changed events
- **Creative**: approval transitions, content artifact batches
- **Rights holder**: revocation notifications

Not this skill: *consuming* webhooks (the same package's `HTTPHandler` handles that — see `adcp/webhook/doc.go`).

## Before Writing Code

Ask the user — don't guess.

1. **Which payload type?** One of: `adcp.MCPWebhookPayload` (task status), `adcp.CollectionListChangedWebhook`, `adcp.PropertyListChangedWebhook`, `adcp.ArtifactWebhookPayload`, `adcp.RevocationNotification`.
2. **Who are the subscribers?** Usually the URL lives in `req.PushNotificationConfig` on the request that started the async work. Use `webhook.DecodeConfig(req.PushNotificationConfig)` to pull it out.
3. **What's the signing key?** Generate once with `signing.GenerateKeyForProfile(AlgEd25519, kid, ProfileWebhookSigning)`, publish the JWK in `adagents.json` under that `kid`, and load the private key from PEM in the server.

## Quickstart

Two-step pattern: construct the Publisher once at server startup, call `Emit` per event.

```go
package main

import (
    "context"
    "errors"
    "log/slog"
    "time"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/adcontextprotocol/adcp-go/adcp/signing"
    "github.com/adcontextprotocol/adcp-go/adcp/webhook"
)

func main() {
    // Your server wiring goes here — see skills/build-seller-agent/ for the
    // full MCP server skeleton. This skeleton shows just the webhook pieces.
}

// Server startup: build the Publisher once. Safe for concurrent use.
func newPublisher(pem []byte) (*webhook.Publisher, error) {
    priv, _, err := signing.LoadPrivateKey(pem)
    if err != nil {
        return nil, err
    }
    signer, err := webhook.NewSigner(signing.SignerOptions{
        KeyID:      "seller-webhook-ed25519-2026", // matches kid in adagents.json
        PrivateKey: priv,
    })
    if err != nil {
        return nil, err
    }
    return webhook.NewPublisher(webhook.PublisherOptions{Signer: signer}), nil
}

// Inside the tool handler: DECODE THE SUBSCRIBER URL EAGERLY and stash it on
// your backend alongside the task id. This is cleaner than holding req around
// — the request lifecycle is short, the async work is long, and the
// subscriber URL is the only field you need later.
//
//   cfg, err := webhook.DecodeConfig(req.PushNotificationConfig)
//   if err != nil && !errors.Is(err, webhook.ErrMissingURL) {
//       // Log; buyer sent a malformed config. Don't fail the request — the
//       // buyer can still poll for status.
//   }
//   if cfg != nil {
//       b.subscribers[taskID] = cfg.URL
//   }

// Per-event (called from the async worker after the task transitions):
func emitStatusChange(ctx context.Context, pub *webhook.Publisher, subscriberURL, taskID, status string) error {
    if subscriberURL == "" {
        return nil // no subscriber — normal, not an error
    }
    payload := &adcp.MCPWebhookPayload{
        TaskID:    taskID,
        TaskType:  "create_media_buy",
        Status:    status,
        Timestamp: time.Now().UTC().Format(time.RFC3339),
    }
    res, err := pub.Emit(ctx, subscriberURL, payload)
    if err != nil {
        var pe *webhook.PublishError
        if errors.As(err, &pe) {
            slog.Error("webhook delivery exhausted",
                "reason", pe.Reason, "attempts", pe.Attempts,
                "last_status", pe.LastStatus, "task_id", taskID)
        }
        return err
    }
    res.Response.Body.Close()
    return nil
}
```

`webhook.DecodeConfig` returns three cases you should handle distinctly:

| Return | Meaning | Action |
|---|---|---|
| `(nil, nil)` | Buyer sent no `push_notification_config` | No subscriber. Task will be poll-only. |
| `(nil, ErrMissingURL)` | Config present but `url` missing | Buyer bug. Log at `Warn`, proceed poll-only. |
| `(cfg, nil)` | Good config | Stash `cfg.URL` on your task state. |

## Deliver vs Publisher — which do I use?

- **`webhook.Publisher`** — production default. Retries 5xx/408/429 with exponential backoff, honors `Retry-After`, bounded by `MaxAttempts` (default 5) + `MaxElapsed` (default 1h). Terminates on 2xx (success) or 4xx-non-retryable (sender bug).
- **`webhook.Deliver`** — one-shot. Use only when you have your own retry loop (e.g., durable job queue) or when the event is fire-and-forget with no retry expectation.

Almost every server wants Publisher. Deliver is the lower-level primitive Publisher builds on.

## Gotchas (real mistakes Claude Code makes)

### 1. Don't re-marshal the payload between retries

`webhook.Publisher.Emit` marshals *once* and resends the same bytes on every attempt. If you call `Marshal` yourself and then build a fresh request each attempt, RFC 9421 content-digest verification will fail. Let Publisher own the bytes.

### 2. `Emit(ctx, url, &payload)` mutates `payload.IdempotencyKey` in place

The first call stamps a UUIDv4 onto the empty field. Subsequent calls with the *same struct* reuse that key — this is correct for retries. If you reuse the same `&payload` for a *logically new event*, reset `payload.IdempotencyKey = ""` first or you'll collide with the previous event's key.

### 3. Don't handroll `CheckRedirect` on the http.Client

`Deliver` and `Publisher.Emit` install `CheckRedirect = http.ErrUseLastResponse` automatically (signed requests MUST NOT follow redirects — the signature binds `@target-uri`). Pass `nil` for the client and you're safe. If you pass a custom `*http.Client` with a `Timeout` but no `CheckRedirect`, the package preserves your `Timeout` and adds `CheckRedirect` via a shallow clone.

### 4. Keep the signing key separate from request-signing keys

`webhook.NewSigner` forces `Profile = ProfileWebhookSigning` regardless of what you pass in `SignerOptions` — this is deliberate, so you can't accidentally emit a request-signing webhook. The corollary: passing `Profile: ProfileRequestSigning` to `webhook.NewSigner` is silently ignored (you still get a webhook signer). Use `signing.NewSigner` directly if you want request-signing.

A JWK published with `adcp_use: "request-signing"` will NOT verify a webhook signature (and vice versa). Generate two distinct keys and publish both in `adagents.json`.

### 5. `idempotency_key` on retry

The receiver dedupes by `(sender_keyid, idempotency_key)`. Publisher preserves the key across retries automatically. If you emit through a queue that restarts, persist the key with the pending job — otherwise the retry after restart mints a new key and the receiver treats it as a new event.

## Retry policy tuning

```go
pub := webhook.NewPublisher(webhook.PublisherOptions{
    Signer: signer,
    Retry: webhook.RetryPolicy{
        MaxAttempts:  8,                // more retries for long-running subscribers
        InitialDelay: 500 * time.Millisecond,
        MaxDelay:     time.Minute,
        Multiplier:   2.0,
        Jitter:       0.2,              // ±20% to desync concurrent publishers
        MaxElapsed:   24 * time.Hour,   // cap total wall-clock — must be within receiver's dedup TTL
    },
})
```

`MaxElapsed` MUST be ≤ the receiver's dedup TTL (typically 24–48h, advertised in `get_adcp_capabilities`). Otherwise a retry after the receiver's TTL expiry will re-trigger the handler.

## Cross-profile key generation (publish both keys)

Your agent likely signs BOTH tool-call responses (request-signing) and webhooks (webhook-signing). Generate two keys:

```go
reqRes, _ := signing.GenerateKeyForProfile(signing.AlgEd25519, "seller-request-2026", signing.ProfileRequestSigning)
whRes, _  := signing.GenerateKeyForProfile(signing.AlgEd25519, "seller-webhook-2026", signing.ProfileWebhookSigning)
// Publish reqRes.PublicJWK and whRes.PublicJWK in your adagents.json JWKS.
// Store reqRes.PrivateKeyPEM and whRes.PrivateKeyPEM as secrets.
```

## Receiving webhooks (brief)

If your agent ALSO receives webhooks — for example, a governance agent that subscribes to collection-list-changed events from downstream governance agents — use `webhook.HTTPHandler`:

```go
mux.Handle("/webhooks/collection-list", webhook.HTTPHandler(webhook.HTTPHandlerOptions{
    Store:   webhook.NewStore(webhook.Options{Backend: idempotency.NewMemoryBackend(time.Minute), TTL: 48 * time.Hour}),
    Handler: func(ctx context.Context, body []byte) error {
        var p adcp.CollectionListChangedWebhook
        if err := json.Unmarshal(body, &p); err != nil { return err }
        // process p
        return nil
    },
    Verification: &webhook.VerificationOptions{
        Resolver: resolver, // signing.NewStaticJWKSResolver or HTTPJWKSResolver
        Replay:   signing.NewMemoryReplayStore(0),
    },
}))
```

The full receiver walkthrough (including legacy HMAC fallback via `AllowUnverified`) lives in `adcp/webhook/doc.go`.

## Common Mistakes

| Symptom | Fix |
|---|---|
| 401 `webhook_signature_required` | Unsigned delivery — use `webhook.NewSigner` + `Publisher`, not `http.DefaultClient` |
| 401 `webhook_signature_key_purpose_invalid` | JWK is scoped for the wrong profile — generate with `ProfileWebhookSigning` |
| 401 `webhook_signature_tag_invalid` | Your signer is emitting `adcp/request-signing/v1` — use `webhook.NewSigner`, not `signing.NewSigner` |
| 409 on retry | You re-marshaled the payload between retries — let `Publisher.Emit` own the bytes |
| 410 on retry | You exceeded the receiver's dedup TTL — lower `MaxElapsed` or persist the pending job with its idempotency_key |
| `Deliver` succeeded but handler never ran | Redirect followed — should not happen with `Publisher`/`Deliver` defaults, but verify `CheckRedirect` on your custom `*http.Client` |

## Related

- `adcp/webhook/doc.go` — package-level godoc with full receiver example
- `adcp/signing/` — RFC 9421 primitives (you rarely touch this directly; `webhook.NewSigner` is the wrapper)
- `adcp/idempotency/` — dedup backend shared with request-side idempotency
- Spec: adcontextprotocol/adcp#2417 (idempotency_key required), #2423 (RFC 9421 webhook-signing profile)
