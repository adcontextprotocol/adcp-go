package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
	"github.com/adcontextprotocol/adcp-go/adcp/signing"
)

// RetryPolicy controls Publisher's attempt cadence. Zero values select the
// DefaultRetry fields — callers can override any subset.
type RetryPolicy struct {
	// MaxAttempts caps total attempts including the first. A value of 1
	// disables retries. Defaults to 5.
	MaxAttempts int

	// InitialDelay is the backoff before the second attempt. Defaults to 1s.
	InitialDelay time.Duration

	// MaxDelay caps any single backoff. Defaults to 30s.
	MaxDelay time.Duration

	// Multiplier grows the backoff geometrically between attempts. Defaults to 2.0.
	Multiplier float64

	// Jitter is the full-jitter fraction (±Jitter) applied to each backoff.
	// 0.1 = ±10%. Defaults to 0.1. Set to 0 for deterministic delays (tests).
	Jitter float64

	// MaxElapsed aborts retry when the total elapsed time since the first
	// attempt would exceed this value. Defaults to 1 hour. Idempotency keys
	// survive receiver dedup for hours to days, so long-running retries are
	// safe so long as the receiver's dedup window still covers the original
	// key.
	MaxElapsed time.Duration
}

// DefaultRetry is the out-of-the-box policy. Reasonable for most publishers;
// override via PublisherOptions.Retry.
var DefaultRetry = RetryPolicy{
	MaxAttempts:  5,
	InitialDelay: time.Second,
	MaxDelay:     30 * time.Second,
	Multiplier:   2.0,
	Jitter:       0.1,
	MaxElapsed:   time.Hour,
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultRetry.MaxAttempts
	}
	if p.InitialDelay <= 0 {
		p.InitialDelay = DefaultRetry.InitialDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = DefaultRetry.MaxDelay
	}
	if p.Multiplier < 1 {
		p.Multiplier = DefaultRetry.Multiplier
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.MaxElapsed <= 0 {
		p.MaxElapsed = DefaultRetry.MaxElapsed
	}
	return p
}

// PublisherOptions configures a Publisher.
type PublisherOptions struct {
	// Signer is the RFC 9421 signer used on every attempt. Required.
	// Typically constructed via webhook.NewSigner so ProfileWebhookSigning is
	// guaranteed.
	Signer *signing.Signer

	// Client is used for outbound HTTP. Zero value is http.DefaultClient,
	// cloned with CheckRedirect=ErrUseLastResponse (signed requests MUST
	// NOT follow redirects — the signature binds @target-uri).
	Client *http.Client

	// Retry configures the retry policy. Zero value selects DefaultRetry.
	Retry RetryPolicy

	// Clock returns the current time. Injectable for tests. Defaults to time.Now.
	Clock func() time.Time

	// Sleep yields for d or until ctx is canceled, whichever happens first.
	// Injectable for tests so retry timing is deterministic. Defaults to a
	// context-aware time.Timer.
	Sleep func(ctx context.Context, d time.Duration) error

	// Logger logs retry decisions at Warn and attempt outcomes at Debug.
	// Defaults to slog.Default.
	Logger *slog.Logger
}

// Publisher emits signed webhooks with retry. A Publisher is safe for
// concurrent use.
type Publisher struct {
	opts PublisherOptions
}

// NewPublisher returns a Publisher. Panics when Signer is nil — a publisher
// without a signer cannot emit compliant webhooks in AdCP 3.0.
func NewPublisher(opts PublisherOptions) *Publisher {
	if opts.Signer == nil {
		panic("webhook: PublisherOptions.Signer is required")
	}
	opts.Retry = opts.Retry.withDefaults()
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now() }
	}
	if opts.Sleep == nil {
		opts.Sleep = contextSleep
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Publisher{opts: opts}
}

// EmitResult bundles the outcome of a successful Emit.
type EmitResult struct {
	// Response is the final successful HTTP response. Callers MUST close Body.
	Response *http.Response

	// Body is the frozen wire bytes shared across every attempt — useful for
	// audit logging. Retries within a single Emit call already reuse these.
	Body []byte

	// IdempotencyKey is the key from the payload (preserved across attempts).
	IdempotencyKey string

	// Attempts is the total number of HTTP attempts made, including the
	// successful one. Useful for metrics.
	Attempts int

	// Elapsed is total wall-clock time across attempts including backoff.
	Elapsed time.Duration
}

