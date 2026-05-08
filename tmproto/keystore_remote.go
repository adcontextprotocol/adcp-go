package tmproto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// RemoteKeyStore is a tmproto.KeyStore backed by a polled JSON snapshot
// (typically the router's GET /registry/snapshot endpoint). Reference
// providers use this to discover the router's signing keys without coupling
// to the router package's full Registry implementation.
//
// The snapshot is parsed into a kid-indexed map. Refresh runs on a fixed
// interval; LookupKey serves from the most recent successful refresh.
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
	// signing_keys arrays.
	URL string

	// HTTPClient is the client used for snapshot fetches. Defaults to a
	// 10-second-timeout client.
	HTTPClient *http.Client

	// RefreshInterval between background refreshes. Defaults to 5 minutes
	// (the spec's recommended cache TTL).
	RefreshInterval time.Duration

	// Logger receives refresh outcomes.
	Logger *slog.Logger
}

// NewRemoteKeyStore builds a RemoteKeyStore. Call Start to begin background
// refresh, or Refresh once for synchronous initial load.
func NewRemoteKeyStore(opts RemoteKeyStoreOptions) (*RemoteKeyStore, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("tmproto: RemoteKeyStore URL is required")
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
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

// LookupKey implements tmproto.KeyStore.
func (s *RemoteKeyStore) LookupKey(kid string) (*SigningKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[kid]
	return k, ok
}

// Refresh fetches the snapshot once and replaces the in-memory keystore.
// Returns the number of keys observed.
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return 0, fmt.Errorf("read snapshot: %w", err)
	}
	keys, err := parseRegistrySnapshot(body)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
	return len(keys), nil
}

// Start begins a background refresh loop, blocking on an initial synchronous
// fetch so the keystore is non-empty before the caller serves traffic. Returns
// an error if the initial fetch fails.
func (s *RemoteKeyStore) Start(ctx context.Context) error {
	if _, err := s.Refresh(ctx); err != nil {
		return err
	}
	go s.refreshLoop(ctx)
	return nil
}

func (s *RemoteKeyStore) refreshLoop(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
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

func parseRegistrySnapshot(b []byte) (map[string]*SigningKey, error) {
	var snap minimalSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	out := make(map[string]*SigningKey)
	for _, p := range snap.Properties {
		for i := range p.SigningKeys {
			k := p.SigningKeys[i]
			if k.Kid == "" {
				continue
			}
			out[k.Kid] = &k
		}
	}
	return out, nil
}
