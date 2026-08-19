package uid2client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Default operator URLs. Consumers can override via [Config.OperatorURL];
// these match the values documented on unifiedid.com and euid.eu.
const (
	DefaultUID2OperatorURL = "https://prod.uidapi.com"
	DefaultEUIDOperatorURL = "https://prod.euid.eu"

	// keyRefreshPath is the operator endpoint used by BidstreamClient in
	// the reference SDKs; it returns the shared keyset a bid-side
	// participant needs to decrypt any advertising token addressed to
	// them. Path relative to Config.OperatorURL.
	keyRefreshPath = "/v2/key/bidstream"

	defaultKeyRefreshInterval = 5 * time.Minute
	defaultHTTPTimeout        = 5 * time.Second
)

// Config is the constructor input for [New]. All fields are optional
// except APIKey and ClientSecret; use [NewUID2Config] or [NewEUIDConfig]
// to fill in the scope-specific defaults.
type Config struct {
	// OperatorURL is the base URL of the operator service (no path).
	// Defaults to [DefaultUID2OperatorURL] or [DefaultEUIDOperatorURL]
	// depending on IdentityScope.
	OperatorURL string

	// APIKey is the operator-issued bearer token. Sent as
	// "Authorization: Bearer <APIKey>" on every key-refresh call.
	APIKey string

	// ClientSecret is the operator-issued AES key, base64-encoded. Must
	// decode to exactly 32 bytes.
	ClientSecret string

	// IdentityScope selects between UID2 and EUID. See [ScopeUID2] and
	// [ScopeEUID].
	IdentityScope IdentityScope

	// HTTPClient is used for key-refresh HTTP calls. When nil, a client
	// with HTTPTimeout is created; callers that want a custom transport
	// (mTLS, custom retry, etc.) should supply their own here.
	HTTPClient *http.Client

	// HTTPTimeout bounds any single key-refresh HTTP call. Defaults to
	// 5s. Only consulted when HTTPClient is nil; a caller-supplied
	// client is trusted to manage its own timeouts.
	HTTPTimeout time.Duration

	// KeyRefreshInterval controls how often the background goroutine
	// pulls fresh keys. Defaults to 5 minutes. The reference Java SDK
	// uses 1 hour; the shorter default here favors rapid propagation of
	// new keys (a key coming online at operator time T is usable by this
	// client by T+KeyRefreshInterval).
	KeyRefreshInterval time.Duration

	// Recorder is an optional observability hook. Nil = no-op. The
	// implementation is expected to be lightweight (increment a counter,
	// no blocking work) — [Client.Decrypt] calls it inline on the hot
	// path.
	Recorder Recorder

	// Logger is used for background refresh diagnostics. Nil =
	// slog.Default().
	Logger *slog.Logger
}

// Recorder is the observability contract this client honors. Implementations
// are expected to be non-blocking (increment a counter, no I/O). Nil
// recorders are treated as no-ops at every call site.
type Recorder interface {
	// KeyRefresh is called after each background refresh attempt with
	// err == nil on success. Callers typically increment a counter
	// dimensioned by outcome (success / auth_failed / network / parse).
	KeyRefresh(err error)

	// TokenDecrypt is called once per Decrypt call with a reason string
	// keyed off the error class. Reasons: "success", "invalid",
	// "expired", "opted_out", "key_not_found", "scope_mismatch",
	// "keys_stale", "version_unsupported".
	TokenDecrypt(reason string)
}

// NewUID2Config returns a Config pre-populated with the UID2 identity
// scope and default UID2 operator URL. Callers still need to supply
// apiKey and clientSecret and may override any other field.
func NewUID2Config(apiKey, clientSecret string) Config {
	return Config{
		OperatorURL:   DefaultUID2OperatorURL,
		APIKey:        apiKey,
		ClientSecret:  clientSecret,
		IdentityScope: ScopeUID2,
	}
}

// NewEUIDConfig returns a Config pre-populated with the EUID identity
// scope and default EUID operator URL.
func NewEUIDConfig(apiKey, clientSecret string) Config {
	return Config{
		OperatorURL:   DefaultEUIDOperatorURL,
		APIKey:        apiKey,
		ClientSecret:  clientSecret,
		IdentityScope: ScopeEUID,
	}
}

// Client is a UID2 or EUID operator client. Construct with [New]; call
// [Client.Decrypt] to convert a token into its raw identity bytes. Safe
// for concurrent use.
type Client struct {
	operatorURL     string
	apiKey          string
	secret          []byte
	scope           IdentityScope
	httpClient      *http.Client
	refreshInterval time.Duration

	// store is the current keyset. Swapped atomically as a whole on
	// refresh; readers hold the pointer they load for the duration of
	// one Decrypt call, avoiding read locks on the hot path.
	store atomic.Pointer[keyStore]

	recorder Recorder
	logger   *slog.Logger

	// now returns the current time; overridable in tests for determinism
	// on the expiry and lifetime checks. Production callers never touch
	// this; keeping it as a field rather than package-level avoids the
	// "no package-level state" guardrail.
	now func() time.Time
}

