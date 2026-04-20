// Package webhook provides sender and receiver helpers for AdCP webhook
// payloads. It wires the two baseline requirements from AdCP 3.0:
//
//   - idempotency_key generation and dedup per adcontextprotocol/adcp#2417
//   - RFC 9421 webhook-signing verification per adcontextprotocol/adcp#2423
//
// # Sender (publisher)
//
//	signer, _ := webhook.NewSigner(signing.SignerOptions{
//	    KeyID:      "publisher-ed25519-2026",
//	    PrivateKey: priv, // from signing.LoadPrivateKey(pemBytes)
//	})
//	p := adcp.MCPWebhookPayload{
//	    TaskID:    "task_123",
//	    TaskType:  "create_media_buy",
//	    Status:    "completed",
//	    Timestamp: time.Now().UTC().Format(time.RFC3339),
//	}
//
//	// Quick one-shot (no retry):
//	res, err := webhook.Deliver(ctx, subscriberURL, &p, signer, nil)
//
//	// Production — Publisher wraps Deliver with retry + backoff. Retries
//	// on 5xx/408/429 (honors Retry-After); terminates on 2xx/4xx.
//	pub := webhook.NewPublisher(webhook.PublisherOptions{Signer: signer})
//	emitted, err := pub.Emit(ctx, subscriberURL, &p)
//	if err == nil { emitted.Response.Body.Close() }
//
//	// Reading the subscriber URL out of the originating AdCP request:
//	cfg, _ := webhook.DecodeConfig(req.PushNotificationConfig)
//	if cfg != nil {
//	    pub.Emit(ctx, cfg.URL, &p)
//	}
//
// # Receiver (subscriber)
//
//	// JWKS resolver: map each publisher's keyid to its published JWK. In
//	// production, use signing.HTTPJWKSResolver with the publisher's jwks_uri
//	// (from adagents.json). StaticJWKSResolver is shown here for clarity.
//	resolver := signing.NewStaticJWKSResolver()
//	resolver.Put("publisher-ed25519-2026", &publisherJWK, "https://publisher.example.com")
//
//	store := webhook.NewStore(webhook.Options{
//	    Backend: idempotency.NewMemoryBackend(time.Minute),
//	    TTL:     24 * time.Hour,
//	})
//	handler := func(ctx context.Context, body []byte) error {
//	    var p adcp.MCPWebhookPayload
//	    if err := json.Unmarshal(body, &p); err != nil { return err }
//	    // process...
//	    return nil
//	}
//	mux := http.NewServeMux()
//	mux.Handle("/webhooks/mcp", webhook.HTTPHandler(webhook.HTTPHandlerOptions{
//	    Store:   store,
//	    Handler: handler,
//	    // Verification chains ProfileWebhookSigning + DigestRequired before
//	    // the dedup layer. Unsigned / wrong-profile deliveries are rejected
//	    // with webhook_signature_* codes. Omitting Verification panics at
//	    // construction time unless AllowUnverified is set (legacy HMAC path).
//	    Verification: &webhook.VerificationOptions{
//	        Resolver: resolver,
//	        Replay:   signing.NewMemoryReplayStore(0),
//	    },
//	}))
//	http.ListenAndServe(":8080", mux)
//
// # Dedup scope
//
// Per spec guidance, idempotency_key uniqueness is scoped to the authenticated
// sender identity — keys from different senders are independent. When
// Verification is set, HTTPHandler derives the sender from the verified
// RFC 9421 keyid. Callers authenticating via HMAC or Bearer (legacy, removed
// in AdCP 4.0) must set AllowUnverified=true AND supply a custom Sender.
package webhook
