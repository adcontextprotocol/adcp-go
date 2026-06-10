package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/liveramp"
	"github.com/adcontextprotocol/adcp-go/targeting/tmpxdecoders"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// LiveRampSidecar is the interface NewTMPXSealer accepts for decoding
// RampID identities. The production implementation lives in
// targeting/internal/liveramp; the interface is declared here so tests can
// supply a fake without spinning up an httptest server and without
// importing the internal package.
type LiveRampSidecar interface {
	MappedID(ctx context.Context, env string) (string, error)
}

// Compile-time assertion: the production LiveRamp client satisfies
// LiveRampSidecar. Lives next to the interface so a method-set drift fails
// at the point of declaration rather than at the setup wire-up.
var _ LiveRampSidecar = (*liveramp.Client)(nil)

// TmpxTokenDecoder converts a user_token string supplied on an inbound
// IdentityToken into the binary form TMPX packs into its encrypted
// plaintext. Concrete implementations live in
// targeting/internal/tmpxdecoders; the interface is declared here so tests
// can supply fakes without taking a dependency on the internal package.
//
// Decode receives the request's context.Context so HTTP-backed decoders
// (LiveRamp sidecar, identity graph lookups) can honor the caller's
// deadline. Format-only decoders ignore it.
//
// Implementations must return exactly tmproto.TmpxTokenSize(typeID) bytes
// for the UID type they are registered against; selectEntries validates the
// length before adding the entry to the wire payload. A decoder may return
// ErrDropIdentity to signal that the user_token was consumed but the
// resulting identity should be omitted from the wire (LiveRamp miss is the
// canonical example).
type TmpxTokenDecoder interface {
	Decode(ctx context.Context, userToken string) ([]byte, error)
}

// ErrDropIdentity is the sentinel a TmpxTokenDecoder returns to signal
// "consumed the input, but produce no entry for this identity" — e.g. the
// LiveRamp sidecar returned no mapping for a RampID. Decode treats it as
// a silent drop, not an error.
var ErrDropIdentity = tmpxdecoders.ErrDropFromSeal

// DecodedIdentity is the per-request decode result for one inbound
// IdentityToken. Both the TMPX seal path and the audience/fcap lookup
// path consume this so identities are decoded exactly once per request
// (a hot concern for LiveRamp-backed RampIDs that make a sidecar call)
// and both paths see consistent drop decisions.
//
// Bytes is the canonical decoded form the buyer master keys its
// downstream stores on. A nil or zero-length slice means the identity
// was dropped during decode (no mapped type, no decoder, sentinel,
// transport error, size mismatch) — selectEntries and
// audienceEligibleIdentities skip such entries.
type DecodedIdentity struct {
	UIDType tmproto.UIDType
	Bytes   []byte
}

// audienceEligibleIdentities projects a decoded slice onto the
// IdentityToken shape the audience and frequency-cap services expect.
// Identities the Decode pass dropped (Bytes == nil) are skipped so
// downstream Valkey lookups don't waste round trips on keys the buyer
// master will never have populated.
//
// UserToken is set to the canonical lowercase-hex form of the decoded
// bytes — matching ExposureLog.user_token per its proto spec, which is
// the keying convention downstream marker writers and buyer-master
// readers honor.
func audienceEligibleIdentities(decoded []DecodedIdentity) []tmproto.IdentityToken {
	out := make([]tmproto.IdentityToken, 0, len(decoded))
	for _, d := range decoded {
		if len(d.Bytes) == 0 {
			continue
		}
		out = append(out, tmproto.IdentityToken{
			UIDType:   d.UIDType,
			UserToken: tmpxdecoders.Canonical(d.Bytes),
		})
	}
	return out
}

