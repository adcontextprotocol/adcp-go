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
	baseURL     *url.URL
	bearerToken string
	client      *http.Client
	logger      *slog.Logger

	positiveTTL time.Duration
	negativeTTL time.Duration

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

	// Logger receives cache-miss and fetch-error events.
	Logger *slog.Logger
}

type agentCacheEntry struct {
	// byKid is the flattened kid → SigningKey map for this agent across
	// all authorizations the registry returned. Nil for a negative
	// cache entry.
	byKid map[string]*SigningKey
	// expires is when this entry becomes stale.
	expires time.Time
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
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       3 * time.Second,
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
	return &LazyAuthorizationKeyStore{
		baseURL:     parsed,
		bearerToken: opts.BearerToken,
		client:      client,
		logger:      logger,
		positiveTTL: positiveTTL,
		negativeTTL: negativeTTL,
		cache:       cache,
		fetchSem:    make(chan struct{}, maxConcurrent),
		inflight:    make(map[string]*inflightFetch),
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
	canonical, err := urlcanon.Canonicalize(sellerAgentURL)
	if err != nil {
		s.logger.Debug("agent URL failed canonicalization; treating as unknown", "seller_agent_url", sellerAgentURL, "error", err)
		return nil, false
	}
	entry := s.entryFor(canonical)
	if entry == nil || entry.byKid == nil {
		return nil, false
	}
	k, ok := entry.byKid[kid]
	return k, ok
}

// Invalidate drops the cached entry for the given agent URL so the next
// lookup triggers a fresh fetch. The current verify middleware does not
// call this on signature failure — a rotation therefore takes up to the
// positive TTL (default 5 minutes) to propagate. Exported for host code
// that wants to wire re-fetch-on-failure per spec §590.
func (s *LazyAuthorizationKeyStore) Invalidate(sellerAgentURL string) {
	canonical, err := urlcanon.Canonicalize(sellerAgentURL)
	if err != nil {
		return
	}
	s.cache.Remove(canonical)
}

// entryFor returns a cached entry for the canonical URL, fetching if
// missing or expired. Concurrent misses for the same URL are collapsed.
func (s *LazyAuthorizationKeyStore) entryFor(canonical string) *agentCacheEntry {
	if entry, ok := s.cache.Get(canonical); ok && time.Now().Before(entry.expires) {
		return entry
	}

	// Single-flight the fetch so a burst of first requests for a new
	// agent produces one HTTP call, not N.
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

	// Non-blocking semaphore acquire — if concurrent fetches are already
	// at the ceiling, drop this request fail-closed rather than queuing.
	// A queue is the amplifier we're guarding against; the caller's
	// verify returns ErrSignatureKeyUnknown (401), which is the safe
	// outcome under load.
	select {
	case s.fetchSem <- struct{}{}:
	default:
		in.err = errors.New("fetch semaphore full")
		in.wg.Done()
		s.inflightMu.Lock()
		delete(s.inflight, canonical)
		s.inflightMu.Unlock()
		s.logger.Warn("authorization keystore refused fetch — concurrency ceiling reached", "seller_agent_url", canonical)
		return nil
	}

	entry, err := s.fetch(canonical)
	<-s.fetchSem
	in.entry = entry
	in.err = err
	in.wg.Done()

	s.inflightMu.Lock()
	delete(s.inflight, canonical)
	s.inflightMu.Unlock()

	if err != nil {
		s.logger.Warn("authorization keystore fetch failed", "seller_agent_url", canonical, "error", err)
		return nil
	}

	s.cache.Add(canonical, entry)
	return entry
}

// authorizationsResponse mirrors the JSON envelope of
// GET /api/registry/authorizations. Only the fields the keystore needs
// are decoded — extension fields are ignored.
type authorizationsResponse struct {
	Rows []struct {
		SigningKeys []SigningKey `json:"signing_keys,omitempty"`
	} `json:"rows"`
}

func (s *LazyAuthorizationKeyStore) fetch(canonicalAgentURL string) (*agentCacheEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil) //nolint:gosec
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

	// 404 is a legitimate "no authorizations exist for this agent" —
	// cache negatively so a burst of forged requests does not pound
	// the registry.
	if resp.StatusCode == http.StatusNotFound {
		return &agentCacheEntry{expires: time.Now().Add(s.negativeTTL)}, nil
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
		return &agentCacheEntry{expires: time.Now().Add(s.negativeTTL)}, nil
	}
	return &agentCacheEntry{
		byKid:   byKid,
		expires: time.Now().Add(s.positiveTTL),
	}, nil
}
