package tmproto

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
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

// JWKSAlgEncryptionDHKEMX25519 is the JWK `alg` value buyers publish for the
// TMPX HPKE recipient public key under the suite the spec fixes today.
const JWKSAlgEncryptionDHKEMX25519 = "HPKE-DHKEM-X25519-HKDF-SHA256"

// adcpUseRequestSigning / adcpUseTmpxEncrypt are the `adcp_use` discriminator
// values in the JWKS. Keys carrying any other value are ignored.
const (
	adcpUseRequestSigning = "request-signing"
	adcpUseTmpxEncrypt    = "tmpx-encrypt"
)

// JWKSStore polls a JWKS endpoint and indexes the keys by purpose:
//
//   - Signing keys (`adcp_use=request-signing`) accessible via LookupKey(kid)
//     for verifier middleware.
//   - The current TMPX encryption key (`adcp_use=tmpx-encrypt`, newest `iat`)
//     accessible via CurrentEncryptionRecipient() for token sealers.
//
// Buyers publish both on the same `/.well-known/jwks.json` endpoint; the
// store handles both purposes in one Refresh.
type JWKSStore struct {
	url      string
	client   *http.Client
	logger   *slog.Logger
	interval time.Duration

	mu          sync.RWMutex
	signingKeys map[string]*SigningKey
	encRecip    *encRecipient
}

// encRecipient is the resolved encryption-key view: the most-recent
// adcp_use=tmpx-encrypt entry, with its X25519 public key pre-parsed.
type encRecipient struct {
	kid       string
	publicKey *ecdh.PublicKey
	issuedAt  int64
}

// JWKSStoreOptions configures a JWKSStore.
type JWKSStoreOptions struct {
	// URL of the JWKS endpoint (typically `/.well-known/jwks.json`).
	// Must be https:// unless AllowInsecureScheme is true.
	URL string

	// AllowInsecureScheme permits http:// URLs for local development only.
	AllowInsecureScheme bool

	// HTTPClient overrides the default 10-second client with cross-origin
	// redirect denial.
	HTTPClient *http.Client

	// RefreshInterval defaults to 5 minutes (spec-recommended cache TTL).
	RefreshInterval time.Duration

	// Logger receives refresh outcomes.
	Logger *slog.Logger
}

// NewJWKSStore builds a JWKSStore. Call Refresh once for an initial fetch,
// then Run for background polling.
func NewJWKSStore(opts JWKSStoreOptions) (*JWKSStore, error) {
	if opts.URL == "" {
		return nil, errors.New("tmproto: JWKSStore URL is required")
	}
	parsed, err := url.Parse(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("tmproto: JWKSStore URL invalid: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !opts.AllowInsecureScheme {
			return nil, errors.New("tmproto: JWKSStore URL must use https:// (set AllowInsecureScheme for local development)")
		}
	default:
		return nil, fmt.Errorf("tmproto: JWKSStore URL must use http(s) scheme, got %q", parsed.Scheme)
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
	return &JWKSStore{
		url:         opts.URL,
		client:      client,
		logger:      logger,
		interval:    interval,
		signingKeys: make(map[string]*SigningKey),
	}, nil
}

// LookupKey implements KeyStore over the JWKS-published signing keys.
func (s *JWKSStore) LookupKey(kid string) (*SigningKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.signingKeys[kid]
	return k, ok
}

// CurrentEncryptionRecipient returns the active TMPX recipient, picked as the
// adcp_use=tmpx-encrypt entry with the newest iat. Returns (zero, false) when
// no encryption key is currently advertised.
func (s *JWKSStore) CurrentEncryptionRecipient() (TmpxRecipient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.encRecip == nil {
		return TmpxRecipient{}, false
	}
	return TmpxRecipient{Kid: s.encRecip.kid, PublicKey: s.encRecip.publicKey}, true
}

// Refresh fetches the JWKS once and rebuilds both indexes. Transient empty
// snapshots retain cached state.
func (s *JWKSStore) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSnapshotBytes))
	if err != nil {
		return fmt.Errorf("read jwks: %w", err)
	}
	signing, enc, err := parseJWKS(body, s.logger)
	if err != nil {
		return err
	}
	if len(signing) == 0 && enc == nil {
		s.mu.RLock()
		had := len(s.signingKeys) > 0 || s.encRecip != nil
		s.mu.RUnlock()
		if had {
			s.logger.Warn("jwks empty — retaining cached keys", "url", s.url)
			return nil
		}
	}
	s.mu.Lock()
	s.signingKeys = signing
	s.encRecip = enc
	s.mu.Unlock()
	return nil
}

