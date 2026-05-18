package identityagent

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TMPXSealer holds the resolved TMPX recipient state used to seal tokens
// alongside identity-match responses. Construct with NewTMPXSealer; call
// Seal at request time to produce a per-request HPKE token.
//
// The string→binary conversion underlying Seal is the spec's reference
// SHA-512 stub — tokens are NOT interoperable with any real buyer master.
// Real deployments decode UID2/RampID/etc. according to the source graph's
// encoding. NewTMPXSealer refuses to construct an instance unless the
// caller has acknowledged this via TMPXConfig.ReferenceStubAck.
type TMPXSealer struct {
	country  string
	encStore tmpxRecipientResolver

	// priority is the explicit per-spec priority ordering used when the
	// resolved identities exceed the 255-byte wire budget. Entries earlier
	// in the slice rank higher; entries whose UIDType is absent are
	// dropped (the spec requires explicit configuration — arbitrary
	// truncation is forbidden). When priority is empty, no truncation is
	// performed and an over-budget token is reported as an error.
	priority []tmproto.UIDType
}

// tmpxRecipientResolver returns the buyer-cluster TMPX recipient at the
// moment of sealing. Backed by tmproto.JWKSStore in production; replaceable
// with a fixed recipient in tests.
type tmpxRecipientResolver interface {
	CurrentEncryptionRecipient() (tmproto.TmpxRecipient, bool)
}

// NewTMPXSealer builds a TMPXSealer from the supplied config, starts the
// underlying JWKS refresh goroutine bound to runCtx, and validates the
// initial key fetch. Returns (nil, nil) when TMPX is not configured
// (every relevant field empty). Returns an error when the configuration
// is partially set, the initial JWKS fetch fails, the JWKS publishes no
// recipient key, or the caller has not acknowledged the SHA-512 stub.
//
// runCtx governs the long-lived refresh goroutine; cancel it during
// shutdown to drain.
//
// The background refresh runs under safeGo: a panic inside the upstream
// library is logged at ERROR and recorded on
// recorder.BackgroundPanic("tmpx-jwks-refresh") rather than taking down
// the process. recorder may be nil for callers without observability.
func NewTMPXSealer(runCtx context.Context, cfg TMPXConfig, logger *slog.Logger, recorder Recorder) (*TMPXSealer, error) {
	configured := cfg.EncryptJWKSURL != "" || cfg.Country != "" || cfg.Priority != ""
	if !configured {
		return nil, nil
	}
	if cfg.EncryptJWKSURL == "" || cfg.Country == "" {
		return nil, errors.New("TMPX requires TMPX_ENCRYPT_JWKS_URL and TMPX_COUNTRY")
	}
	if !cfg.ReferenceStubAck {
		return nil, errors.New("TMPX is configured but uses a SHA-512 stub that is NOT interoperable with any real buyer master; set TMPX_REFERENCE_STUB_ACK=true to acknowledge")
	}
	if logger == nil {
		logger = slog.Default()
	}
	store, err := tmproto.NewJWKSStore(tmproto.JWKSStoreOptions{
		URL:             cfg.EncryptJWKSURL,
		RefreshInterval: cfg.EncryptJWKSTTL,
	})
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(runCtx, 10*time.Second)
	defer cancel()
	if err := store.Refresh(fetchCtx); err != nil {
		return nil, fmt.Errorf("initial TMPX JWKS fetch from %s: %w", cfg.EncryptJWKSURL, err)
	}
	if _, ok := store.CurrentEncryptionRecipient(); !ok {
		return nil, fmt.Errorf("TMPX JWKS at %s does not publish an adcp_use=tmpx-encrypt key", cfg.EncryptJWKSURL)
	}
	safeGo(logger, recorder, "tmpx-jwks-refresh", func() {
		if err := store.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("TMPX JWKS Run terminated", "url", cfg.EncryptJWKSURL, "error", err)
		}
	})
	order, err := parseTmpxPriority(cfg.Priority)
	if err != nil {
		return nil, err
	}
	logger.Warn("TMPX generation enabled with reference SHA-512 stub — buyer masters will not be able to decode these tokens",
		"country", cfg.Country)
	return &TMPXSealer{country: cfg.Country, encStore: store, priority: order}, nil
}

// Seal produces an HPKE TMPX token containing the resolved identities.
// Identities whose UIDType has no TMPX type-ID mapping are dropped per the
// spec's forward-compatibility rule. When the sealer was constructed with
// a non-empty Priority, entries are sorted by priority and the
// highest-priority prefix that fits the TmpxMaxWireBytes (255) budget is
// included; identities whose UIDType is not in the priority list are
// excluded entirely. When Priority is empty the spec forbids arbitrary
// truncation — an over-budget set returns an error.
//
// Returns "" without error when no identity is encodable (e.g. the
// request carried only UID types with no TMPX mapping).
func (s *TMPXSealer) Seal(ids []tmproto.IdentityToken) (string, error) {
	recipient, ok := s.encStore.CurrentEncryptionRecipient()
	if !ok {
		return "", errors.New("no TMPX encryption recipient currently published — buyer JWKS missing adcp_use=tmpx-encrypt key")
	}
	entries, err := s.selectEntries(ids)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	plaintext, err := tmproto.EncodeTmpxPlaintext(s.country, entries, time.Now())
	if err != nil {
		return "", err
	}
	return tmproto.SealTmpx(recipient, nil, plaintext)
}

