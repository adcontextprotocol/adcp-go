package signing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// JWKSResolver resolves a keyid to a JWK and the agent URL declaring it.
//
// Implementations handle JWKS fetching, caching, the 30-second refetch cooldown
// on kid-miss, and SSRF validation. For tests and specialized deployments,
// implement this interface directly.
type JWKSResolver interface {
	// Resolve returns the JWK for keyid along with the agent URL of the agent
	// that published it. On kid miss after a permitted refetch, return
	// (nil, "", *Error{Code: CodeKeyUnknown}). On transient fetch failure,
	// return *Error{Code: CodeJWKSUnavailable}. On SSRF failure,
	// CodeJWKSUntrusted.
	Resolve(ctx context.Context, keyid string) (*JWK, string, error)
}

// StaticJWKSResolver holds an in-memory keyid → (JWK, agentURL) mapping.
// Useful for tests and for verifiers that prefer to manage key rotation
// outside the signing package.
type StaticJWKSResolver struct {
	mu      sync.RWMutex
	entries map[string]staticJWKSEntry
}

type staticJWKSEntry struct {
	jwk      *JWK
	agentURL string
}

// NewStaticJWKSResolver constructs an empty resolver.
func NewStaticJWKSResolver() *StaticJWKSResolver {
	return &StaticJWKSResolver{entries: map[string]staticJWKSEntry{}}
}

// Put adds or replaces a keyid entry.
func (s *StaticJWKSResolver) Put(keyid string, jwk *JWK, agentURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[keyid] = staticJWKSEntry{jwk: jwk, agentURL: agentURL}
}

// Resolve implements JWKSResolver.
func (s *StaticJWKSResolver) Resolve(ctx context.Context, keyid string) (*JWK, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[keyid]
	if !ok {
		return nil, "", newError(CodeKeyUnknown, "keyid not in resolver")
	}
	return e.jwk, e.agentURL, nil
}

// HTTPJWKSResolver fetches JWKS documents over HTTPS with SSRF validation and
// a 30-second refetch cooldown between refetches per the AdCP profile.
//
// Each entry in the AgentMap associates a signing agent URL with a JWKS URL.
// On resolve, the resolver looks across all configured agents for a matching
// kid and returns the first match along with the agent URL.
type HTTPJWKSResolver struct {
	// Agents maps agent URL to that agent's jwks_uri. Populated at onboarding
	// or by a separate discovery loop reading brand.json.
	Agents map[string]string

	// HTTPClient is used for JWKS fetches. Must have a Transport whose
	// DialContext rejects private/link-local/loopback addresses per the AdCP
	// SSRF rules. If nil, a safely-configured default is constructed.
	HTTPClient *http.Client

	// RefetchCooldown is the minimum time between refetches of the same
	// jwks_uri. Defaults to 30 seconds per the AdCP profile.
	RefetchCooldown time.Duration

	mu          sync.Mutex
	cache       map[string]*jwksCacheEntry // keyed by jwks_uri
	lastRefetch map[string]time.Time       // keyed by jwks_uri
	nowFunc     func() time.Time           // for tests
}

type jwksCacheEntry struct {
	jwks *JWKS
	at   time.Time
}

// NewHTTPJWKSResolver constructs a fetcher with a safe default HTTP client.
func NewHTTPJWKSResolver(agents map[string]string) *HTTPJWKSResolver {
	return &HTTPJWKSResolver{
		Agents:          agents,
		HTTPClient:      newSafeHTTPClient(),
		RefetchCooldown: defaultJWKSRefetchCooldown,
		cache:           map[string]*jwksCacheEntry{},
		lastRefetch:     map[string]time.Time{},
		nowFunc:         time.Now,
	}
}

func (h *HTTPJWKSResolver) now() time.Time {
	if h.nowFunc != nil {
		return h.nowFunc()
	}
	return time.Now()
}