// Run runs an initial Refresh, then loops on the refresh interval until ctx
// is canceled. Returns ctx.Err() when the loop exits.
func (s *JWKSStore) Run(ctx context.Context) error {
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.Refresh(ctx); err != nil {
				s.logger.Warn("jwks refresh failed", "url", s.url, "error", err)
			} else {
				s.logger.Debug("jwks refreshed", "url", s.url)
			}
		}
	}
}

func parseJWKS(b []byte, logger *slog.Logger) (map[string]*SigningKey, *encRecipient, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var doc struct {
		Keys []SigningKey `json:"keys"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("parse jwks: %w", err)
	}
	signing := make(map[string]*SigningKey)
	var best *encRecipient
	for i := range doc.Keys {
		k := doc.Keys[i]
		if k.Kid == "" {
			continue
		}
		switch k.AdcpUse {
		case adcpUseRequestSigning:
			if err := validateSigningJWK(&k); err != nil {
				if logger != nil {
					logger.Warn("jwks signing key skipped", "kid", k.Kid, "error", err)
				}
				continue
			}
			if _, dup := signing[k.Kid]; dup {
				if logger != nil {
					logger.Warn("jwks duplicate signing kid — keeping first-seen", "kid", k.Kid)
				}
				continue
			}
			signing[k.Kid] = &k
		case adcpUseTmpxEncrypt:
			if err := validateTmpxKid(k.Kid); err != nil {
				if logger != nil {
					logger.Warn("jwks encryption key skipped", "kid", k.Kid, "error", err)
				}
				continue
			}
			pk, err := decodeX25519FromJWK(&k)
			if err != nil {
				if logger != nil {
					logger.Warn("jwks encryption key skipped", "kid", k.Kid, "error", err)
				}
				continue
			}
			candidate := &encRecipient{kid: k.Kid, publicKey: pk, issuedAt: k.IssuedAt}
			if best == nil || candidate.issuedAt > best.issuedAt {
				best = candidate
			}
		default:
			// Unknown adcp_use — forward compat, skip silently.
		}
	}
	return signing, best, nil
}

func validateSigningJWK(k *SigningKey) error {
	if k.Kty != signingKeyType {
		return fmt.Errorf("kty=%q, expected OKP", k.Kty)
	}
	if k.Crv != signingCurve {
		return fmt.Errorf("crv=%q, expected Ed25519", k.Crv)
	}
	if k.Alg != "" && k.Alg != signingAlgorithm {
		return fmt.Errorf("alg=%q, expected EdDSA", k.Alg)
	}
	if k.Use != "" && k.Use != "sig" {
		return fmt.Errorf("use=%q, expected sig", k.Use)
	}
	return nil
}

func decodeX25519FromJWK(k *SigningKey) (*ecdh.PublicKey, error) {
	if k.Kty != signingKeyType {
		return nil, fmt.Errorf("kty=%q, expected OKP", k.Kty)
	}
	if k.Crv != "X25519" {
		return nil, fmt.Errorf("crv=%q, expected X25519", k.Crv)
	}
	if k.Alg != "" && k.Alg != JWKSAlgEncryptionDHKEMX25519 {
		return nil, fmt.Errorf("alg=%q, expected %s", k.Alg, JWKSAlgEncryptionDHKEMX25519)
	}
	if k.Use != "" && k.Use != "enc" {
		return nil, fmt.Errorf("use=%q, expected enc", k.Use)
	}
	raw, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("base64url x: %w", err)
	}
	return LoadX25519PublicKey(raw)
}