// resolvedConfig is the input Config after defaults are applied. Kept
// distinct from Config so a caller that inspects Config later sees the
// values they passed in, not the defaults we substituted.
type resolvedConfig struct {
	operatorURL     string
	apiKey          string
	secret          []byte
	scope           IdentityScope
	httpClient      *http.Client
	refreshInterval time.Duration
	recorder        Recorder
	logger          *slog.Logger
}

func (cfg Config) resolve() (resolvedConfig, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return resolvedConfig{}, errors.New("uid2client: APIKey is required")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return resolvedConfig{}, errors.New("uid2client: ClientSecret is required")
	}
	secret, err := base64.StdEncoding.DecodeString(cfg.ClientSecret)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("uid2client: ClientSecret must be base64-encoded: %w", err)
	}
	if len(secret) != 32 {
		return resolvedConfig{}, fmt.Errorf("uid2client: ClientSecret must decode to 32 bytes, got %d", len(secret))
	}

	operatorURL := strings.TrimRight(cfg.OperatorURL, "/")
	if operatorURL == "" {
		switch cfg.IdentityScope {
		case ScopeUID2:
			operatorURL = DefaultUID2OperatorURL
		case ScopeEUID:
			operatorURL = DefaultEUIDOperatorURL
		default:
			return resolvedConfig{}, fmt.Errorf("uid2client: unknown IdentityScope %d", cfg.IdentityScope)
		}
	}
	if !strings.HasPrefix(operatorURL, "http://") && !strings.HasPrefix(operatorURL, "https://") {
		return resolvedConfig{}, fmt.Errorf("uid2client: OperatorURL %q must include http:// or https://", operatorURL)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.HTTPTimeout
		if timeout <= 0 {
			timeout = defaultHTTPTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	refreshInterval := cfg.KeyRefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = defaultKeyRefreshInterval
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return resolvedConfig{
		operatorURL:     operatorURL,
		apiKey:          strings.TrimSpace(cfg.APIKey),
		secret:          secret,
		scope:           cfg.IdentityScope,
		httpClient:      httpClient,
		refreshInterval: refreshInterval,
		recorder:        cfg.Recorder,
		logger:          logger,
	}, nil
}

// New constructs a Client, performs the initial synchronous key refresh,
// and starts the background refresh goroutine bound to lifetimeCtx.
// Cancelling lifetimeCtx during shutdown drains the goroutine.
//
// New returns an error if:
//   - Config validation fails (missing APIKey/ClientSecret, bad base64).
//   - The initial key refresh HTTP call fails.
//   - The operator's response does not decrypt / does not match the
//     configured IdentityScope.
//
// It does NOT retry the initial refresh; the caller controls startup
// policy. Post-startup refresh failures are logged and retried on the
// background goroutine's schedule; token decrypt continues to use the
// last known-good keyset until it fully expires.
func New(lifetimeCtx context.Context, cfg Config) (*Client, error) {
	resolved, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	c := &Client{
		operatorURL:     resolved.operatorURL,
		apiKey:          resolved.apiKey,
		secret:          resolved.secret,
		scope:           resolved.scope,
		httpClient:      resolved.httpClient,
		refreshInterval: resolved.refreshInterval,
		recorder:        resolved.recorder,
		logger:          resolved.logger,
		now:             time.Now,
	}

	initialCtx, cancel := context.WithTimeout(lifetimeCtx, c.httpTimeoutForInitial())
	defer cancel()
	if err := c.refreshOnce(initialCtx); err != nil {
		return nil, fmt.Errorf("uid2client: initial key refresh: %w", err)
	}

	go c.runRefresh(lifetimeCtx)
	return c, nil
}

// httpTimeoutForInitial derives a timeout for the initial synchronous
// refresh. When the caller passes their own http.Client we don't know its
// timeout, so we fall back to the default HTTP timeout — a bounded upper
// limit that keeps New from blocking indefinitely if the operator is
// unreachable.
func (c *Client) httpTimeoutForInitial() time.Duration {
	if c.httpClient.Timeout > 0 {
		return c.httpClient.Timeout
	}
	return defaultHTTPTimeout
}

// Decrypt decodes and decrypts a UID2 or EUID advertising token, returning
// the raw identity bytes (typically 32 bytes). The token string is
// consumed byte-for-byte — do NOT lowercase or otherwise normalize it
// upstream (the base64 alphabet is case-sensitive).
//
// Errors are the ErrXxx sentinels declared in errors.go. Callers wrap
// this with a decoder that maps ErrOptedOut / ErrTokenExpired /
// ErrKeyNotFound / ErrInvalidToken to whatever "drop this identity"
// signal their pipeline uses.
func (c *Client) Decrypt(ctx context.Context, token string) ([]byte, error) {
	// Local crypto — no HTTP — so the fast path skips the ctx check.
	// Callers that pass a cancelled ctx still see the cancellation on
	// the way in.
	if err := ctx.Err(); err != nil {
		c.record("cancelled")
		return nil, err
	}
	store := c.store.Load()
	raw, err := decryptToken(store, c.scope, token, c.now())
	c.record(reasonFor(err))
	return raw, err
}

// Refresh performs a single synchronous key refresh. Exposed for tests and
// for operators who want to trigger a refresh out-of-band (e.g. after
// receiving a "key rotated" push). Not required for normal use; the
// background goroutine calls this on its own schedule.
func (c *Client) Refresh(ctx context.Context) error {
	return c.refreshOnce(ctx)
}

// runRefresh is the background refresh loop. It ticks at refreshInterval;
// on failure it logs, notifies the recorder, and continues on schedule.
// A permanently-broken operator therefore produces one log line and one
// counter increment per interval, not a tight retry burst.
func (c *Client) runRefresh(ctx context.Context) {
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Recover per-tick so a panic in one refresh (JSON bug,
			// mid-refactor nil-deref, etc.) doesn't kill the loop and
			// leave the client serving stale keys until latestExpiry.
			// A permanently-broken refresh therefore produces one log
			// line + one counter increment per tick, which is loud
			// enough for operators to see.
			c.refreshWithRecover(ctx)
		}
	}
}

