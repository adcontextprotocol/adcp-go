// Package tmpendpoint validates the endpoint URL a TMP provider agent
// verifies request signatures against.
//
// The TMP spec binds every request signature to `provider_endpoint_url`, which
// is the provider's registered BASE url — `endpoint` on
// provider-registration.json, described as "Base URL the router calls. The
// router appends /context for Context Match and /identity for Identity Match."
// The router signs that base value and appends the operation path only when
// dispatching, so an agent configured with the path-inclusive URL builds a
// different signing input than the router and rejects every request with
// ErrSignatureInvalid.
//
// That failure mode is silent at startup and total at runtime, which is why it
// is worth a config-time check rather than a comment.
//
// This lives under targeting/internal because both agents in this module need
// it. It is arguably a property of the signing convention that tmproto owns
// (alongside NormalizeProviderEndpointURL), but tmproto is an independently
// released module — putting it there would make the agents wait on a tmproto
// release to pick up the check.
package tmpendpoint

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// operationPaths are the per-operation paths a router appends to a provider's
// registered base endpoint when dispatching (spec §Transport: "Each provider
// exposes two path-based endpoints under its base URL: POST /context for
// Context Match and POST /identity for Identity Match").
var operationPaths = []string{"/context", "/identity"}

// Validate reports whether raw is usable as the value a router puts in
// provider_endpoint_url when signing.
//
// The operation path is not stripped automatically: rewriting an operator's
// configured endpoint would hide a registration mismatch that they, not the
// agent, have to reconcile against the router's provider config.
func Validate(raw string) error {
	// Trailing slashes are insignificant — the signing convention trims them on
	// both sides (tmproto.NormalizeProviderEndpointURL) — so normalize first and
	// ".../identity/" is caught alongside ".../identity".
	normalized := strings.TrimRight(strings.TrimSpace(raw), "/")
	if normalized == "" {
		return errors.New("own endpoint URL must not be empty")
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return fmt.Errorf("own endpoint URL %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("own endpoint URL %q must use the http or https scheme", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("own endpoint URL %q must be absolute, with a scheme and host", raw)
	}
	// Match on the parsed path, not the whole string: a value carrying a query
	// or fragment (".../context?x=1") is still path-inclusive and must not slip
	// through a raw-suffix test.
	//
	// This is a suffix heuristic, so it also rejects the pathological case of a
	// provider whose registered base URL genuinely ends in an operation path
	// (endpoint=https://api.example.com/v1/identity, dispatched as
	// .../v1/identity/identity). There is no override: that configuration is
	// indistinguishable from the misconfiguration this exists to catch, and the
	// error text points at the router's registration as the thing to reconcile.
	path := strings.TrimRight(u.Path, "/")
	for _, opPath := range operationPaths {
		if strings.HasSuffix(path, opPath) {
			return fmt.Errorf("own endpoint URL %q has a path ending in %q, but signatures bind to the registered BASE url that the router appends %q to — drop the suffix and make this match the `endpoint` in the router's provider registration", raw, opPath, opPath)
		}
	}
	return nil
}