// PublishError is returned when Emit exhausts its retry budget. It bundles
// the most recent response status and network error for diagnostics.
type PublishError struct {
	// URL is the subscriber URL that was targeted.
	URL string
	// Attempts is the number of attempts made before giving up.
	Attempts int
	// Elapsed is the total wall-clock time across attempts.
	Elapsed time.Duration
	// LastStatus is the last HTTP status observed; 0 if every attempt failed
	// at the transport layer.
	LastStatus int
	// LastErr is the last transport-layer error observed; nil if the last
	// attempt produced an HTTP response.
	LastErr error
	// Reason describes why the publisher stopped ("max_attempts",
	// "max_elapsed", "context_canceled", "terminal_status").
	Reason string
}

func (e *PublishError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("webhook: publish to %s failed after %d attempts (%s): %v", e.URL, e.Attempts, e.Reason, e.LastErr)
	}
	return fmt.Sprintf("webhook: publish to %s failed after %d attempts (%s): last status %d", e.URL, e.Attempts, e.Reason, e.LastStatus)
}

func (e *PublishError) Unwrap() error { return e.LastErr }

// Emit marshals p, signs and sends it to url, and retries transient failures
// per the configured RetryPolicy. The idempotency_key is stamped onto p once
// and preserved across every attempt, so a receiver dedupes correctly even
// when the first delivery was lost mid-flight.
//
// Each attempt produces a fresh RFC 9421 signature (new created/expires/
// nonce) — the profile's 300s window means long retry horizons need fresh
// signatures; the static body + stable idempotency_key still lets the
// receiver dedupe.
//
// Returns EmitResult on a terminal success (2xx). Returns *PublishError when
// the retry budget is exhausted, when a 4xx non-retryable status is received,
// or when ctx is canceled. 4xx responses (other than 408 and 429) are treated
// as terminal — the receiver is telling the publisher the request is broken
// and retrying won't help.
func (p *Publisher) Emit(ctx context.Context, url string, payload Payload) (*EmitResult, error) {
	body, key, err := Marshal(payload)
	if err != nil {
		return nil, err
	}

	start := p.opts.Clock()
	client := clientNoRedirect(p.opts.Client)

	var (
		attempts   int
		lastStatus int
		lastErr    error
	)

	for {
		attempts++

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if err := p.opts.Signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}); err != nil {
			return nil, fmt.Errorf("webhook: sign request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			lastStatus = 0
			p.opts.Logger.Debug("webhook: attempt failed at transport",
				"url", url, "attempt", attempts, "key", idempotency.LogKey(key), "err", err)
		} else {
			lastStatus = resp.StatusCode
			class, retryAfter := classifyResponse(resp)
			if class == classSuccess {
				return &EmitResult{
					Response:       resp,
					Body:           body,
					IdempotencyKey: key,
					Attempts:       attempts,
					Elapsed:        p.opts.Clock().Sub(start),
				}, nil
			}
			if class == classTerminal {
				// Drain and close so the connection returns to the pool.
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				return nil, &PublishError{
					URL: url, Attempts: attempts, Elapsed: p.opts.Clock().Sub(start),
					LastStatus: lastStatus, Reason: "terminal_status",
				}
			}
			// Transient — drain, close, and loop.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if retryAfter > 0 {
				p.opts.Logger.Warn("webhook: receiver asked for backoff",
					"url", url, "attempt", attempts, "status", lastStatus,
					"retry_after_ms", retryAfter.Milliseconds(),
					"key", idempotency.LogKey(key))
			} else {
				p.opts.Logger.Debug("webhook: transient status",
					"url", url, "attempt", attempts, "status", lastStatus,
					"key", idempotency.LogKey(key))
			}
			lastErr = nil

			// If Retry-After dominates our computed backoff, honor it.
			if retryAfter > 0 {
				if err := p.sleepOrBudget(ctx, start, retryAfter, attempts); err != nil {
					return nil, publishErrFromSleep(url, attempts, p.opts.Clock().Sub(start), lastStatus, lastErr, err)
				}
				continue
			}
		}

		if attempts >= p.opts.Retry.MaxAttempts {
			return nil, &PublishError{
				URL: url, Attempts: attempts, Elapsed: p.opts.Clock().Sub(start),
				LastStatus: lastStatus, LastErr: lastErr, Reason: "max_attempts",
			}
		}

		delay := backoff(attempts, p.opts.Retry)
		if err := p.sleepOrBudget(ctx, start, delay, attempts); err != nil {
			return nil, publishErrFromSleep(url, attempts, p.opts.Clock().Sub(start), lastStatus, lastErr, err)
		}
	}
}