// TMPXSealer holds the resolved TMPX recipient state used to seal tokens
// alongside identity-match responses. Construct with NewTMPXSealer; call
// Seal at request time to produce a per-request HPKE token.
//
// String→binary conversion is delegated to a per-UID-type registry of
// TmpxTokenDecoders (default constructed by NewTMPXSealer from
// targeting/internal/tmpxdecoders). UID types without a registered decoder
// are silently dropped from both the TMPX wire and the audience/fcap
// shadow request.
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

	// decoders maps each TMPX-encodable UID type to the adapter that
	// converts user_token strings into the binary form TMPX expects.
	// Missing entries are treated as silent drops by Decode — operators
	// opt UID types in (RampID via LiveRamp) or out without failing the
	// sealer.
	decoders map[tmproto.UIDType]TmpxTokenDecoder

	// logger and recorder are used by Decode to surface per-identity
	// drop events. Both may be nil; helpers fall back to slog.Default() and
	// a no-op recorder respectively.
	logger   *slog.Logger
	recorder Recorder
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
// is partially set, the initial JWKS fetch fails, or the JWKS publishes
// no recipient key.
//
// The decoder registry is intentionally allowed to be incomplete: any UID
// type in uidToTmpxTypeID without a registered decoder is treated as a
// silent drop at Seal time. Decode counts these drops via the `no_decoder`
// reason on the TmpxIdentityDrop counter so operators can see them.
//
// lrClient is the optional LiveRamp sidecar used to decode RampID /
// RampIDDerived identities. When nil, those UID types have no decoder
// registered and selectEntries drops them silently from the TMPX wire.
// Pass nil — not a typed nil pointer to a concrete client — to disable
// (Go's interface-nil rules treat a typed nil as non-nil).
//
// runCtx governs the long-lived refresh goroutine; cancel it during
// shutdown to drain.
//
// The background refresh runs under safeGo: a panic inside the upstream
// library is logged at ERROR and recorded on
// recorder.BackgroundPanic("tmpx-jwks-refresh") rather than taking down
// the process. recorder may be nil for callers without observability.
func NewTMPXSealer(runCtx context.Context, cfg TMPXConfig, lrClient LiveRampSidecar, logger *slog.Logger, recorder Recorder) (*TMPXSealer, error) {
	configured := cfg.EncryptJWKSURL != "" || cfg.Country != "" || cfg.Priority != ""
	if !configured {
		return nil, nil
	}
	if cfg.EncryptJWKSURL == "" || cfg.Country == "" {
		return nil, errors.New("TMPX requires TMPX_ENCRYPT_JWKS_URL and TMPX_COUNTRY")
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
	// The decoder package's LiveRampClient interface is structurally
	// identical to LiveRampSidecar, so any concrete type that satisfies one
	// satisfies the other. We assign through an interface variable to keep
	// the nil check correct (avoid the typed-nil trap).
	var decoderAdapter tmpxdecoders.LiveRampClient
	if lrClient != nil {
		decoderAdapter = lrClient
	}
	decoders := buildTmpxDecoders(tmpxdecoders.RegistryOptions{LiveRampClient: decoderAdapter})
	logDecoderLayout(logger, cfg.Country, decoders, order)
	return &TMPXSealer{
		country:  cfg.Country,
		encStore: store,
		priority: order,
		decoders: decoders,
		logger:   logger,
		recorder: recorder,
	}, nil
}

// log returns the sealer's logger, falling back to slog.Default() when none
// was configured. Tests that construct TMPXSealer directly without a
// logger still get sensible output.
func (s *TMPXSealer) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// recordDrop emits a TmpxIdentityDrop counter for an identity that did not
// make it onto the TMPX wire. Safe to call when recorder is nil.
func (s *TMPXSealer) recordDrop(ctx context.Context, reason string, uid tmproto.UIDType) {
	if s.recorder == nil {
		return
	}
	s.recorder.TmpxIdentityDrop(ctx, reason, string(uid))
}

// buildTmpxDecoders projects the tmpxdecoders.Decoder map onto the
// identityagent.TmpxTokenDecoder map TMPXSealer holds. UID types absent
// from the registry (typically RampID / RampIDDerived when the LiveRamp
// sidecar is disabled) are intentionally omitted — selectEntries treats
// missing decoders as a silent drop signal.
func buildTmpxDecoders(opts tmpxdecoders.RegistryOptions) map[tmproto.UIDType]TmpxTokenDecoder {
	raw := tmpxdecoders.NewDefaultRegistry(opts)
	out := make(map[tmproto.UIDType]TmpxTokenDecoder, len(raw))
	for uid, dec := range raw {
		out[uid] = dec
	}
	return out
}

// logDecoderLayout emits startup lines describing which TMPX-encodable UID
// types have a registered decoder, which will be dropped at decode time,
// and which priority entries are unreachable because their decoder is
// missing. Operators read this to confirm a LiveRamp misconfiguration
// didn't silently disable RampID handling and that their TMPX_PRIORITY
// list isn't half-dead.
func logDecoderLayout(logger *slog.Logger, country string, decoders map[tmproto.UIDType]TmpxTokenDecoder, priority []tmproto.UIDType) {
	enabled := make([]tmproto.UIDType, 0, len(decoders))
	dropped := make([]tmproto.UIDType, 0, len(uidToTmpxTypeID))
	for uid := range uidToTmpxTypeID {
		if !inboundDecodable(uid) {
			continue
		}
		if _, ok := decoders[uid]; ok {
			enabled = append(enabled, uid)
		} else {
			dropped = append(dropped, uid)
		}
	}
	sortUIDs(enabled)
	sortUIDs(dropped)
	attrs := []any{"country", country, "enabled_uid_types", joinUIDs(enabled)}
	if len(dropped) > 0 {
		attrs = append(attrs, "dropped_uid_types", joinUIDs(dropped))
	}
	switch {
	case len(enabled) == 0:
		logger.Warn("TMPX generation enabled but no UID type has a decoder — every identity will be dropped at decode time", attrs...)
	case len(dropped) > 0:
		logger.Info("TMPX generation enabled; some UID types will be dropped from the wire (no decoder configured)", attrs...)
	default:
		logger.Info("TMPX generation enabled with decoders for every UID type", attrs...)
	}

	unreachable := make([]tmproto.UIDType, 0, len(priority))
	for _, uid := range priority {
		if _, ok := decoders[uid]; !ok {
			unreachable = append(unreachable, uid)
		}
	}
	if len(unreachable) > 0 {
		logger.Warn("TMPX_PRIORITY contains UID types without a configured decoder — those entries are unreachable and will be dropped at decode time",
			"unreachable_uid_types", joinUIDs(unreachable))
	}
}

func sortUIDs(s []tmproto.UIDType) {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
}

func joinUIDs(s []tmproto.UIDType) string {
	parts := make([]string, len(s))
	for i, u := range s {
		parts[i] = string(u)
	}
	return strings.Join(parts, ",")
}

// Decode runs every supplied identity through its UID-type decoder and
// returns the results in input order. Per-identity drops (unmapped, no
// decoder, decoder drop sentinel, decoder error, size mismatch) are
// surfaced on the recorder's TmpxIdentityDrop counter with these reasons:
//
//   - unmapped       — UIDType has no TMPX type-ID mapping
//   - no_decoder     — UIDType mapped but no decoder configured (LiveRamp off)
//   - decoder_drop   — decoder returned ErrDropIdentity (LiveRamp miss)
//   - decoder_error  — decoder returned a transport/parse error
//   - size_mismatch  — decoder produced wrong byte length for the type
//
// Dropped identities have Bytes == nil in the returned slice; downstream
// stages (SealDecoded, audience/fcap lookups) skip them silently.
//
// In production the per-request decode pass is owned by the canonicalizer
// (see IdentityCanonicalizer.Decode); the handler hands SealDecoded the
// already-decoded slice so LiveRamp-backed RampIDs make at most one
// sidecar call per request. This method remains so callers (including the
// Seal convenience wrapper and the reference / test code) that already
// hold a *TMPXSealer can still decode against the sealer's own registry
// without taking a dependency on IdentityCanonicalizer.
func (s *TMPXSealer) Decode(ctx context.Context, ids []tmproto.IdentityToken) []DecodedIdentity {
	return decodeIdentities(ctx, ids, s.decoders, s.log(), s.recordDrop)
}

// Seal is a convenience that runs Decode and then SealDecoded. Callers
// that share decoded identities across the TMPX and audience/fcap paths
// should call Decode and SealDecoded directly so the per-request decode
// pass happens exactly once.
func (s *TMPXSealer) Seal(ctx context.Context, ids []tmproto.IdentityToken) (string, error) {
	return s.SealDecoded(ctx, s.Decode(ctx, ids))
}

// SealDecoded produces an HPKE TMPX token from a pre-decoded identity
// slice. Identities with no Bytes (those Decode dropped) are skipped.
//
// When TMPX_PRIORITY is set, entries are packed in priority order and
// trailing ones that don't fit the 255-byte wire budget are dropped.
// When TMPX_PRIORITY is empty, the spec's "no arbitrary truncation" rule
// applies: if the set overflows the budget, SealDecoded returns an error
// and the handler omits TMPX from the response.
//
// Returns "" without error when no identity is encodable.
func (s *TMPXSealer) SealDecoded(ctx context.Context, decoded []DecodedIdentity) (string, error) {
	// Sealing is sub-millisecond CPU work and won't observe ctx mid-flight,
	// but checking at the top closes the seal-after-timeout window where
	// the handler's deadline already fired during Decode.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	recipient, ok := s.encStore.CurrentEncryptionRecipient()
	if !ok {
		return "", errors.New("no TMPX encryption recipient currently published — buyer JWKS missing adcp_use=tmpx-encrypt key")
	}
	entries, err := s.selectEntries(decoded)
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
	tmproto.UIDTypeWorldIDNullifier:    tmproto.TmpxTypeWorldIDNullifier,
}

// inboundDecodable reports whether a TMPX-encodable UID type is expected to
// have a decoder in the inbound registry. World ID nullifiers are
// verify-before-trust and never decoded from inbound identities, so their
// absence from the inbound registry is by design, not a misconfiguration —
// the startup coverage logs exclude them so an operator doesn't read them as
// a dropped type.
func inboundDecodable(uid tmproto.UIDType) bool {
	return uid != tmproto.UIDTypeWorldIDNullifier
}

// worldIDNullifierEncoder converts a verifier-derived nullifier string into
// its TMPX token bytes. It is intentionally not part of the inbound decoder
// registry (see tmpxdecoders.NewDefaultRegistry) so it is unreachable from
// sender-asserted identities; the verified-identity stage is its only caller.
var worldIDNullifierEncoder = tmpxdecoders.WorldIDNullifier{}

// verifiedIdentityEntries converts the verifier-derived identities into
// pre-decoded TMPX entries. These bypass the inbound decoder registry by
// design — verify-before-trust: a World ID nullifier reaches the wire ONLY
// after the verified-identity stage validated its proof. An inbound,
// sender-asserted world_id_nullifier token has no registered decoder and is
// dropped at Decode, so it can never arrive here.
//
// A nullifier that fails to encode (malformed/oversized) is dropped with a
// decoder_error counter rather than failing the whole seal — one bad
// nullifier must not suppress the other resolved identities.
func (s *TMPXSealer) verifiedIdentityEntries(ctx context.Context, verified []targeting.VerifiedIdentity) []DecodedIdentity {
	if len(verified) == 0 {
		return nil
	}
	out := make([]DecodedIdentity, 0, len(verified))
	for _, vi := range verified {
		if vi.Nullifier == "" {
			continue
		}
		b, err := worldIDNullifierEncoder.Token(ctx, vi.RelyingPartyID, vi.Nullifier)
		if err != nil {
			s.recordDrop(ctx, TmpxDropDecoderError, tmproto.UIDTypeWorldIDNullifier)
			s.log().Warn("world id nullifier encode failed — dropping from tmpx", "error", err)
			continue
		}
		out = append(out, DecodedIdentity{UIDType: tmproto.UIDTypeWorldIDNullifier, Bytes: b})
	}
	return out
}

// selectEntries packs already-decoded identities into the wire TmpxEntry
// list under the TmpxMaxWireBytes budget. Identities the Decode pass
// dropped (Bytes == nil) are skipped here; their drop counters fired in
// Decode. Identities whose UIDType is not in TMPX_PRIORITY (when priority
// is configured) are also skipped here without a counter — operators set
// priority explicitly, so unreachable entries are intentional and surfaced
// at startup by logDecoderLayout, not per-request.
//
// The budget is computed against TmpxMaxKidLen rather than the currently
// advertised kid: a JWKS rotation can change the kid length between
// seals, and a prefix that just fits today must still fit if the kid
// grows from 1 to 8 chars at the next refresh.
//
// When TMPX_PRIORITY is empty and the surviving set overflows the wire
// budget, returns an error — the spec forbids arbitrary truncation.
// When TMPX_PRIORITY is set, trailing entries that don't fit are
// dropped; if even the highest-priority entry doesn't fit, that's a
// configuration bug and is also surfaced as an error.
func (s *TMPXSealer) selectEntries(decoded []DecodedIdentity) ([]tmproto.TmpxEntry, error) {
	type candidate struct {
		d        DecodedIdentity
		typeID   tmproto.TmpxTypeID
		priority int
	}
	// Invariant: Decode only sets Bytes when uidToTmpxTypeID[d.UIDType] is
	// present, so every survivor here has a valid TmpxTypeID. We rely on
	// this rather than re-checking the mapping.
	survivors := make([]candidate, 0, len(decoded))
	for _, d := range decoded {
		if len(d.Bytes) == 0 {
			continue
		}
		typeID := uidToTmpxTypeID[d.UIDType]
		p := indexOfUIDType(s.priority, d.UIDType)
		if len(s.priority) > 0 && p < 0 {
			continue
		}
		survivors = append(survivors, candidate{d: d, typeID: typeID, priority: p})
	}
	if len(survivors) == 0 {
		return nil, nil
	}
	if len(s.priority) > 0 {
		sort.SliceStable(survivors, func(i, j int) bool {
			return survivors[i].priority < survivors[j].priority
		})
	}

	entries := make([]tmproto.TmpxEntry, 0, len(survivors))
	usedBytes := 0
	budgetOverflowed := false
	for _, c := range survivors {
		need := 1 + len(c.d.Bytes)
		nextWire := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes+need)
		if nextWire > tmproto.TmpxMaxWireBytes {
			if len(s.priority) == 0 {
				return nil, fmt.Errorf("tmpx wire size %d exceeds %d-byte budget and no TMPX_PRIORITY configured: spec forbids arbitrary truncation",
					nextWire, tmproto.TmpxMaxWireBytes)
			}
			budgetOverflowed = true
			break
		}
		entries = append(entries, tmproto.TmpxEntry{TypeID: c.typeID, Token: c.d.Bytes})
		usedBytes += need
	}
	if len(entries) == 0 && budgetOverflowed {
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
