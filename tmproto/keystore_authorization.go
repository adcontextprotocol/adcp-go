package tmproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/urlcanon"
	lru "github.com/hashicorp/golang-lru/v2"
)

// LazyAuthorizationKeyStore is an AgentAwareKeyStore backed by an AdCP
// registry authorizations endpoint (typically
// https://agenticadvertising.org/api/registry/authorizations). Instead of
// syncing every publisher's keys at startup, it fetches on demand for the
// exact seller_agent_url a request is signed by and caches per agent.
//
// Fits the deployment where the verifier does not know its callers ahead
// of time and where the caller set is small relative to the global
// registry: only agents that actually send traffic ever land in the cache.
//
// Trust anchor per the TMP spec (specification.mdx §601-603): the registry
// materializes each publisher's adagents.json into per-(agent, publisher)
// authorization rows with signing_keys[]. The endpoint canonicalizes
// agent_url server-side; this store canonicalizes client-side too so cache
// keys are stable across callers that submit non-canonical URLs.
//
// Availability-hardening: verification runs before the signature is
// checked (the verifier needs the key to check it), so LookupKeyForAgent
// operates on attacker-controllable input. An attacker spraying signed-
// shaped requests with unique `seller_agent_url` values would otherwise
// (a) miss the cache on every request, amplifying to the registry 1:1,
// and (b) fill the cache with negative entries indefinitely. Two
// bounds mitigate both:
//   - the cache is an LRU with a size cap — attacker garbage evicts
//     itself before displacing legitimate agents (whose entries get
//     touched on every legitimate verify);
//   - a fetch semaphore caps concurrent outbound registry calls, so a
//     spray cannot fan out to unbounded HTTP concurrency. Excess
//     concurrent misses fail closed (the extra request 401s) rather
//     than queue — a queue is the amplifier.
type LazyAuthorizationKeyStore struct {
	// baseURL is parsed once at construction. Per-request URLs are built
	// by cloning it and setting the `agent_url` query parameter — the
	// host is never derived from caller input, only the query string is.
	baseURL      *url.URL
	bearerToken  string
	client       *http.Client
	logger       *slog.Logger
	fetchTimeout time.Duration

	positiveTTL               time.Duration
	negativeTTL               time.Duration
	serveStaleGrace           time.Duration
	unknownKidRefetchCooldown time.Duration

	// cache is bounded by size; expiry is checked at read time. LRU
	// promotion happens on Get, so entries for agents that keep signing
	// stay hot while attacker sprays age out.
	cache *lru.Cache[string, *agentCacheEntry]

	// fetchSem caps concurrent outbound registry calls. A non-blocking
	// acquire — the point is to prevent amplification, not to enqueue
	// work under load.
	fetchSem chan struct{}

	// singleflight collapses concurrent misses for the same agent URL onto
	// one HTTP call.
	inflightMu sync.Mutex
	inflight   map[string]*inflightFetch
}

