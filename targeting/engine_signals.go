package targeting

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// hasAnySignalProfile reports whether any candidate config carries a
// non-empty ContextSignals profile. The signal-eval path is skipped
// entirely when this returns false so requests against packages with
// no signal targeting pay no extraction or planning cost.
func hasAnySignalProfile(configs []*PackageContextConfig) bool {
	for _, c := range configs {
		if c != nil && !c.ContextSignals.IsEmpty() {
			return true
		}
	}
	return false
}

// extractSignalLookupData is the per-request projection of
// ContextMatchRequest onto the KeyType axis the signal store consults.
// Built once per request; reused for both PlanLookup (key collection)
// and MatchProfile (per-package evaluation), so the same key bytes are
// reproduced both times without re-walking the request.
//
// Only context attributes are surfaced; identity dimensions never
// appear here, so a misconfigured cfg cannot key an MGet on user data
// even if the engine's IsAllowed gate is ever bypassed downstream.
func (e *ContextEngine) extractSignalLookupData(req *tmproto.ContextMatchRequest, country string) signalstore.LookupData {
	data := make(signalstore.LookupData, len(signalstore.AllowedKeyTypes()))

	for _, ref := range req.ArtifactRefs {
		if ref.Value == "" {
			continue
		}
		// `url` refs project ONLY onto url_hash via the spec-canonical
		// hashing path. The KeyType taxonomy excludes raw URLs to
		// avoid delimiter collisions in the Valkey key shape, so a
		// publisher sending a raw URL gets it normalized to the hash
		// the writer keys on.
		if ref.Type == tmproto.ArtifactRefTypeURL {
			appendUnique(data, signalstore.KeyTypeURLHash, tmproto.HashURL(ref.Value))
			continue
		}
		kt, ok := artifactRefKeyType(ref.Type)
		if !ok {
			continue
		}
		appendUnique(data, kt, ref.Value)
	}

	if country != "" {
		appendUnique(data, signalstore.KeyTypeCountry, country)
	}
	if region, ok := req.Geo["region"].(string); ok && region != "" {
		appendUnique(data, signalstore.KeyTypeRegion, region)
	}
	if metro := metroFromGeo(req.Geo); metro != "" {
		appendUnique(data, signalstore.KeyTypeMetro, metro)
	}

	// Topic values carry the publisher-declared taxonomy as part of
	// the value (`{source}:{id}:{topicID}`, via topicstore.Taxonomy
	// String) so the writer can disambiguate the same topic id under
	// multiple taxonomies (e.g. "sports" under iab:7 vs a custom
	// taxonomy). Topics from unaccepted taxonomies drop silently —
	// same fail-closed posture as the topic-match path.
	if cs := req.ContextSignals; cs != nil && len(cs.Topics) > 0 {
		tax := topicstore.Taxonomy{Source: cs.TaxonomySource, ID: cs.TaxonomyID}
		if e.acceptsTaxonomy(tax) {
			prefix := tax.String() + ":"
			for _, t := range cs.Topics {
				if t == "" {
					continue
				}
				appendUnique(data, signalstore.KeyTypeTopic, prefix+t)
			}
		}
	}

	if len(data) == 0 {
		return nil
	}
	return data
}

// appendUnique adds v to data[kt] iff it is not already present.
// Deduplication keeps cartesian expansion bounded when the request
// repeats the same value across multiple artifact_refs.
//
// Values containing the reserved ',' separator are dropped: the
// signal:* key encodes the value tuple comma-joined (see
// signalstore.Key), so a request value carrying a comma could shift the
// parse and shadow a legitimately-written compound-tuple key. The
// writer uses the same Key() encoding and so could never have written a
// key for a comma-bearing value anyway, making the drop a no-op for any
// value that could legitimately match.
func appendUnique(data signalstore.LookupData, kt signalstore.KeyType, v string) {
	if strings.ContainsRune(v, ',') {
		return
	}
	for _, existing := range data[kt] {
		if existing == v {
			return
		}
	}
	data[kt] = append(data[kt], v)
}

// artifactRefKeyType projects the spec's ArtifactRefType onto the
// signalstore KeyType taxonomy. Returns ok = false for ref types not
// allowed on the context-match endpoint; callers skip those refs.
func artifactRefKeyType(t tmproto.ArtifactRefType) (signalstore.KeyType, bool) {
	switch t {
	case tmproto.ArtifactRefTypeURLHash:
		return signalstore.KeyTypeURLHash, true
	case tmproto.ArtifactRefTypeEIDR:
		return signalstore.KeyTypeEIDR, true
	case tmproto.ArtifactRefTypeGracenote:
		return signalstore.KeyTypeGracenote, true
	case tmproto.ArtifactRefTypeISRC:
		return signalstore.KeyTypeISRC, true
	case tmproto.ArtifactRefTypeGTIN:
		return signalstore.KeyTypeGTIN, true
	case tmproto.ArtifactRefTypeRSSGUID:
		return signalstore.KeyTypeRSSGUID, true
	case tmproto.ArtifactRefTypeISBN:
		return signalstore.KeyTypeISBN, true
	case tmproto.ArtifactRefTypeCustom:
		return signalstore.KeyTypeCustom, true
	}
	// ArtifactRefTypeURL is intentionally absent: the engine derives
	// url_hash from raw URLs via the case above (the writer side never
	// keys on raw URLs because they collide with the signal key
	// delimiter).
	return "", false
}

