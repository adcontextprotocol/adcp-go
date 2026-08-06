package router

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// authorizationHeader is the standard credential header, and the only one
// accepted unless the operator names an additional one via AuthConfig.KeyHeader.
//
// There is deliberately no default in the `X-AdCP-*` namespace: that namespace
// belongs to the spec, which defines exactly two members (X-AdCP-Signature,
// X-AdCP-Key-Id) and explicitly places publisher-to-router authentication
// "outside the scope of TMP signing". Minting an X-AdCP-Router-Key would claim
// protocol namespace for a deployment concern the protocol declines to own.
const authorizationHeader = "Authorization"

// MinAuthAPIKeyLength is the shortest shared secret the router accepts. It is
// a floor on length, not a measure of entropy — nothing here can tell a
// 32-character random key from a 32-character passphrase, and operators are
// responsible for generating these from a CSPRNG. The floor is set where a
// hex- or base64-encoded random key clears 128 bits, because forging one
// inbound request yields the router's signature on a fan-out, so a guessable
// key defeats the whole signing model.
const MinAuthAPIKeyLength = 32

// Rejection reasons reported to AuthMetrics. Bounded set — safe as a metric
// label.
const (
	AuthRejectMissingCredential = "missing_credential"
	AuthRejectInvalidKey        = "invalid_key"
	AuthRejectMissingClientCert = "missing_client_cert"
)

// AuthConfig configures publisher→router authentication.
//
// The spec (§Signature verification) requires this: "The router MUST
// authenticate incoming requests from the publisher before signing and fanning
// out. The mechanism for publisher-to-router authentication is
// deployment-specific (mTLS, API key, etc.) and outside the scope of TMP
// signing, but MUST be enforced. This prevents a compromised publisher-side
// component from laundering unauthenticated requests through the router's
// signature."
//
// Two mechanisms. Configuring both means BOTH are required on every request —
// they are evaluated in series, not as alternatives, so adding one to a running
// deployment locks out every caller that cannot yet satisfy it:
//
//   - APIKeys — a shared secret presented as `Authorization: Bearer <key>`, or
//     in KeyHeader when the operator names one (for ingresses that consume the
//     Authorization header). Multiple keys are accepted so a secret can be
//     rotated without a window where neither the old nor the new key works.
//   - ClientCAPath — mTLS. The PEM's certificates become the router
//     listener's client-cert trust anchor and the handshake requires a
//     verified client cert. Requires the router to terminate TLS itself
//     (TLSConfig cert+key); ServerConfig.Validate rejects the combination
//     otherwise, because a listener that never sees a client cert would
//     reject every request.
//
// Disabled turns authentication off. It exists because a deployment may
// enforce this at an ingress the router never sees (a service mesh with mTLS
// between the publisher's ad server and the router, for instance) — but it is
// an explicit opt-out, mirroring SigningConfig.Disabled, so an operator cannot
// arrive at an unauthenticated router by omission.
type AuthConfig struct {
	Disabled     bool     `json:"disabled,omitempty"`
	APIKeys      []string `json:"api_keys,omitempty"`
	KeyHeader    string   `json:"key_header,omitempty"`
	ClientCAPath string   `json:"client_ca_path,omitempty"`
}

// Enabled reports whether the router should enforce inbound authentication.
func (c AuthConfig) Enabled() bool { return !c.Disabled }

// EffectiveKeyHeader returns the additional credential header the operator
// named, or "" when only Authorization is accepted.
func (c AuthConfig) EffectiveKeyHeader() string {
	return strings.TrimSpace(c.KeyHeader)
}