// LazyAuthorizationKeyStoreOptions configures a LazyAuthorizationKeyStore.
type LazyAuthorizationKeyStoreOptions struct {
	// BaseURL is the authorizations endpoint. `?agent_url=<canonical>` is
	// appended per request. Example:
	//   https://agenticadvertising.org/api/registry/authorizations
	// Must use https:// unless AllowInsecureScheme is true.
	BaseURL string

	// BearerToken is sent as `Authorization: Bearer <token>` on every
	// request. Optional — leave empty when the endpoint permits
	// unauthenticated reads.
	BearerToken string

	// AllowInsecureScheme permits http:// URLs. For local development only.
	AllowInsecureScheme bool

	// HTTPClient is the client used for authorization fetches. When nil, a
	// 3-second client is constructed with cross-origin redirects denied.
	// The default matches the spec's ~30μs verification target — a slow
	// registry should never block the auction hot path indefinitely.
	HTTPClient *http.Client

	// PositiveTTL is how long a resolved agent's keys are cached before
	// re-fetch. Defaults to 5 minutes (spec §579 recommended TTL).
	PositiveTTL time.Duration

	// NegativeTTL is how long a "no authorization rows" answer is cached
	// to avoid pounding the registry on unknown callers. Defaults to 60
	// seconds — short enough that a genuine new authorization is picked
	// up quickly, long enough to absorb a burst of forged requests.
	NegativeTTL time.Duration

	// MaxCacheEntries caps the number of agents kept in the LRU cache.
	// An attacker spraying unique seller_agent_url values cannot grow
	// the cache past this bound — legitimate agents' entries stay hot
	// because every legitimate verify touches them, while the attacker's
	// unique-URL entries age out of the tail. Defaults to 4096, which
	// comfortably covers any realistic per-verifier caller set while
	// being small enough that steady-state memory is trivially bounded.
	MaxCacheEntries int

	// MaxConcurrentFetches caps outbound registry calls in flight. A
	// spray of unique agent URLs cannot fan out beyond this bound —
	// excess concurrent misses fail closed (return no key → the request
	// 401s) rather than queue. Defaults to 32, high enough for
	// legitimate cold-start bursts, low enough to bound the amplifier.
	MaxConcurrentFetches int

	// FetchTimeout bounds a single outbound registry call. Defaults to
	// 3 seconds. Set alongside HTTPClient's own Timeout — whichever is
	// tighter wins. An operator-supplied HTTPClient with no Timeout
	// still gets this ceiling.
	FetchTimeout time.Duration

	// UnknownKidRefetchCooldown gates how soon after a fetch the store
	// will retry the registry when the incoming kid isn't in the cached
	// keyset (typical cause: publisher rotation). Defaults to 30 s.
	// Shorter values pick up rotations faster; longer values further
	// dampen the impact of forged-kid probes. Exposed primarily so
	// tests can shorten it without racing wall-clock time.
	UnknownKidRefetchCooldown time.Duration

	// ServeStaleGrace lets an expired positive entry keep serving for
	// this window when a refetch fails (timeout / 5xx / network blip).
	// Zero disables the grace — expiry means immediate 401 for that
	// agent until the next successful fetch. A non-zero value trades a
	// bounded revocation window (extends the effective positive TTL by
	// up to Grace on failure) for continuity of service during a
	// registry outage. Snapshot-mode RemoteKeyStore keeps serving its
	// last snapshot indefinitely across refresh failures; this bounds
	// that behaviour explicitly.
	ServeStaleGrace time.Duration

	// Logger receives cache-miss and fetch-error events.
	Logger *slog.Logger
}

type agentCacheEntry struct {
	// byKid is the flattened kid → SigningKey map for this agent across
	// all authorizations the registry returned. Nil for a negative
	// cache entry.
	byKid map[string]*SigningKey
	// fetchedAt is when this entry was populated by a successful
	// registry response. Used to gate refetch-on-unknown-kid so a
	// freshly-fetched entry does not thrash on a spray of unknown kids
	// from the same agent.
	fetchedAt time.Time
	// expires is when this entry becomes stale.
	expires time.Time
	// staleUntil is the wall-clock cutoff beyond which even
	// serve-stale-on-error refuses to serve. Zero when serve-stale is
	// disabled (returns nil on expiry+refetch-failure).
	staleUntil time.Time
}

// defaultUnknownKidRefetchCooldown gates the "refetch on unknown kid"
// fallback: once an entry is younger than this window, we trust the
// freshly-cached keyset and do not refetch even if the incoming kid
// isn't in it. Long enough to absorb a spray of forged kids from one
// agent; short enough that a legitimate rotation lands quickly.
const defaultUnknownKidRefetchCooldown = 30 * time.Second

// refetchOnUnknownKidAllowed reports whether the entry is old enough to
// justify one more refetch attempt on an unknown kid. Semantics: a
// brand-new entry answers false (still fresh, trust the keyset); an
// entry past the cooldown answers true. The kid argument is reserved
// for a future per-kid tracking scheme; today the gate is entry-global.
func (e *agentCacheEntry) refetchOnUnknownKidAllowed(_ string, cooldown time.Duration) bool {
	return time.Since(e.fetchedAt) >= cooldown
}

type inflightFetch struct {
	wg    sync.WaitGroup
	entry *agentCacheEntry
	err   error
}

// MaxAuthorizationBodyBytes caps the per-agent authorization response
// this store will ingest. Sized generously for hundreds of authorization
// rows per agent (typical: single-digit); a runaway response cannot
// exhaust the verifier's memory.
const MaxAuthorizationBodyBytes = 1 * 1024 * 1024

