package uid2client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// IdentityScope selects between the UID2 and EUID protocol variants. The
// wire protocol is identical; only the operator URLs and the token prefix
// byte differ (see decryptToken).
type IdentityScope int

const (
	// ScopeUID2 is the North-American UID2 identity scope, prefix bit 4
	// clear on V3/V4 tokens.
	ScopeUID2 IdentityScope = 0
	// ScopeEUID is the European EUID identity scope, prefix bit 4 set on
	// V3/V4 tokens.
	ScopeEUID IdentityScope = 1
)

func (s IdentityScope) String() string {
	switch s {
	case ScopeUID2:
		return "UID2"
	case ScopeEUID:
		return "EUID"
	default:
		return fmt.Sprintf("IdentityScope(%d)", int(s))
	}
}

// key is one entry in the operator's shared key store, populated from a
// /v2/key/bidstream response.
//
// The secret is a 32-byte AES key. It is used as-is by AES-GCM (V3/V4
// tokens) and by AES-CBC (V2 tokens). Do not log it.
type key struct {
	id        int64
	keysetID  int
	siteID    int // only meaningful for legacy /key/latest-style responses
	created   time.Time
	activates time.Time
	expires   time.Time
	secret    []byte
}

// keyStore is an immutable snapshot of the operator's shared keys. It is
// swapped atomically as a whole via [Client.store]; readers hold the
// pointer they read for the duration of one Decrypt call, so there is no
// tear across a refresh.
type keyStore struct {
	// keys is keyed by key ID. Both master keys and site/keyset keys live
	// here; the token structure references them by ID.
	keys map[int64]*key

	// scope is the identity scope advertised by the operator's response,
	// or the client's configured scope when the operator did not
	// advertise one. Verified against the client's configured scope by
	// the Client constructor.
	scope IdentityScope

	// latestExpiry is the max of every key's Expires time. Used by
	// [keyStore.isValid] to detect a fully-expired store — a symptom of a
	// stuck background refresh — and surface it as ErrKeysStale rather
	// than the more ambiguous ErrKeyNotFound.
	latestExpiry time.Time
}

// isValid reports whether the store has at least one key that hasn't yet
// expired. Used by [Client.Decrypt] to distinguish "the token's key ID
// isn't here" from "every key has expired" — a distinction operators care
// about because the latter means the refresh loop is broken.
func (s *keyStore) isValid(now time.Time) bool {
	return s != nil && now.Before(s.latestExpiry)
}

// keyRefreshResponse is the JSON shape returned by /v2/key/bidstream and
// /v2/key/sharing after AES-GCM envelope decryption. Fields we don't use
// (site_data, caller_site_id, etc.) are decoded but discarded — decoding
// them tolerates evolving operator schemas without blowing up.
type keyRefreshResponse struct {
	Body keyRefreshBody `json:"body"`
}

type keyRefreshBody struct {
	// IdentityScope is advertised as "UID2" or "EUID". Missing = scope
	// not published by this operator; the client falls back to the
	// configured scope in that case.
	IdentityScope string `json:"identity_scope"`

	// Keys is the array of shared keys addressable by their integer ID.
	// Each token references one master key ID (by which the outer layer
	// is decrypted) and one site/keyset key ID (by which the inner
	// identity layer is decrypted).
	Keys []keyJSON `json:"keys"`
}

type keyJSON struct {
	ID        int64  `json:"id"`
	KeysetID  int    `json:"keyset_id"`
	SiteID    int    `json:"site_id"`
	Created   int64  `json:"created"`
	Activates int64  `json:"activates"`
	Expires   int64  `json:"expires"`
	Secret    string `json:"secret"`
}

// parseKeyRefreshResponse decodes a JSON key-refresh response body into an
// immutable [keyStore]. The advertised identity scope is threaded through
// so [Client.New] can reject a UID2 API key that returns EUID keys (or
// vice versa) — a configuration mistake that would otherwise surface as
// silent decrypt failures per token.
//
// wantScope is the client's configured scope, used only as a fallback when
// the operator does not publish "identity_scope" in the response body.
func parseKeyRefreshResponse(body []byte, wantScope IdentityScope) (*keyStore, error) {
	var resp keyRefreshResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("uid2client: unmarshal key refresh body: %w", err)
	}

	store := &keyStore{
		keys:  make(map[int64]*key, len(resp.Body.Keys)),
		scope: wantScope,
	}

	switch resp.Body.IdentityScope {
	case "":
		// no advertised scope; keep the configured one
	case "UID2":
		store.scope = ScopeUID2
	case "EUID":
		store.scope = ScopeEUID
	default:
		return nil, fmt.Errorf("uid2client: unknown identity_scope %q", resp.Body.IdentityScope)
	}

	if len(resp.Body.Keys) == 0 {
		return nil, fmt.Errorf("uid2client: key refresh response contained no keys")
	}

	for i := range resp.Body.Keys {
		k := &resp.Body.Keys[i]
		secret, err := base64.StdEncoding.DecodeString(k.Secret)
		if err != nil {
			return nil, fmt.Errorf("uid2client: decode key %d secret: %w", k.ID, err)
		}
		// UID2 uses AES-256, so keys are always 32 bytes. Rejecting a
		// short/long key here catches a malformed operator response
		// before it produces a downstream "aes: invalid key size" panic
		// deep inside a token decrypt.
		if len(secret) != 32 {
			return nil, fmt.Errorf("uid2client: key %d secret is %d bytes, want 32", k.ID, len(secret))
		}
		entry := &key{
			id:        k.ID,
			keysetID:  k.KeysetID,
			siteID:    k.SiteID,
			created:   time.Unix(k.Created, 0),
			activates: time.Unix(k.Activates, 0),
			expires:   time.Unix(k.Expires, 0),
			secret:    secret,
		}
		store.keys[entry.id] = entry
		if entry.expires.After(store.latestExpiry) {
			store.latestExpiry = entry.expires
		}
	}

	return store, nil
}