// sleepOrBudget honors the MaxElapsed budget before sleeping, returning a
// sentinel when the budget is exhausted. A context cancellation during sleep
// is propagated unchanged so callers can distinguish deliberate cancellation
// from elapsed-time exhaustion.
func (p *Publisher) sleepOrBudget(ctx context.Context, start time.Time, delay time.Duration, _ int) error {
	if p.opts.Clock().Sub(start)+delay > p.opts.Retry.MaxElapsed {
		return errElapsed
	}
	return p.opts.Sleep(ctx, delay)
}

var errElapsed = errors.New("webhook: retry budget elapsed")

func publishErrFromSleep(url string, attempts int, elapsed time.Duration, lastStatus int, lastErr error, sleepErr error) *PublishError {
	reason := "context_canceled"
	if errors.Is(sleepErr, errElapsed) {
		reason = "max_elapsed"
	}
	return &PublishError{
		URL: url, Attempts: attempts, Elapsed: elapsed,
		LastStatus: lastStatus, LastErr: lastErr, Reason: reason,
	}
}

// contextSleep waits for d or ctx cancellation, whichever comes first.
func contextSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- response classification ---

type retryClass int

const (
	classSuccess retryClass = iota
	classTransient
	classTerminal
)

// classifyResponse maps an HTTP response to a retry decision. Returns the
// class and an optional Retry-After duration (non-zero when the header is
// present and parseable). Per AdCP guidance and common HTTP practice:
//   - 2xx: success.
//   - 408, 429, 5xx: transient — retry.
//   - Other 4xx: terminal — the receiver is rejecting the request structure
//     itself (missing or malformed idempotency_key, invalid signature, etc),
//     and retrying with the same body will produce the same answer.
func classifyResponse(resp *http.Response) (retryClass, time.Duration) {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return classSuccess, 0
	}
	if resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500 {
		return classTransient, parseRetryAfter(resp.Header.Get("Retry-After"), resp.Request)
	}
	return classTerminal, 0
}

// parseRetryAfter accepts either "delta-seconds" or an HTTP-date and returns
// a non-negative duration. Returns 0 on parse failure so the caller falls back
// to its computed backoff.
func parseRetryAfter(v string, _ *http.Request) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// --- backoff ---

// backoff returns the delay before attempt n (1-indexed). The first retry
// delay is policy.InitialDelay; subsequent delays grow geometrically until
// policy.MaxDelay. Full jitter (±policy.Jitter) is applied when non-zero.
func backoff(attempt int, policy RetryPolicy) time.Duration {
	// attempt=1 is the first attempt — no delay applies before it. This
	// function is called AFTER attempt n fails, to compute the delay before
	// attempt n+1. So the first delay is InitialDelay * Multiplier^0.
	base := float64(policy.InitialDelay) * math.Pow(policy.Multiplier, float64(attempt-1))
	if base > float64(policy.MaxDelay) {
		base = float64(policy.MaxDelay)
	}
	if policy.Jitter > 0 {
		base = base * (1 + jitter(policy.Jitter))
		if base < 0 {
			base = 0
		}
	}
	return time.Duration(base)
}

// jitter returns a number uniformly in [-fraction, +fraction]. Uses
// crypto/rand so jitter in concurrent publishers doesn't correlate the way
// math/rand (seeded with time.Now) would without careful seeding. The jitter
// budget here is small (milliseconds), so the rand cost is irrelevant.
func jitter(fraction float64) float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Deterministic midpoint on rand failure — preserves retry at the
		// base delay without propagating an error through the happy path.
		return 0
	}
	u := binary.BigEndian.Uint64(b[:])
	// Map to [0, 1).
	f := float64(u) / float64(^uint64(0))
	return (f*2 - 1) * fraction
}