// Validate reports a configuration error when authentication is enabled but no
// usable mechanism is configured, or when a shared secret is too short to be
// worth having. "api_keys or client_ca_path" here is the requirement to supply
// at least one — supplying both makes both mandatory at request time.
func (c AuthConfig) Validate() error {
	if c.Disabled {
		return nil
	}
	if len(c.APIKeys) == 0 && c.ClientCAPath == "" {
		return errors.New("auth: configure api_keys or client_ca_path (or set auth.disabled=true / TMP_ROUTER_AUTH_DISABLED=true to enforce authentication upstream)")
	}
	for i, key := range c.APIKeys {
		if len(key) < MinAuthAPIKeyLength {
			return fmt.Errorf("auth: api_keys[%d] is %d characters, minimum is %d", i, len(key), MinAuthAPIKeyLength)
		}
	}
	if h := c.EffectiveKeyHeader(); h != "" && strings.ContainsAny(h, " \t\r\n:") {
		return fmt.Errorf("auth: key_header %q is not a valid HTTP header name", c.KeyHeader)
	}
	return nil
}

// ClientCAPool loads the client-certificate trust anchor. Returns (nil, nil)
// when no CA path is configured, or when authentication is disabled.
//
// Disabled has to short-circuit here, not just at NewInboundAuth: installing the
// pool makes the listener demand a verified client certificate, so a
// leftover client_ca_path in a config file would keep rejecting every publisher
// at the TLS handshake after the operator moved authentication upstream — an
// outage under a startup log line claiming authentication is off.
func (c AuthConfig) ClientCAPool() (*x509.CertPool, error) {
	if c.Disabled || c.ClientCAPath == "" {
		return nil, nil
	}
	pemBytes, err := os.ReadFile(c.ClientCAPath) //nolint:gosec // path is from operator config
	if err != nil {
		return nil, fmt.Errorf("auth: read client CA %q: %w", c.ClientCAPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		// AppendCertsFromPEM reports only "nothing was added", which is the
		// same signal for an empty file, a non-PEM file, and a PEM holding
		// only non-certificate blocks. Distinguish the common typo — pointing
		// at a private key instead of a CA bundle — so the operator does not
		// have to guess.
		if block, _ := pem.Decode(pemBytes); block != nil && block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("auth: client CA %q holds a %q PEM block, expected CERTIFICATE", c.ClientCAPath, block.Type)
		}
		return nil, fmt.Errorf("auth: client CA %q contains no PEM certificates", c.ClientCAPath)
	}
	return pool, nil
}

// AuthMetrics counts inbound authentication rejections. reason is one of the
// AuthReject* constants.
type AuthMetrics interface {
	IncAuthRejected(reason string)
}

// InboundAuth authenticates publisher requests before the router signs and
// fans them out.
type InboundAuth struct {
	// keyDigests holds SHA-256 of each configured key. Digests rather than raw
	// keys so the comparison is both constant-time and length-independent —
	// subtle.ConstantTimeCompare short-circuits on a length mismatch, which
	// would leak the configured key length.
	keyDigests        [][sha256.Size]byte
	keyHeader         string
	requireClientCert bool
	metrics           AuthMetrics
	logger            *slog.Logger
}

// InboundAuthOption configures an InboundAuth.
type InboundAuthOption func(*InboundAuth)

// WithAuthMetrics sets the rejection-counter sink.
func WithAuthMetrics(m AuthMetrics) InboundAuthOption {
	return func(a *InboundAuth) { a.metrics = m }
}

// WithAuthLogger sets the logger. Defaults to slog.Default().
func WithAuthLogger(l *slog.Logger) InboundAuthOption {
	return func(a *InboundAuth) { a.logger = l }
}