// NewLazyAuthorizationKeyStore constructs the store. It does not fetch
// anything until LookupKeyForAgent is called for the first time —
// entries populate lazily on demand.
func NewLazyAuthorizationKeyStore(opts LazyAuthorizationKeyStoreOptions) (*LazyAuthorizationKeyStore, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("tmproto: LazyAuthorizationKeyStore BaseURL is required")
	}
	parsed, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("tmproto: LazyAuthorizationKeyStore BaseURL invalid: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		// fine.
	case "http":
		if !opts.AllowInsecureScheme {
			return nil, errors.New("tmproto: LazyAuthorizationKeyStore BaseURL must use https:// (set AllowInsecureScheme for local development)")
		}
	default:
		return nil, fmt.Errorf("tmproto: LazyAuthorizationKeyStore BaseURL must use http(s) scheme, got %q", parsed.Scheme)
	}
	fetchTimeout := opts.FetchTimeout
	if fetchTimeout <= 0 {
		fetchTimeout = 3 * time.Second
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       fetchTimeout,
			CheckRedirect: denyCrossOriginRedirect,
		}
	}
	positiveTTL := opts.PositiveTTL
	if positiveTTL <= 0 {
		positiveTTL = 5 * time.Minute
	}
	negativeTTL := opts.NegativeTTL
	if negativeTTL <= 0 {
		negativeTTL = 60 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxEntries := opts.MaxCacheEntries
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	cache, err := lru.New[string, *agentCacheEntry](maxEntries)
	if err != nil {
		return nil, fmt.Errorf("tmproto: LazyAuthorizationKeyStore cache: %w", err)
	}
	maxConcurrent := opts.MaxConcurrentFetches
	if maxConcurrent <= 0 {
		maxConcurrent = 32
	}
	unknownKidCooldown := opts.UnknownKidRefetchCooldown
	if unknownKidCooldown <= 0 {
		unknownKidCooldown = defaultUnknownKidRefetchCooldown
	}
	return &LazyAuthorizationKeyStore{
		baseURL:                   parsed,
		bearerToken:               opts.BearerToken,
		client:                    client,
		logger:                    logger,
		fetchTimeout:              fetchTimeout,
		positiveTTL:               positiveTTL,
		negativeTTL:               negativeTTL,
		serveStaleGrace:           opts.ServeStaleGrace,
		unknownKidRefetchCooldown: unknownKidCooldown,
		cache:                     cache,
		fetchSem:                  make(chan struct{}, maxConcurrent),
		inflight:                  make(map[string]*inflightFetch),
	}, nil
}

// LookupKey implements KeyStore but always returns false. This store is
// exclusively agent-scoped: verifying a kid without knowing which agent
// claimed it would defeat the per-agent isolation the AgentAwareKeyStore
// interface exists to preserve (see AgentAwareKeyStore godoc). Callers
// on the signing path always have req.SellerAgentURL and go through
// LookupKeyForAgent; a caller that hits this branch is on a misconfigured
// path and should fail closed.
func (s *LazyAuthorizationKeyStore) LookupKey(_ string) (*SigningKey, bool) {
	return nil, false
}

// LookupKeyForAgent implements AgentAwareKeyStore.
func (s *LazyAuthorizationKeyStore) LookupKeyForAgent(kid, sellerAgentURL string) (*SigningKey, bool) {
	return s.LookupKeyForAgentCtx(context.Background(), kid, sellerAgentURL)
}

// LookupKeyForAgentCtx is the context-aware variant. Callers on a
// deadline (verify middleware inside a request handler) SHOULD use this
// so a slow registry can't couple the agent's availability to it — the
// context deadline caps how long the store will spend on a cold-miss
// fetch. The interface method LookupKeyForAgent stays context-less to
// match the KeyStore family.
func (s *LazyAuthorizationKeyStore) LookupKeyForAgentCtx(ctx context.Context, kid, sellerAgentURL string) (*SigningKey, bool) {
	canonical, err := urlcanon.Canonicalize(sellerAgentURL)
	if err != nil {
		s.logger.Debug("agent URL failed canonicalization; treating as unknown", "seller_agent_url", sellerAgentURL, "error", err)
		return nil, false
	}
	entry := s.entryFor(ctx, canonical)
	if entry == nil || entry.byKid == nil {
		return nil, false
	}
	if k, ok := entry.byKid[kid]; ok {
		return k, true
	}
	// Kid isn't in the cached keyset. The most common cause is a
	// legitimate signer that rotated to a new kid after we cached this
	// agent — the registry has the new key, we're serving stale. Try to
	// refetch once (bounded by a short cooldown to dampen forged-kid
	// probes). CRUCIAL: fetch-then-swap. We do NOT evict the existing
	// entry first, because an attacker with a bogus kid could otherwise
	// force a refetch that fails (registry blip, semaphore saturation
	// from a spray of unique agent URLs) and cancel the ServeStaleGrace
	// net we hold for legitimate traffic. On refetch failure the old
	// entry stays exactly where it was.
	if entry.refetchOnUnknownKidAllowed(kid, s.unknownKidRefetchCooldown) {
		if fresh := s.refetchAgent(ctx, canonical); fresh != nil && fresh.byKid != nil {
			if k, ok := fresh.byKid[kid]; ok {
				return k, true
			}
		}
	}
	return nil, false
}