// Resolve implements JWKSResolver.
func (h *HTTPJWKSResolver) Resolve(ctx context.Context, keyid string) (*JWK, string, error) {
	// First pass: look in cache.
	for agentURL, jwksURL := range h.Agents {
		jwk, err := h.lookupInCache(jwksURL, keyid)
		if err != nil {
			return nil, "", err
		}
		if jwk != nil {
			return jwk, agentURL, nil
		}
	}
	// Kid miss — refetch each (subject to cooldown).
	var lastFetchErr error
	for agentURL, jwksURL := range h.Agents {
		if !h.canRefetch(jwksURL) {
			continue
		}
		if err := h.refetch(ctx, jwksURL); err != nil {
			lastFetchErr = err
			continue
		}
		jwk, err := h.lookupInCache(jwksURL, keyid)
		if err != nil {
			return nil, "", err
		}
		if jwk != nil {
			return jwk, agentURL, nil
		}
	}
	if lastFetchErr != nil {
		if asErr := AsError(lastFetchErr); asErr != nil {
			return nil, "", asErr
		}
		return nil, "", wrapError(CodeJWKSUnavailable, "all JWKS fetches failed", lastFetchErr)
	}
	return nil, "", newError(CodeKeyUnknown, "keyid not found in any agent JWKS")
}

func (h *HTTPJWKSResolver) lookupInCache(jwksURL, keyid string) (*JWK, error) {
	h.mu.Lock()
	entry := h.cache[jwksURL]
	h.mu.Unlock()
	if entry == nil {
		return nil, nil
	}
	return entry.jwks.Find(keyid), nil
}

func (h *HTTPJWKSResolver) canRefetch(jwksURL string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	last, ok := h.lastRefetch[jwksURL]
	if !ok {
		return true
	}
	cooldown := h.RefetchCooldown
	if cooldown <= 0 {
		cooldown = defaultJWKSRefetchCooldown
	}
	return h.now().Sub(last) >= cooldown
}

func (h *HTTPJWKSResolver) refetch(ctx context.Context, jwksURL string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", jwksURL, nil)
	if err != nil {
		return wrapError(CodeJWKSUnavailable, "build request", err)
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		// net.OpError with .Op=="dial" on SSRF denial translates to CodeJWKSUntrusted.
		if errors.Is(err, errSSRFBlocked) {
			return wrapError(CodeJWKSUntrusted, "jwks_uri failed SSRF validation", err)
		}
		return wrapError(CodeJWKSUnavailable, "fetch jwks", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return newError(CodeJWKSUnavailable, fmt.Sprintf("jwks fetch status %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return wrapError(CodeJWKSUnavailable, "read jwks body", err)
	}
	jwks, err := ParseJWKS(body)
	if err != nil {
		return wrapError(CodeJWKSUnavailable, "parse jwks", err)
	}
	h.mu.Lock()
	h.cache[jwksURL] = &jwksCacheEntry{jwks: jwks, at: h.now()}
	h.lastRefetch[jwksURL] = h.now()
	h.mu.Unlock()
	return nil
}

// --- SSRF-safe HTTP client ---

var errSSRFBlocked = errors.New("ssrf: address blocked")

// NewSafeHTTPClient returns an http.Client with SSRF-safe dialing: DNS
// resolution rejects private / link-local / loopback / CGNAT / ULA addresses,
// and the dial is pinned to the first validated IP to prevent DNS rebinding.
// TLS SNI uses the original hostname.
//
// Callers that supply a custom HTTPClient to HTTPJWKSResolver lose this
// protection unless they start from NewSafeHTTPClient and extend it.
func NewSafeHTTPClient() *http.Client {
	return newSafeHTTPClient()
}

func newSafeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isDisallowedIP(ip.IP) {
					return nil, errSSRFBlocked
				}
			}
			// Pin to the first validated IP to prevent DNS rebinding between
			// validation and connect.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout: 5 * time.Second,
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: transport}
}

// isDisallowedIP returns true for IPs that must not be dialed per the AdCP
// webhook SSRF rules (also applied to JWKS fetch per the spec cross-reference).
func isDisallowedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// Private RFC 1918 / ULA.
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0: // 0.0.0.0/8 — this-network; routes to loopback on Linux
			return true
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1]&0xf0 == 16:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 169 && ip4[1] == 254:
			return true
		case ip4[0] == 100 && ip4[1]&0xc0 == 64: // CGNAT 100.64.0.0/10
			return true
		case ip4[0] == 198 && ip4[1]&0xfe == 18: // 198.18.0.0/15 benchmark
			return true
		}
	}
	if ip.To4() == nil && len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc { // fc00::/7 ULA
			return true
		}
	}
	return false
}
