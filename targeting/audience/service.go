package audience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

const (
	// userKeyPrefix is the constant prefix for user-keyed audience hashes.
	// The hash field is the audience ID; the value is the score.
	userKeyPrefix = "audience:user:"

	// audienceKeyPrefix is the constant prefix for audience-keyed sets that
	// list every user-identity hash currently a member of the audience.
	// Used to enumerate members during DeleteAudience.
	audienceKeyPrefix = "audience:list:"
)

// Service is the high-level audience membership API. Callers pass raw user
// identities (id5, MAID, etc.); the Service hashes them and dual-writes to
// the user-keyed hash and the audience-keyed set.
type Service struct {
	store Store
}

// New constructs a Service backed by the provided Store.
func New(store Store) *Service {
	return &Service{store: store}
}

// Upsert applies one audience's adds and removes. The add side dual-writes
// each member into the user's hash and the audience's set; the remove side
// reverses both. Empty slices are no-ops; an entirely empty upsert returns
// nil without touching the store.
func (s *Service) Upsert(ctx context.Context, upsert AudienceUpsert) error {
	if len(upsert.Add) == 0 && len(upsert.Remove) == 0 {
		return nil
	}
	return s.UpsertBatch(ctx, []AudienceUpsert{upsert})
}

// UpsertBatch applies upserts for multiple audiences in a single pipelined
// round-trip per kind of operation.
func (s *Service) UpsertBatch(ctx context.Context, upserts []AudienceUpsert) error {
	if len(upserts) == 0 {
		return nil
	}

	var hsetItems []HSetItem
	addedHashesByAudience := make(map[string][]string)
	removedHashesByAudience := make(map[string][]string)
	hdelByUserKey := make(map[string][]string)

	for _, u := range upserts {
		audienceListKey := audienceKey(u.AudienceID)
		for _, m := range u.Add {
			userIdHash := hashIdentity(m.UserToken)
			userHashKey := userKey(userIdHash)
			hsetItems = append(hsetItems, HSetItem{
				Key:   userHashKey,
				Field: u.AudienceID,
				Value: encodeScore(m.Score),
			})
			addedHashesByAudience[audienceListKey] = append(addedHashesByAudience[audienceListKey], userIdHash)
		}
		for _, token := range u.Remove {
			userIdHash := hashIdentity(token)
			userHashKey := userKey(userIdHash)
			hdelByUserKey[userHashKey] = append(hdelByUserKey[userHashKey], u.AudienceID)
			removedHashesByAudience[audienceListKey] = append(removedHashesByAudience[audienceListKey], userIdHash)
		}
	}

	if len(hsetItems) > 0 {
		if err := s.store.HSetBatch(ctx, hsetItems); err != nil {
			return fmt.Errorf("audience: hset batch: %w", err)
		}
	}
	for audienceListKey, hashes := range addedHashesByAudience {
		if err := s.store.SAdd(ctx, audienceListKey, hashes); err != nil {
			return fmt.Errorf("audience: sadd %s: %w", audienceListKey, err)
		}
	}
	for userHashKey, fields := range hdelByUserKey {
		if err := s.store.HDel(ctx, userHashKey, fields); err != nil {
			return fmt.Errorf("audience: hdel %s: %w", userHashKey, err)
		}
	}
	for audienceListKey, hashes := range removedHashesByAudience {
		if err := s.store.SRem(ctx, audienceListKey, hashes); err != nil {
			return fmt.Errorf("audience: srem %s: %w", audienceListKey, err)
		}
	}
	return nil
}

// DeleteAudience removes the audience entirely. Enumerates every member's
// hash from the audience set, removes the audience field from each user's
// hash, and finally deletes the audience set itself.
func (s *Service) DeleteAudience(ctx context.Context, audienceID string) error {
	audienceListKey := audienceKey(audienceID)
	memberHashes, err := s.store.SMembers(ctx, audienceListKey)
	if err != nil {
		return fmt.Errorf("audience: smembers %s: %w", audienceListKey, err)
	}
	for _, h := range memberHashes {
		if err := s.store.HDel(ctx, userKey(h), []string{audienceID}); err != nil {
			return fmt.Errorf("audience: hdel %s: %w", userKey(h), err)
		}
	}
	if err := s.store.Del(ctx, audienceListKey); err != nil {
		return fmt.Errorf("audience: del %s: %w", audienceListKey, err)
	}
	return nil
}

// IsMember reports whether userToken is currently a member of audienceID.
func (s *Service) IsMember(ctx context.Context, userToken, audienceID string) (bool, error) {
	return s.store.HExists(ctx, userKey(hashIdentity(userToken)), audienceID)
}

// IsMemberBatch checks one (user, audience) pair per lookup. Result order
// matches lookups.
func (s *Service) IsMemberBatch(ctx context.Context, lookups []MembershipLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	storeLookups := make([]HLookup, len(lookups))
	for i, l := range lookups {
		storeLookups[i] = HLookup{Key: userKey(hashIdentity(l.UserToken)), Field: l.AudienceID}
	}
	return s.store.HExistsBatch(ctx, storeLookups)
}

// Memberships returns every audience userToken is a member of, mapped to
// the stored score. Score 0 means membership-only.
func (s *Service) Memberships(ctx context.Context, userToken string) (map[string]float64, error) {
	raw, err := s.store.HGetAll(ctx, userKey(hashIdentity(userToken)))
	if err != nil {
		return nil, err
	}
	return decodeScoreMap(raw), nil
}

// MembershipsBatch returns memberships for each user token in order. Missing
// users produce empty maps at their index.
func (s *Service) MembershipsBatch(ctx context.Context, userTokens []string) ([]map[string]float64, error) {
	if len(userTokens) == 0 {
		return nil, nil
	}
	keys := make([]string, len(userTokens))
	for i, t := range userTokens {
		keys[i] = userKey(hashIdentity(t))
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

// hashIdentity returns a SHA-256 hex digest of userToken truncated to 16
// bytes. Matches the truncation used elsewhere in the targeting SDK.
func hashIdentity(userToken string) string {
	h := sha256.Sum256([]byte(userToken))
	return hex.EncodeToString(h[:16])
}

func userKey(hash string) string         { return userKeyPrefix + hash }
func audienceKey(audienceID string) string { return audienceKeyPrefix + audienceID }

// encodeScore renders score as a decimal string. Score 0 becomes "0".
func encodeScore(score float64) string {
	return strconv.FormatFloat(score, 'g', -1, 64)
}

// decodeScoreMap converts the raw HGETALL result into a typed score map.
// Unparseable values default to 0 (membership-only).
func decodeScoreMap(raw map[string]string) map[string]float64 {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]float64, len(raw))
	for audienceID, v := range raw {
		score, _ := strconv.ParseFloat(v, 64)
		out[audienceID] = score
	}
	return out
}