// refetchAgent forces a registry call for the agent, updating the cache
// only on success. Unlike entryFor, it bypasses the "cache still valid"
// short-circuit — the caller has already established that the cached
// entry needs refresh — but preserves single-flight and the fetch
// semaphore. On failure it returns nil WITHOUT touching the existing
// cache entry, so a still-valid entry (with respect to the caller's
// original expectation) is preserved.
func (s *LazyAuthorizationKeyStore) refetchAgent(ctx context.Context, canonical string) *agentCacheEntry {
	s.inflightMu.Lock()
	if in, ok := s.inflight[canonical]; ok {
		s.inflightMu.Unlock()
		in.wg.Wait()
		if in.err != nil {
			return nil
		}
		return in.entry
	}
	in := &inflightFetch{}
	in.wg.Add(1)
	s.inflight[canonical] = in
	s.inflightMu.Unlock()

	semAcquired := false
	defer func() {
		if semAcquired {
			<-s.fetchSem
		}
		in.wg.Done()
		s.inflightMu.Lock()
		delete(s.inflight, canonical)
		s.inflightMu.Unlock()
	}()

	select {
	case s.fetchSem <- struct{}{}:
		semAcquired = true
	default:
		in.err = errors.New("fetch semaphore full")
		return nil
	}

	entry, err := s.fetch(ctx, canonical)
	in.entry = entry
	in.err = err
	if err != nil {
		return nil
	}
	s.cache.Add(canonical, entry)
	return entry
}

// Invalidate drops the cached entry for the given agent URL so the next
// lookup triggers a fresh fetch. The current verify middleware does not
// call this on signature failure — a rotation therefore takes up to the
// positive TTL (default 5 minutes) to propagate. Exported for host code
// that wants to wire re-fetch-on-failure per spec §590.
//
// Race note: an in-flight leader fetch that completes AFTER Invalidate
// will Add its result back into the cache, re-inserting the entry the
// caller just wanted gone. Callers wiring §590 refetch-on-failure
// SHOULD accept that as best-effort — a stale entry surviving one extra
// round is bounded by the positive TTL either way.
func (s *LazyAuthorizationKeyStore) Invalidate(sellerAgentURL string) {
	canonical, err := urlcanon.Canonicalize(sellerAgentURL)
	if err != nil {
		return
	}
	s.cache.Remove(canonical)
}

// entryFor returns a cached entry for the canonical URL, fetching if
// missing or expired. Concurrent misses for the same URL are collapsed.
// Callers pass a context so the fetch inherits the request's deadline
// rather than pinning a semaphore slot for the full FetchTimeout.
func (s *LazyAuthorizationKeyStore) entryFor(ctx context.Context, canonical string) *agentCacheEntry {
	cached, hasCached := s.cache.Get(canonical)
	if hasCached && time.Now().Before(cached.expires) {
		return cached
	}

	// Single-flight the fetch so a burst of first requests for a new
	// agent produces one HTTP call, not N.
	s.inflightMu.Lock()
	if in, ok := s.inflight[canonical]; ok {
		s.inflightMu.Unlock()
		in.wg.Wait()
		if in.err != nil {
			return s.staleOrNil(cached, hasCached)
		}
		return in.entry
	}
	in := &inflightFetch{}
	in.wg.Add(1)
	s.inflight[canonical] = in
	s.inflightMu.Unlock()

	// Everything past this point must run even on panic — otherwise
	// waiters block forever on wg.Wait(), the inflight entry leaks, and
	// a semaphore slot is lost. defer covers all three legs.
	semAcquired := false
	defer func() {
		if semAcquired {
			<-s.fetchSem
		}
		in.wg.Done()
		s.inflightMu.Lock()
		delete(s.inflight, canonical)
		s.inflightMu.Unlock()
	}()

	// Non-blocking semaphore acquire — if concurrent fetches are already
	// at the ceiling, drop this request fail-closed rather than queuing.
	// A queue is the amplifier we're guarding against; the caller's
	// verify returns ErrSignatureKeyUnknown (401), which is the safe
	// outcome under load.
	select {
	case s.fetchSem <- struct{}{}:
		semAcquired = true
	default:
		in.err = errors.New("fetch semaphore full")
		s.logger.Warn("authorization keystore refused fetch — concurrency ceiling reached", "seller_agent_url", canonical)
		return s.staleOrNil(cached, hasCached)
	}

	entry, err := s.fetch(ctx, canonical)
	in.entry = entry
	in.err = err

	if err != nil {
		s.logger.Warn("authorization keystore fetch failed", "seller_agent_url", canonical, "error", err)
		return s.staleOrNil(cached, hasCached)
	}

	s.cache.Add(canonical, entry)
	return entry
}

