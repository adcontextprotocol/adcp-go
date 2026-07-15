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
)

// LazyAuthorizationKeyStore is an AgentAwareKeyStore backed by an AdCP
// registry authorizations endpoint (typically
// https://agenticadvertising.org/api/registry/authorizations). Instead of
// syncing every publisher's keys at startup, it fetches on demand for the
// exact seller_agent_url a request is signed by, caches per agent, and
// invalidates on TTL expiry or verification failure.
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
type LazyAuthorizationKeyStore struct {
	baseURL     string
	bearerToken string
	client      *http.Client
	logger      *slog.Logger

	positiveTTL time.Duration
	negativeTTL time.Duration

	mu    sync.RWMutex
	byURL map[string]*agentCacheEntry

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
// anything until LookupKeyForAgent is called for the first time — Run is
// optional and only useful when the verifier wants a background timer to
// prune stale entries.
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
	return &LazyAuthorizationKeyStore{
		baseURL:     opts.BaseURL,
		bearerToken: opts.BearerToken,
		client:      client,
		logger:      logger,
		positiveTTL: positiveTTL,
		negativeTTL: negativeTTL,
		byURL:       make(map[string]*agentCacheEntry),
		inflight:    make(map[string]*inflightFetch),
	}, nil
}

// LookupKey implements KeyStore. Without a seller_agent_url the store
// falls back to a linear scan of the cache — this is only useful for
// callers that have not yet been updated to pass agent context. Verifiers
// on the current signing path always have req.SellerAgentURL and take the
// LookupKeyForAgent path instead.
func (s *LazyAuthorizationKeyStore) LookupKey(kid string) (*SigningKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, entry := range s.byURL {
		if entry == nil || entry.byKid == nil {
			continue
		}
		if now.After(entry.expires) {
			continue
		}
		if k, ok := entry.byKid[kid]; ok {
			return k, true
		}
	}
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
// lookup triggers a fresh fetch. Callers should Invalidate after a
// signature verification failure — the spec (§590) requires re-fetching
// on failure to pick up a rotation.
func (s *LazyAuthorizationKeyStore) Invalidate(sellerAgentURL string) {
	canonical, err := urlcanon.Canonicalize(sellerAgentURL)
	if err != nil {
		return
	}
	s.mu.Lock()
	delete(s.byURL, canonical)
	s.mu.Unlock()
}

// entryFor returns a cached entry for the canonical URL, fetching if
// missing or expired. Concurrent misses for the same URL are collapsed.
func (s *LazyAuthorizationKeyStore) entryFor(canonical string) *agentCacheEntry {
	s.mu.RLock()
	entry := s.byURL[canonical]
	s.mu.RUnlock()
	if entry != nil && time.Now().Before(entry.expires) {
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

	entry, err := s.fetch(canonical)
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

	s.mu.Lock()
	s.byURL[canonical] = entry
	s.mu.Unlock()
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

	q := url.Values{}
	q.Set("agent_url", canonicalAgentURL)
	sep := "?"
	if strings.Contains(s.baseURL, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+sep+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	}

	resp, err := s.client.Do(req)
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
