package identityagent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/adcontextprotocol/adcp-go/targeting/tmpxdecoders"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// IdentityCanonicalizer decodes inbound IdentityToken.UserToken strings into
// their canonical binary form so audience and frequency-cap lookups key on
// the same shape ExposureLog.user_token publishes downstream — lowercase-hex
// of the decoded bytes — regardless of whether TMPX sealing is enabled on
// this deployment.
//
// It exists as a sibling of TMPXSealer so the canonicalization step runs
// independently of TMPX recipient configuration. Deployments that don't
// emit TMPX tokens still benefit from the consistent key form; deployments
// that do still hand the same decoded slice to the sealer so the per-request
// decode pass (in particular the LiveRamp sidecar call for RampIDs) happens
// at most once.
//
// Construct with NewIdentityCanonicalizer. A nil *IdentityCanonicalizer
// signals "no canonicalization configured" — the handler falls back to the
// legacy pass-through behavior in that case. The handler treats a non-nil
// canonicalizer with an empty decoder map the same way as a populated one:
// every identity is dropped at decode time, which matches the
// "missing-config = silent drop" pattern used elsewhere in the agent.
type IdentityCanonicalizer struct {
	// decoders maps each TMPX-encodable UID type to the adapter that
	// converts user_token strings into the canonical binary form. UID types
	// without a registered decoder are dropped at decode time so audience
	// and frequency-cap lookups don't waste round trips on keys the
	// buyer-master populator will never have published.
	decoders map[tmproto.UIDType]TmpxTokenDecoder

	// logger and recorder are used to surface per-identity drop events on
	// the same TmpxIdentityDrop counter the sealer uses — operationally
	// the signal is identical (mapping problem, decoder problem,
	// content-shape problem). Both may be nil; helpers fall back to
	// slog.Default() and a no-op recorder respectively.
	logger   *slog.Logger
	recorder Recorder
}

// NewIdentityCanonicalizer builds an IdentityCanonicalizer backed by the
// default decoder registry. lrClient is the optional LiveRamp sidecar used
// to decode RampID and RampID-derived identities. When nil, those UID
// types have no decoder registered and identities of those types are
// silently dropped from the canonicalized shadow request (the same
// behavior the TMPX path applies). Pass nil — not a typed nil pointer to
// a concrete client — to disable (Go's interface-nil rules treat a typed
// nil as non-nil).
//
// Returns a populated canonicalizer for any LiveRamp configuration: even
// without the sidecar the default registry yields the format-only decoders
// (MAID, HashedEmail, ID5) which cover the common case. Callers that
// explicitly want to opt out of canonicalization entirely (and preserve
// the legacy "publisher wire string flows through to audience/fcap"
// behavior) should pass a nil *IdentityCanonicalizer to the handler.
func NewIdentityCanonicalizer(lrClient LiveRampSidecar, uid2Client, euidClient UID2Operator, logger *slog.Logger, recorder Recorder) *IdentityCanonicalizer {
	decoders := buildTmpxDecoders(tmpxdecoders.RegistryOptions{
		LiveRampClient: adaptLiveRamp(lrClient),
		UID2Client:     adaptUID2(uid2Client),
		EUIDClient:     adaptUID2(euidClient),
	})
	if logger != nil {
		logCanonicalizerLayout(logger, decoders)
	}
	return &IdentityCanonicalizer{
		decoders: decoders,
		logger:   logger,
		recorder: recorder,
	}
}

// log returns the canonicalizer's logger, falling back to slog.Default()
// when none was configured.
func (c *IdentityCanonicalizer) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// recordDrop emits a TmpxIdentityDrop counter for an identity that did not
// produce a canonical binary form. Safe to call when recorder is nil.
// Reuses the existing TmpxIdentityDrop series — operationally the same
// signal ("this UID type wasn't decodable here") whether or not TMPX is
// enabled on the deployment.
func (c *IdentityCanonicalizer) recordDrop(ctx context.Context, reason string, uid tmproto.UIDType) {
	if c.recorder == nil {
		return
	}
	c.recorder.TmpxIdentityDrop(ctx, reason, string(uid))
}