// refreshWithRecover runs one refresh with a per-tick panic recover so the
// loop keeps ticking after any single-refresh failure. Bounds the HTTP
// call to httpTimeoutForInitial so a slow operator can't stall the next
// tick.
func (c *Client) refreshWithRecover(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("uid2client: refresh panicked",
				"scope", c.scope.String(),
				"panic", r)
			if c.recorder != nil {
				c.recorder.KeyRefresh(fmt.Errorf("uid2client: refresh panicked: %v", r))
			}
		}
	}()
	refreshCtx, cancel := context.WithTimeout(ctx, c.httpTimeoutForInitial())
	defer cancel()
	if err := c.refreshOnce(refreshCtx); err != nil {
		c.logger.Warn("uid2client: background key refresh failed",
			"scope", c.scope.String(),
			"error", err)
	}
}

// refreshOnce performs one round-trip against /v2/key/bidstream and
// swaps the resulting keyset in as the current store on success. On
// failure the current store is left unchanged; a stale store is
// preferable to a null store — Decrypt continues to service tokens
// against the last known-good keys until their expiry.
func (c *Client) refreshOnce(ctx context.Context) error {
	envelope, nonce, err := sealRequestEnvelope(c.secret, nil, c.now())
	if err != nil {
		c.notifyRefresh(err)
		return err
	}

	url := c.operatorURL + keyRefreshPath
	body := base64.StdEncoding.EncodeToString(envelope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		c.notifyRefresh(err)
		return fmt.Errorf("uid2client: build refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.notifyRefresh(err)
		return fmt.Errorf("uid2client: post %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.notifyRefresh(err)
		return fmt.Errorf("uid2client: read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Do NOT include the response body in the error/log: a
		// misbehaving operator could echo the Authorization header
		// (bearer token) or client secret back to us, which would then
		// land in error logs. Status alone is enough for triage; a
		// deeper look means running the operator's own dashboards.
		wrapped := fmt.Errorf("uid2client: refresh returned HTTP %d", resp.StatusCode)
		c.notifyRefresh(wrapped)
		return wrapped
	}

	plain, err := openResponseEnvelope(c.secret, string(responseBody), nonce)
	if err != nil {
		c.notifyRefresh(err)
		return err
	}
	store, err := parseKeyRefreshResponse(plain, c.scope)
	if err != nil {
		c.notifyRefresh(err)
		return err
	}
	if store.scope != c.scope {
		wrapped := fmt.Errorf("uid2client: operator advertises scope %s but client is configured for %s",
			store.scope, c.scope)
		c.notifyRefresh(wrapped)
		return wrapped
	}
	c.store.Store(store)
	c.notifyRefresh(nil)
	return nil
}

func (c *Client) notifyRefresh(err error) {
	if c.recorder == nil {
		return
	}
	c.recorder.KeyRefresh(err)
}

func (c *Client) record(reason string) {
	if c.recorder == nil {
		return
	}
	c.recorder.TokenDecrypt(reason)
}

// reasonFor maps a decrypt result error to a compact counter-label. Kept
// switch-based rather than errors.As-per-err so a future error type gets
// a compile-time nudge to update this mapping.
func reasonFor(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrInvalidToken):
		return "invalid"
	case errors.Is(err, ErrTokenExpired):
		return "expired"
	case errors.Is(err, ErrKeyNotFound):
		return "key_not_found"
	case errors.Is(err, ErrScopeMismatch):
		return "scope_mismatch"
	case errors.Is(err, ErrKeysStale):
		return "keys_stale"
	case errors.Is(err, ErrVersionUnsupported):
		return "version_unsupported"
	case errors.Is(err, ErrNotInitialized):
		return "not_initialized"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "unknown"
	}
}