// parseTmpxPriority parses a comma-separated list of UID type names into the
// ordered slice used by selectEntries. Whitespace around tokens is tolerated;
// unknown UID types are rejected (a typo would silently drop identities).
func parseTmpxPriority(s string) ([]tmproto.UIDType, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]tmproto.UIDType, 0, len(parts))
	seen := make(map[tmproto.UIDType]bool, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		uid := tmproto.UIDType(name)
		if _, ok := uidToTmpxTypeID[uid]; !ok {
			return nil, fmt.Errorf("TMPX_PRIORITY entry %q is not a TMPX-encodable uid_type", name)
		}
		if seen[uid] {
			return nil, fmt.Errorf("TMPX_PRIORITY entry %q appears more than once", name)
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out, nil
}

// uidToTmpxTypeID maps spec UID types to TMPX type-ID registry entries.
var uidToTmpxTypeID = map[tmproto.UIDType]tmproto.TmpxTypeID{
	tmproto.UIDTypeUID2:                tmproto.TmpxTypeUID2,
	tmproto.UIDTypeEUID:                tmproto.TmpxTypeEUID,
	tmproto.UIDTypeID5:                 tmproto.TmpxTypeID5,
	tmproto.UIDTypeRampID:              tmproto.TmpxTypeRampID,
	tmproto.UIDTypeRampIDDerived:       tmproto.TmpxTypeRampIDDerived,
	tmproto.UIDTypeMAID:                tmproto.TmpxTypeMAID,
	tmproto.UIDTypePairID:              tmproto.TmpxTypePairID,
	tmproto.UIDTypeHashedEmail:         tmproto.TmpxTypeHashedEmail,
	tmproto.UIDTypePublisherFirstParty: tmproto.TmpxTypePublisherFirstParty,
}

// selectEntries returns the ordered TmpxEntries that Seal will encode:
// mappable UIDTypes filtered through the operator-configured priority list,
// sorted by priority (highest first), then truncated to fit the
// TmpxMaxWireBytes budget. The budget is computed against the spec-defined
// TmpxMaxKidLen rather than the currently advertised kid — a JWKS rotation
// can change the kid length between seals, and a prefix that just fits
// today must still fit if the kid grows from 1 to 8 chars at the next
// refresh. When the sealer has no priority configured and the candidates
// don't all fit, returns an error — the spec forbids arbitrary truncation.
func (s *TMPXSealer) selectEntries(ids []tmproto.IdentityToken) ([]tmproto.TmpxEntry, error) {
	type candidate struct {
		priority int
		entry    tmproto.TmpxEntry
	}
	candidates := make([]candidate, 0, len(ids))
	for _, id := range ids {
		typeID, ok := uidToTmpxTypeID[id.UIDType]
		if !ok {
			continue
		}
		p := indexOfUIDType(s.priority, id.UIDType)
		if len(s.priority) > 0 && p < 0 {
			continue
		}
		bin, err := stubBinaryToken(typeID, id.UserToken)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{priority: p, entry: tmproto.TmpxEntry{TypeID: typeID, Token: bin}})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(s.priority) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].priority < candidates[j].priority
		})
	}

	entries := make([]tmproto.TmpxEntry, 0, len(candidates))
	usedBytes := 0
	for _, c := range candidates {
		need := 1 + len(c.entry.Token)
		nextWire := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes+need)
		if nextWire > tmproto.TmpxMaxWireBytes {
			if len(s.priority) == 0 {
				return nil, fmt.Errorf("tmpx wire size %d exceeds %d-byte budget and no TMPX_PRIORITY configured: spec forbids arbitrary truncation",
					nextWire, tmproto.TmpxMaxWireBytes)
			}
			break
		}
		entries = append(entries, c.entry)
		usedBytes += need
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("tmpx wire budget %d cannot fit even the highest-priority entry", tmproto.TmpxMaxWireBytes)
	}
	return entries, nil
}

// indexOfUIDType returns the position of uid in list, or -1 if absent.
func indexOfUIDType(list []tmproto.UIDType, uid tmproto.UIDType) int {
	for i, u := range list {
		if u == uid {
			return i
		}
	}
	return -1
}

// stubBinaryToken converts a string user_token to the binary representation
// TMPX expects for the given type ID. Reference impl only: hashes the source
// string with SHA-512 and truncates to the spec-required byte length. Real
// buyer deployments decode tokens per source-graph encoding.
func stubBinaryToken(typeID tmproto.TmpxTypeID, token string) ([]byte, error) {
	size, ok := tmproto.TmpxTokenSize(typeID)
	if !ok {
		return nil, fmt.Errorf("unknown TMPX type id %d", typeID)
	}
	h := sha512.Sum512([]byte(token))
	out := make([]byte, size)
	copy(out, h[:size])
	return out, nil
}