// metroFromGeo flattens the spec's geo.metro sub-object
// (`{ "system": "nielsen_dma", "value": "501" }`) into a single
// "{system}:{value}" string. Returns empty when either side is
// missing or non-string so the caller can skip the metro dimension
// without surfacing a partial key.
func metroFromGeo(geo map[string]any) string {
	if len(geo) == 0 {
		return ""
	}
	m, ok := geo["metro"].(map[string]any)
	if !ok {
		return ""
	}
	system, _ := m["system"].(string)
	value, _ := m["value"].(string)
	if system == "" || value == "" {
		return ""
	}
	return system + ":" + value
}

// preloadContextConfigs fetches every candidate package's
// PackageContextConfig in a single round-trip (via the storage
// ContextConfigs batch method when implemented; falls back to
// per-package fetches otherwise). nil at index i means "no config
// stored" (the engine then skips that package). Per-package decode
// errors are recorded per metric and surfaced as nil at that index;
// the request continues with the configs it could load.
func (e *ContextEngine) preloadContextConfigs(ctx context.Context, candidatePkgIDs []string) []*PackageContextConfig {
	if len(candidatePkgIDs) == 0 {
		return nil
	}
	if batch, ok := e.storage.(contextConfigBatcher); ok {
		configs, err := batch.ContextConfigs(ctx, candidatePkgIDs)
		if err != nil {
			e.metrics.StoreError(ctx, "context_config", err)
			// Per-key errors are surfaced inside the slice as nils;
			// a whole-batch error fails-closed by returning nils for
			// every candidate (the per-package loop then skips them).
			return make([]*PackageContextConfig, len(candidatePkgIDs))
		}
		return configs
	}
	out := make([]*PackageContextConfig, len(candidatePkgIDs))
	for i, pkgID := range candidatePkgIDs {
		if err := ctx.Err(); err != nil {
			return out
		}
		cfg, ok, err := e.storage.ContextConfig(ctx, pkgID)
		if err != nil {
			e.metrics.StoreError(ctx, "context_config", err)
			continue
		}
		if !ok || cfg == nil {
			continue
		}
		out[i] = cfg
	}
	return out
}

// contextConfigBatcher is the optional batched-read extension a
// ContextStorage MAY implement. Storage backends that can MGet
// package configs in one round-trip (e.g. Valkey-backed) satisfy
// this; in-memory test fakes use the per-key fallback.
type contextConfigBatcher interface {
	ContextConfigs(ctx context.Context, packageIDs []string) ([]*PackageContextConfig, error)
}

// fetchSignalsForCandidates plans one MGet across every candidate
// package's ContextSignals profile and returns the decoded fetched
// map the engine passes to MatchProfile. Three return shapes:
//
//   - (map, nil) — fetched successfully; the map may be empty when no
//     profile produced expandable keys.
//   - (nil, nil) — there is nothing to evaluate: no candidate carries a
//     profile, or the request produced no signal lookup data
//     (len(data) == 0); callers should skip the signal stage entirely.
//   - (nil, err) — planning or MGet failed; callers MUST fail-closed
//     every package with a non-empty profile.
func (e *ContextEngine) fetchSignalsForCandidates(
	ctx context.Context,
	configs []*PackageContextConfig,
	data signalstore.LookupData,
) (map[string][]string, error) {
	if len(configs) == 0 || len(data) == 0 {
		return nil, nil
	}
	profiles := make([]*signalstore.Profile, 0, len(configs))
	for _, cfg := range configs {
		if cfg == nil || cfg.ContextSignals.IsEmpty() {
			continue
		}
		profiles = append(profiles, cfg.ContextSignals)
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	keys, err := signalstore.PlanLookup(profiles, data)
	if err != nil {
		e.metrics.StoreError(ctx, StageSignalMatch, err)
		return nil, err
	}
	if len(keys) == 0 {
		return map[string][]string{}, nil
	}
	values, err := e.storage.SignalMGet(ctx, keys...)
	if err != nil {
		e.metrics.StoreError(ctx, StageSignalMatch, err)
		return nil, err
	}
	return signalstore.DecodeValues(keys, values), nil
}

// signalsPass returns true when the package's ContextSignals profile
// (if any) accepts the request given the prefetched MGet results. A
// nil or empty profile passes vacuously. A profile with an invalid
// cfg (rejected KeyType, missing SignalID, cap-trip on expansion)
// fails-closed and logs a warn: misconfiguration must not silently
// let a package serve. fetchErr non-nil means signal evaluation
// hit a storage or planning error; every non-empty profile
// fails-closed in that case (matching the URL filter / topic match
// pattern elsewhere in the engine).
func (e *ContextEngine) signalsPass(
	ctx context.Context,
	cfg *PackageContextConfig,
	pkgID string,
	data signalstore.LookupData,
	fetched map[string][]string,
	fetchErr error,
	logger *slog.Logger,
) bool {
	p := cfg.ContextSignals
	if p.IsEmpty() {
		return true
	}
	if fetchErr != nil {
		e.metrics.ContextEvaluated(ctx, StageSignalMatch, false)
		return false
	}
	pass, err := p.MatchProfile(data, fetched)
	if err != nil {
		if logger != nil {
			logger.Warn("signal targeting profile fail-closed",
				"package_id", pkgID,
				"error", err.Error(),
				"max_keys_per_cfg", signalstore.MaxKeysPerCfg(),
				"unsafe", errors.Is(err, signalstore.ErrCfgUnsafe),
			)
		}
		e.metrics.ContextEvaluated(ctx, StageSignalMatch, false)
		return false
	}
	e.metrics.ContextEvaluated(ctx, StageSignalMatch, pass)
	return pass
}
