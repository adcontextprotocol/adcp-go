package audience

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/adcontextprotocol/adcp-go/targeting/identityhash"
)

// userKeyPrefix is the constant prefix for user-keyed audience hashes.
// The hash field is the audience ID; the value is the score.
const userKeyPrefix = "audience:user:"

// Service is the high-level audience membership API. Callers pass raw user
// identities (id5, MAID, etc.); the Service hashes them and writes to the
// user-keyed hash. Audience-side enumeration of members is the caller's
// responsibility — Service does not maintain a reverse index.
type Service struct {
	store Store
}

// New constructs a Service backed by the provided Store.
func New(store Store) *Service {
	return &Service{store: store}
}

// Upsert applies one audience's adds and removes. Removes are processed
// before adds, so when a single Upsert contains both Add and Remove for the
// same (user, audience) pair, the Add wins.
//
// On error the store may be partially updated. Every operation is idempotent,
// so callers SHOULD retry the same Upsert to converge.
func (s *Service) Upsert(ctx context.Context, upsert AudienceUpsert) error {
	if len(upsert.Add) == 0 && len(upsert.Remove) == 0 {
		return nil
	}
	return s.UpsertBatch(ctx, []AudienceUpsert{upsert})
}

// UpsertBatch applies upserts for multiple audiences. Removes for every
// audience run before adds, so within a single batch an Add for a (user,
// audience) pair wins over a Remove for the same pair.
//
// On error the store may be partially updated. Every operation is idempotent,
// so callers SHOULD retry the same batch to converge.
func (s *Service) UpsertBatch(ctx context.Context, upserts []AudienceUpsert) error {
	if len(upserts) == 0 {
		return nil
	}
	for _, u := range upserts {
		for _, m := range u.Add {
			if !isFiniteScore(m.Score) {
				return fmt.Errorf("audience: non-finite score %g for member of %s", m.Score, u.AudienceID)
			}
		}
	}

	var hsetItems []HSetItem
	hdelByUserKey := make(map[string][]string)

	for _, u := range upserts {
		for _, m := range u.Add {
			hsetItems = append(hsetItems, HSetItem{
				Key:   userKey(identityhash.Hash(m.UserToken)),
				Field: u.AudienceID,
				Value: encodeScore(m.Score),
			})
		}
		for _, token := range u.Remove {
			k := userKey(identityhash.Hash(token))
			hdelByUserKey[k] = append(hdelByUserKey[k], u.AudienceID)
		}
	}

	if len(hdelByUserKey) > 0 {
		hdelItems := make([]HDelItem, 0, len(hdelByUserKey))
		for userHashKey, fields := range hdelByUserKey {
			hdelItems = append(hdelItems, HDelItem{Key: userHashKey, Fields: fields})
		}
		if err := s.store.HDelBatch(ctx, hdelItems); err != nil {
			return fmt.Errorf("audience: hdel batch: %w", err)
		}
	}
	if len(hsetItems) > 0 {
		if err := s.store.HSetBatch(ctx, hsetItems); err != nil {
			return fmt.Errorf("audience: hset batch: %w", err)
		}
	}
	return nil
}

// IsMemberBatch checks one (user, audience) pair per lookup. Result order
// matches lookups. Use this even for single checks — it pipelines correctly
// for batches and is the only public membership-check entry point.
func (s *Service) IsMemberBatch(ctx context.Context, lookups []MembershipLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	storeLookups := make([]HLookup, len(lookups))
	for i, l := range lookups {
		storeLookups[i] = HLookup{Key: userKey(identityhash.Hash(l.UserToken)), Field: l.AudienceID}
	}
	return s.store.HExistsBatch(ctx, storeLookups)
}

// Memberships returns every audience userToken is a member of, mapped to
// the stored score. Score 0 means membership-only. Returns an empty (non-nil)
// map when the user has no memberships.
func (s *Service) Memberships(ctx context.Context, userToken string) (map[string]float64, error) {
	raw, err := s.store.HGetAll(ctx, userKey(identityhash.Hash(userToken)))
	if err != nil {
		return nil, err
	}
	return decodeScoreMap(raw), nil
}

// MembershipsBatch returns memberships for each user token in order. Missing
// users produce empty (non-nil) maps at their index.
func (s *Service) MembershipsBatch(ctx context.Context, userTokens []string) ([]map[string]float64, error) {
	if len(userTokens) == 0 {
		return nil, nil
	}
	keys := make([]string, len(userTokens))
	for i, t := range userTokens {
		keys[i] = userKey(identityhash.Hash(t))
	}
	raws, err := s.store.HGetAllBatch(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]float64, len(raws))
	for i, r := range raws {
		out[i] = decodeScoreMap(r)
	}
	return out, nil
}

func userKey(hash string) string { return userKeyPrefix + hash }

// isFiniteScore rejects NaN and ±Inf. Callers MUST send finite scores so the
// string round-trip via FormatFloat/ParseFloat is unambiguous.
func isFiniteScore(score float64) bool {
	return !math.IsNaN(score) && !math.IsInf(score, 0)
}

// encodeScore renders score as a decimal string. Score 0 becomes "0".
// Caller has already validated finiteness via isFiniteScore.
func encodeScore(score float64) string {
	return strconv.FormatFloat(score, 'g', -1, 64)
}

// decodeScoreMap converts the raw HGETALL result into a typed score map.
// Unparseable values default to 0 (membership-only). Always returns a
// non-nil map, even when raw is empty.
func decodeScoreMap(raw map[string]string) map[string]float64 {
	out := make(map[string]float64, len(raw))
	for audienceID, v := range raw {
		score, _ := strconv.ParseFloat(v, 64)
		out[audienceID] = score
	}
	return out
}