// Decode runs every supplied identity through its UID-type decoder and
// returns the results in input order. Per-identity drops (unmapped, no
// decoder, decoder drop sentinel, decoder error, size mismatch) are
// surfaced on the recorder's TmpxIdentityDrop counter — see the constants
// in metrics.go for the reason set.
//
// Dropped identities have Bytes == nil in the returned slice; downstream
// stages (audienceEligibleIdentities, TMPXSealer.SealDecoded) skip them
// silently.
//
// Decode shares its result between the audience/fcap shadow path and the
// TMPX seal path so LiveRamp-backed RampIDs make at most one sidecar
// call per request.
func (c *IdentityCanonicalizer) Decode(ctx context.Context, ids []tmproto.IdentityToken) []DecodedIdentity {
	return decodeIdentities(ctx, ids, c.decoders, c.log(), c.recordDrop)
}

// decodeIdentities is the shared decode loop used by both
// IdentityCanonicalizer.Decode and TMPXSealer.Decode. Extracting it keeps
// the per-identity drop bookkeeping (counter reasons, size validation)
// in one place so the canonicalization path can't silently diverge from
// the seal path.
//
// recordDrop is supplied by the caller so each owner attributes drops to
// its own recorder/logger context without this helper having to know
// which it is.
func decodeIdentities(
	ctx context.Context,
	ids []tmproto.IdentityToken,
	decoders map[tmproto.UIDType]TmpxTokenDecoder,
	logger *slog.Logger,
	recordDrop func(ctx context.Context, reason string, uid tmproto.UIDType),
) []DecodedIdentity {
	out := make([]DecodedIdentity, len(ids))
	for i, id := range ids {
		out[i].UIDType = id.UIDType
		typeID, ok := uidToTmpxTypeID[id.UIDType]
		if !ok {
			recordDrop(ctx, TmpxDropUnmapped, id.UIDType)
			continue
		}
		decoder, ok := decoders[id.UIDType]
		if !ok {
			recordDrop(ctx, TmpxDropNoDecoder, id.UIDType)
			continue
		}
		bin, err := decoder.Decode(ctx, id.UserToken)
		if err != nil {
			if errors.Is(err, ErrDropIdentity) {
				recordDrop(ctx, TmpxDropDecoderDrop, id.UIDType)
				continue
			}
			recordDrop(ctx, TmpxDropDecoderError, id.UIDType)
			logger.Warn("identity decoder error — dropping identity",
				"uid_type", id.UIDType,
				"error", err)
			continue
		}
		wantSize, _ := tmproto.TmpxTokenSize(typeID)
		if len(bin) != wantSize {
			recordDrop(ctx, TmpxDropSizeMismatch, id.UIDType)
			logger.Warn("identity decoder returned wrong byte length — dropping identity",
				"uid_type", id.UIDType,
				"got", len(bin),
				"want", wantSize)
			continue
		}
		out[i].Bytes = bin
	}
	return out
}

// logCanonicalizerLayout emits a startup line describing which UID types
// will round-trip through canonicalization and which will be dropped. The
// signal mirrors logDecoderLayout but doesn't talk about TMPX priority:
// the canonicalizer is unaware of (and unaffected by) the TMPX_PRIORITY
// ordering — every decodable identity is canonicalized.
func logCanonicalizerLayout(logger *slog.Logger, decoders map[tmproto.UIDType]TmpxTokenDecoder) {
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
	attrs := []any{"enabled_uid_types", joinUIDs(enabled)}
	if len(dropped) > 0 {
		attrs = append(attrs, "dropped_uid_types", joinUIDs(dropped))
	}
	switch {
	case len(enabled) == 0:
		logger.Warn("identity canonicalization enabled but no UID type has a decoder — every identity will be dropped at decode time", attrs...)
	case len(dropped) > 0:
		logger.Info("identity canonicalization enabled; some UID types will be dropped (no decoder configured)", attrs...)
	default:
		logger.Info("identity canonicalization enabled with decoders for every UID type", attrs...)
	}
}
