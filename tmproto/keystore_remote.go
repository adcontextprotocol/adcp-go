package tmproto

import (
	"bytes"
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
)

// RemoteKeyStore is a tmproto.KeyStore backed by a polled JSON snapshot
// (typically the router's GET /registry/snapshot endpoint). Reference
// providers use this to discover the router's signing keys without coupling
// to the router package's full Registry implementation.
//
// The snapshot is parsed into a kid-indexed map. Run() schedules background
// refreshes; LookupKey serves from the most recent successful refresh.
type RemoteKeyStore struct {
	url      string
	client   *http.Client
	logger   *slog.Logger
	interval time.Duration

	mu   sync.RWMutex
	keys map[string]*SigningKey
}

// RemoteKeyStoreOptions configures a RemoteKeyStore.
type RemoteKeyStoreOptions struct {
	// URL of the JSON snapshot endpoint that returns property records with
	// signing_keys arrays. Must use https:// unless AllowInsecureScheme is true.
	URL string

	// AllowInsecureScheme permits http:// URLs. For local development only —
	// a plain-HTTP keystore lets a network attacker swap signing keys.
	AllowInsecureScheme bool

	// HTTPClient is the client used for snapshot fetches. When nil, a 10-second
	// client is constructed with redirects denied (HPKE / signing-key material
	// must not follow registry redirects to arbitrary destinations).
	HTTPClient *http.Client

	// RefreshInterval between background refreshes. Defaults to 5 minutes
	// (the spec's recommended cache TTL).
	RefreshInterval time.Duration

	// Logger receives refresh outcomes.
	Logger *slog.Logger
}

// MaxSnapshotBytes caps the registry snapshot the keystore will ingest. Sized
// for property catalogs in the thousands of entries; the spec caps individual
// property records at a few hundred bytes.
const MaxSnapshotBytes = 1 * 1024 * 1024

// NewRemoteKeyStore builds a RemoteKeyStore. Call Refresh for an initial
// synchronous fetch and Run to begin background polling.
func NewRemoteKeyStore(opts RemoteKeyStoreOptions) (*RemoteKeyStore, error) {
	if opts.URL == "" {
		return nil, errors.New("tmproto: RemoteKeyStore URL is required")
	}
	parsed, err := url.Parse(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("tmproto: RemoteKeyStore URL invalid: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		// fine.
	case "http":
		if !opts.AllowInsecureScheme {
			return nil, errors.New("tmproto: RemoteKeyStore URL must use https:// (set AllowInsecureScheme for local development)")
		}
	default:
		return nil, fmt.Errorf("tmproto: RemoteKeyStore URL must use http(s) scheme, got %q", parsed.Scheme)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: denyCrossOriginRedirect,
		}
	}
	interval := opts.RefreshInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &RemoteKeyStore{
		url:      opts.URL,
		client:   client,
		logger:   logger,
		interval: interval,
		keys:     make(map[string]*SigningKey),
	}, nil
}

// denyCrossOriginRedirect blocks redirects that change scheme or host. A
// signing-key store has no business following 3xx to a different origin —
// that's the SSRF / key-substitution path.
func denyCrossOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	prev := via[0]
	if req.URL.Scheme != prev.URL.Scheme || req.URL.Host != prev.URL.Host {
		return fmt.Errorf("tmproto: cross-origin redirect to %s://%s denied", req.URL.Scheme, req.URL.Host)
	}
	if len(via) >= 5 {
		return errors.New("tmproto: too many redirects")
	}
	return nil
}

// LookupKey implements tmproto.KeyStore.
func (s *RemoteKeyStore) LookupKey(kid string) (*SigningKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[kid]
	return k, ok
}

// Refresh fetches the snapshot once and replaces the in-memory keystore.
// Returns the number of keys observed. An empty snapshot is treated as a
// transient registry condition — the previous keys are retained and a warning
// is logged so the agent doesn't 401 every request during a publisher's
// mid-deploy snapshot churn.
func (s *RemoteKeyStore) Refresh(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("snapshot returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSnapshotBytes))
	if err != nil {
		return 0, fmt.Errorf("read snapshot: %w", err)
	}
	keys, err := parseRegistrySnapshot(body, s.logger)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		s.mu.RLock()
		had := len(s.keys)
		s.mu.RUnlock()
		if had > 0 {
			s.logger.Warn("registry keystore snapshot empty — retaining cached keys", "url", s.url, "cached_keys", had)
			return had, nil
		}
	}
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
	return len(keys), nil
}

// Run blocks on an initial synchronous fetch so the keystore is non-empty
// before the caller serves traffic, then schedules background refreshes
// driven by the supplied context. Returns when ctx is canceled.
func (s *RemoteKeyStore) Run(ctx context.Context) error {
	if _, err := s.Refresh(ctx); err != nil {
		return err
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := s.Refresh(ctx); err != nil {
				s.logger.Warn("registry keystore refresh failed", "url", s.url, "error", err)
			} else {
				s.logger.Debug("registry keystore refreshed", "url", s.url, "keys", n)
			}
		}
	}
}

// minimalSnapshot describes the subset of the router's RegistrySnapshot we
// need to extract signing keys. Anything else in the snapshot is ignored.
type minimalSnapshot struct {
	Properties []struct {
		PropertyID  string       `json:"property_id"`
		PropertyRID string       `json:"property_rid"`
		SigningKeys []SigningKey `json:"signing_keys,omitempty"`
	} `json:"properties"`
}

func parseRegistrySnapshot(b []byte, logger *slog.Logger) (map[string]*SigningKey, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var snap minimalSnapshot
	if err := dec.Decode(&snap); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	out := make(map[string]*SigningKey)
	owners := make(map[string]string)
	for _, p := range snap.Properties {
		for i := range p.SigningKeys {
			k := p.SigningKeys[i]
			if k.Kid == "" {
				continue
			}
			if existing, conflict := owners[k.Kid]; conflict && existing != p.PropertyRID {
				if logger != nil {
					logger.Warn("registry signing-key kid collision — keeping first-seen entry",
						"kid", k.Kid, "first_property_rid", existing, "duplicate_property_rid", p.PropertyRID)
				}
				continue
			}
			out[k.Kid] = &k
			owners[k.Kid] = p.PropertyRID
		}
	}
	return out, nil
}
