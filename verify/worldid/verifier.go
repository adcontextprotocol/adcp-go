// Package worldid is a concrete World ID AttestationVerifier for the TMP
// verified-identity surface. It lives in its own module so its World-ID-specific
// dependency surface (an HTTP client to World's verifier backend) stays out of
// the core adcp-go module that every consumer imports — the reason
// targeting.AttestationVerifier is a pluggable interface with no default. The
// deployable identity-agent wires this in (config-gated) via Run options.
//
// Verify-before-trust: a Verifier never trusts the claims the sender asserts
// on the attestation. It validates the World ID proof against World's
// `verify/{rp_id}` backend and derives the nullifier and claim set from
// World's authoritative response. The cheap, fail-closed local checks
// (signal_binding present, expiry, relying_party_id == the RP we act as) have
// already run in targeting.LocalPreCheck before Verify is called.
package worldid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// DefaultBaseURL is World's developer-portal API host. Override via WithBaseURL
// (the demo points it at a local forwarder or a mock).
const DefaultBaseURL = "https://developer.worldcoin.org"

// maxVerifyResponseBytes bounds the verifier's read of World's response.
const maxVerifyResponseBytes = 64 << 10

// identifierToClaim maps a World ID verify-response credential identifier to
// the closed AdCP attestation-claim vocabulary. Only claims World actually
// returns enter the verified set; the sender's asserted att.Claims are never
// trusted. Age-credential identifiers are added here as World exposes them.
var identifierToClaim = map[string]tmproto.AttestationClaim{
	"proof_of_human": tmproto.AttestationClaimUniqueHuman,
}

// Verifier validates World ID proofs against World's verify backend.
type Verifier struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Option configures a Verifier.
type Option func(*Verifier)

// WithBaseURL overrides World's API host (e.g. a local forwarder or a test
// server).
func WithBaseURL(u string) Option { return func(v *Verifier) { v.baseURL = u } }

// WithAPIKey sets a bearer token for World's backend, if the deployment's
// verify endpoint requires one.
func WithAPIKey(k string) Option { return func(v *Verifier) { v.apiKey = k } }

// WithHTTPClient injects an HTTP client (timeouts, transport).
func WithHTTPClient(c *http.Client) Option { return func(v *Verifier) { v.httpClient = c } }

// New returns a Verifier. Defaults to World's public API host and a 5s client.
func New(opts ...Option) *Verifier {
	v := &Verifier{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

var _ targeting.AttestationVerifier = (*Verifier)(nil)

// verifyResponse is the subset of World's v4 verify response we read. v4
// carries the credential(s) in responses[]; nullifier is `nullifier` (the v3
// `nullifier_hash` is gone). success, when present and false, is a rejection.
type verifyResponse struct {
	Success   *bool  `json:"success"`
	Nullifier string `json:"nullifier"`
	Responses []struct {
		Nullifier  string `json:"nullifier"`
		Identifier string `json:"identifier"`
	} `json:"responses"`
}

func (r verifyResponse) nullifier() string {
	for _, it := range r.Responses {
		if it.Nullifier != "" {
			return it.Nullifier
		}
	}
	return r.Nullifier
}

func (r verifyResponse) claims() map[tmproto.AttestationClaim]struct{} {
	out := make(map[tmproto.AttestationClaim]struct{}, len(r.Responses))
	for _, it := range r.Responses {
		if c, ok := identifierToClaim[it.Identifier]; ok {
			out[c] = struct{}{}
		}
	}
	return out
}

// Verify validates the attestation's World ID proof against World's
// `verify/{rp_id}` backend (rp_id = the relying party this receiver acts as,
// from vctx) and returns the relying-party-scoped nullifier plus the claims
// World confirmed. Any failure — no proof, non-2xx, success:false, missing
// nullifier, or no recognised claim — returns an error, which the caller
// treats as "no attestation" (claims are never trusted on failure).
func (v *Verifier) Verify(ctx context.Context, att *tmproto.Attestation, vctx targeting.VerifyContext) (targeting.VerifiedIdentity, error) {
	var zero targeting.VerifiedIdentity
	if att == nil || len(att.Proof) == 0 {
		return zero, errors.New("worldid: attestation carries no proof material")
	}
	rp := vctx.ExpectedRelyingPartyID
	if rp == "" {
		return zero, errors.New("worldid: no expected relying_party_id to verify against")
	}

	// The proof material is the verbatim World ID widget result; forward it
	// unmodified to World's rp_id-scoped verify endpoint.
	body, err := json.Marshal(att.Proof)
	if err != nil {
		return zero, fmt.Errorf("worldid: marshal proof: %w", err)
	}
	endpoint := strings.TrimRight(v.baseURL, "/") + "/api/v4/verify/" + url.PathEscape(rp)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("worldid: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if v.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+v.apiKey)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("worldid: verify request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxVerifyResponseBytes))
	if err != nil {
		return zero, fmt.Errorf("worldid: read verify response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// World returns 4xx with a body for a rejected proof; do not echo the
		// body (it may carry proof material).
		return zero, fmt.Errorf("worldid: verify rejected (HTTP %d)", resp.StatusCode)
	}

	var vr verifyResponse
	if err := json.Unmarshal(raw, &vr); err != nil {
		return zero, fmt.Errorf("worldid: decode verify response: %w", err)
	}
	if vr.Success != nil && !*vr.Success {
		return zero, errors.New("worldid: proof rejected by verifier")
	}
	nullifier := vr.nullifier()
	if nullifier == "" {
		return zero, errors.New("worldid: verify response carried no nullifier")
	}
	claims := vr.claims()
	if len(claims) == 0 {
		return zero, errors.New("worldid: verify response confirmed no recognised claims")
	}

	return targeting.VerifiedIdentity{
		Nullifier:      nullifier,
		RelyingPartyID: rp,
		Claims:         claims,
	}, nil
}