// staleOrNil returns a still-valid stale entry when serve-stale grace is
// configured and the cached entry has not yet passed its staleUntil, else
// nil. Trades a bounded revocation window for continuity across registry
// blips.
func (s *LazyAuthorizationKeyStore) staleOrNil(cached *agentCacheEntry, hasCached bool) *agentCacheEntry {
	if !hasCached || s.serveStaleGrace <= 0 {
		return nil
	}
	if cached == nil || cached.byKid == nil {
		return nil
	}
	if time.Now().Before(cached.staleUntil) {
		return cached
	}
	return nil
}

// authorizationsResponse mirrors the JSON envelope of
// GET /api/registry/authorizations. Only the fields the keystore needs
// are decoded — extension fields are ignored.
type authorizationsResponse struct {
	Rows []struct {
		SigningKeys []SigningKey `json:"signing_keys,omitempty"`
	} `json:"rows"`
}

func (s *LazyAuthorizationKeyStore) fetch(ctx context.Context, canonicalAgentURL string) (*agentCacheEntry, error) {
	// Cap the fetch by the store's FetchTimeout, but only tighten the
	// caller's deadline — never extend it. This lets a request-scoped
	// context cancel a slow registry, while still bounding an
	// unbounded (background) caller to FetchTimeout.
	fetchCtx, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()

	// Clone the pre-parsed base URL and set the query param. The host is
	// fixed at construction; only the `agent_url` query param carries
	// user-derived input, so a hostile seller_agent_url cannot redirect
	// the request to a different origin.
	u := *s.baseURL
	q := u.Query()
	q.Set("agent_url", canonicalAgentURL)
	u.RawQuery = q.Encode()

	// gosec G107 (SSRF): the URL is fixed at construction time from operator
	// configuration (TMP_REGISTRY_URL); only the ?agent_url= query parameter
	// is derived from request data, which is the documented registry
	// contract. The Client is not a generic HTTP fetcher.
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u.String(), nil) //nolint:gosec
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	}

	resp, err := s.client.Do(req) //nolint:gosec // see G107 justification above
	if err != nil {
		return nil, fmt.Errorf("fetch authorizations: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	now := time.Now()

	// 404 is a legitimate "no authorizations exist for this agent" —
	// cache negatively so a burst of forged requests does not pound
	// the registry.
	if resp.StatusCode == http.StatusNotFound {
		return &agentCacheEntry{
			fetchedAt: now,
			expires:   now.Add(s.negativeTTL),
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authorizations returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxAuthorizationBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read authorizations: %w", err)
	}
	var parsed authorizationsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse authorizations: %w", err)
	}

	byKid := make(map[string]*SigningKey)
	for _, row := range parsed.Rows {
		for i := range row.SigningKeys {
			k := row.SigningKeys[i]
			if k.Kid == "" {
				continue
			}
			byKid[k.Kid] = &k
		}
	}

	// An agent that exists in the registry but has zero signing keys
	// (e.g. mid-onboarding, before adagents.json pins keys) is also a
	// negative result from the verifier's point of view — but cache
	// briefly so the registry is not hammered.
	if len(byKid) == 0 {
		return &agentCacheEntry{
			fetchedAt: now,
			expires:   now.Add(s.negativeTTL),
		}, nil
	}
	entry := &agentCacheEntry{
		byKid:     byKid,
		fetchedAt: now,
		expires:   now.Add(s.positiveTTL),
	}
	if s.serveStaleGrace > 0 {
		entry.staleUntil = entry.expires.Add(s.serveStaleGrace)
	}
	return entry, nil
}