// NewInboundAuth builds the authenticator for cfg. Returns (nil, nil) when
// authentication is disabled, so callers can skip wrapping their handlers.
func NewInboundAuth(cfg AuthConfig, opts ...InboundAuthOption) (*InboundAuth, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Disabled {
		return nil, nil
	}
	a := &InboundAuth{
		keyHeader:         cfg.EffectiveKeyHeader(),
		requireClientCert: cfg.ClientCAPath != "",
		logger:            slog.Default(),
	}
	for _, key := range cfg.APIKeys {
		a.keyDigests = append(a.keyDigests, sha256.Sum256([]byte(key)))
	}
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

// Middleware wraps next so only authenticated callers reach it. A nil
// *InboundAuth passes through unchanged, which is the disabled case.
//
// Wrap the match endpoints and any operator surface that discloses
// configuration. Do NOT wrap /registry/snapshot — providers fetch it to
// resolve the router's signing keys, so it has to stay reachable without a
// publisher credential.
func (a *InboundAuth) Middleware(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// VerifiedChains, not PeerCertificates: the latter is populated for any
		// certificate the client offered, including a self-signed one, under
		// tls.RequestClientCert or RequireAnyClientCert. Only VerifiedChains
		// means the chain was validated against the configured ClientCAs.
		// cmd/router configures RequireAndVerifyClientCert so both would be
		// equivalent there, but InboundAuth is exported for embedders who bring
		// their own tls.Config — and accepting an unverified certificate would
		// hand them exactly the request-laundering this middleware exists to
		// prevent.
		if a.requireClientCert && (req.TLS == nil || len(req.TLS.VerifiedChains) == 0) {
			a.reject(w, req, AuthRejectMissingClientCert)
			return
		}
		if len(a.keyDigests) > 0 {
			presented, ok := a.presentedKey(req)
			if !ok {
				a.reject(w, req, AuthRejectMissingCredential)
				return
			}
			if !a.keyMatches(presented) {
				a.reject(w, req, AuthRejectInvalidKey)
				return
			}
		}
		next.ServeHTTP(w, req)
	})
}

// presentedKey pulls the shared secret from `Authorization: Bearer <key>`, then
// from the operator-named header when one is configured. The first credential
// present is the one evaluated.
func (a *InboundAuth) presentedKey(req *http.Request) (string, bool) {
	if authz := req.Header.Get(authorizationHeader); authz != "" {
		if rest, found := cutBearerPrefix(authz); found {
			if rest != "" {
				return rest, true
			}
			return "", false
		}
	}
	if a.keyHeader != "" {
		if key := req.Header.Get(a.keyHeader); key != "" {
			return key, true
		}
	}
	return "", false
}

// cutBearerPrefix strips a case-insensitive "Bearer " scheme prefix.
func cutBearerPrefix(authz string) (string, bool) {
	const scheme = "bearer "
	if len(authz) < len(scheme) || !strings.EqualFold(authz[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(authz[len(scheme):]), true
}

// keyMatches compares the presented secret against every configured key
// without short-circuiting, so the response time does not reveal which key
// position matched or how many keys are configured.
func (a *InboundAuth) keyMatches(presented string) bool {
	got := sha256.Sum256([]byte(presented))
	matched := 0
	for i := range a.keyDigests {
		matched |= subtle.ConstantTimeCompare(got[:], a.keyDigests[i][:])
	}
	return matched == 1
}

// reject writes a 401 carrying a TMP error envelope.
//
// Rejections are logged at DEBUG and counted on AuthMetrics: an exposed router
// gets scanned, and one WARN per probe would bury real signal. The
// reason-labeled counter is the surface to alert on.
//
// The error code is invalid_request because error.json's `code` enum has no
// authentication value — the same gap the identity agent works around for
// unsupported adcp_major_version.
func (a *InboundAuth) reject(w http.ResponseWriter, req *http.Request, reason string) {
	if a.metrics != nil {
		a.metrics.IncAuthRejected(reason)
	}
	if a.logger != nil {
		a.logger.Debug("rejected unauthenticated request",
			"method", req.Method,
			"path", req.URL.Path,
			"reason", reason,
		)
	}
	w.Header().Set("Content-Type", "application/json")
	if len(a.keyDigests) > 0 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(http.StatusUnauthorized)
	// No request_id echo: the body is deliberately not read on this path, so
	// there is no request_id to echo and reading one would let an
	// unauthenticated caller push bytes through the router.
	if err := json.NewEncoder(w).Encode(tmproto.ErrorResponse{
		Type:    tmproto.TypeError,
		Code:    tmproto.ErrorCodeInvalidRequest,
		Message: "unauthorized",
	}); err != nil && a.logger != nil {
		a.logger.Debug("failed to write auth error response", "error", err)
	}
}
