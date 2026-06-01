// Package redisstore implements registry.Store on top of Valkey or
// Redis using github.com/redis/go-redis/v9. It is the go-redis sibling
// of registry/glidestore and uses the same key layout so the two
// backends interoperate when pointed at the same instance.
//
// Only a single standalone endpoint is supported — the registry dataset
// is small and clusters add complexity without a clear benefit here.
package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/registry"
)

var _ registry.Store = (*Store)(nil)

// DefaultKeyPrefix is the namespace prefix prepended to every key.
const DefaultKeyPrefix = "adcp:registry"

// Options configures a Store.
type Options struct {
	KeyPrefix string
	// ScanCount sets the COUNT hint passed to SCAN/HSCAN. Defaults to
	// 1000 when zero.
	ScanCount int64
}

// Store persists registry state in Valkey/Redis via go-redis.
type Store struct {
	client    redis.UniversalClient
	keyPrefix string
	scanCount int64
}

// New constructs a Store backed by the given go-redis client. Pass
// *redis.Client for standalone; *redis.ClusterClient also satisfies
// UniversalClient but the registry dataset doesn't require cluster
// topology.
func New(client redis.UniversalClient, opts Options) *Store {
	prefix := opts.KeyPrefix
	if prefix == "" {
		prefix = DefaultKeyPrefix
	}
	count := opts.ScanCount
	if count == 0 {
		count = 1000
	}
	return &Store{client: client, keyPrefix: prefix, scanCount: count}
}

func (s *Store) cursorKey() string     { return s.keyPrefix + ":cursor" }
func (s *Store) propertiesKey() string { return s.keyPrefix + ":properties" }
func (s *Store) agentsKey() string     { return s.keyPrefix + ":agents" }
func (s *Store) authKey(agentURL string) string {
	return s.keyPrefix + ":auth:" + agentURL
}
func (s *Store) authMatchPattern() string { return s.keyPrefix + ":auth:*" }

func authField(publisherDomain, authorizationType string) string {
	return publisherDomain + "|" + authorizationType
}

// --- cursor ---

func (s *Store) Load(ctx context.Context) (string, error) {
	val, err := s.client.Get(ctx, s.cursorKey()).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}

func (s *Store) Save(ctx context.Context, cursor string) error {
	if cursor == "" {
		return s.client.Del(ctx, s.cursorKey()).Err()
	}
	return s.client.Set(ctx, s.cursorKey(), cursor, 0).Err()
}

// --- properties ---

func (s *Store) PutProperty(ctx context.Context, p *registry.Property) error {
	if p == nil {
		return errors.New("redisstore: PutProperty with nil property")
	}
	if p.PropertyID == "" {
		return errors.New("redisstore: PutProperty with empty property_id")
	}
	blob, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("redisstore: marshal property: %w", err)
	}
	return s.client.HSet(ctx, s.propertiesKey(), p.PropertyID, string(blob)).Err()
}

func (s *Store) RemoveProperty(ctx context.Context, propertyID string) error {
	if propertyID == "" {
		return nil
	}
	return s.client.HDel(ctx, s.propertiesKey(), propertyID).Err()
}

func (s *Store) ClearProperties(ctx context.Context) error {
	return s.client.Del(ctx, s.propertiesKey()).Err()
}

func (s *Store) LoadProperties(ctx context.Context) ([]registry.Property, error) {
	var out []registry.Property
	if err := s.hscanHash(ctx, s.propertiesKey(), func(_, value string) error {
		var p registry.Property
		if err := json.Unmarshal([]byte(value), &p); err != nil {
			return fmt.Errorf("redisstore: unmarshal property: %w", err)
		}
		out = append(out, p)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// --- agents ---

func (s *Store) PutAgent(ctx context.Context, p *registry.AgentProfile) error {
	if p == nil {
		return errors.New("redisstore: PutAgent with nil profile")
	}
	if p.AgentURL == "" {
		return errors.New("redisstore: PutAgent with empty agent_url")
	}
	blob, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("redisstore: marshal agent: %w", err)
	}
	return s.client.HSet(ctx, s.agentsKey(), p.AgentURL, string(blob)).Err()
}

func (s *Store) RemoveAgent(ctx context.Context, agentURL string) error {
	if agentURL == "" {
		return nil
	}
	return s.client.HDel(ctx, s.agentsKey(), agentURL).Err()
}

func (s *Store) ClearAgents(ctx context.Context) error {
	return s.client.Del(ctx, s.agentsKey()).Err()
}

func (s *Store) LoadAgents(ctx context.Context) ([]registry.AgentProfile, error) {
	var out []registry.AgentProfile
	if err := s.hscanHash(ctx, s.agentsKey(), func(_, value string) error {
		var p registry.AgentProfile
		if err := json.Unmarshal([]byte(value), &p); err != nil {
			return fmt.Errorf("redisstore: unmarshal agent: %w", err)
		}
		out = append(out, p)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// --- authorizations ---

func (s *Store) PutAuth(ctx context.Context, e registry.AuthorizationEntry) error {
	if e.AgentURL == "" || e.PublisherDomain == "" || e.AuthorizationType == "" {
		return errors.New("redisstore: PutAuth requires agent_url, publisher_domain, authorization_type")
	}
	blob, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("redisstore: marshal auth: %w", err)
	}
	return s.client.HSet(ctx, s.authKey(e.AgentURL),
		authField(e.PublisherDomain, e.AuthorizationType), string(blob)).Err()
}

func (s *Store) RemoveAuthEntry(ctx context.Context, agentURL, publisherDomain string) error {
	if agentURL == "" || publisherDomain == "" {
		return nil
	}
	key := s.authKey(agentURL)
	fields, err := s.client.HKeys(ctx, key).Result()
	if err != nil {
		return err
	}
	prefix := publisherDomain + "|"
	var toDelete []string
	for _, f := range fields {
		if strings.HasPrefix(f, prefix) {
			toDelete = append(toDelete, f)
		}
	}
	if len(toDelete) == 0 {
		return nil
	}
	return s.client.HDel(ctx, key, toDelete...).Err()
}

func (s *Store) RemoveAuthAgent(ctx context.Context, agentURL string) error {
	if agentURL == "" {
		return nil
	}
	return s.client.Del(ctx, s.authKey(agentURL)).Err()
}

func (s *Store) ClearAuth(ctx context.Context) error {
	return s.scanKeys(ctx, s.authMatchPattern(), func(batch []string) error {
		if len(batch) == 0 {
			return nil
		}
		return s.client.Del(ctx, batch...).Err()
	})
}

func (s *Store) LoadAuth(ctx context.Context) ([]registry.AuthorizationEntry, error) {
	var out []registry.AuthorizationEntry
	err := s.scanKeys(ctx, s.authMatchPattern(), func(batch []string) error {
		for _, key := range batch {
			if err := s.hscanHash(ctx, key, func(_, value string) error {
				var e registry.AuthorizationEntry
				if err := json.Unmarshal([]byte(value), &e); err != nil {
					return fmt.Errorf("redisstore: unmarshal auth: %w", err)
				}
				out = append(out, e)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- scan helpers ---

func (s *Store) scanKeys(ctx context.Context, pattern string, onBatch func(batch []string) error) error {
	var cursor uint64
	for {
		batch, next, err := s.client.Scan(ctx, cursor, pattern, s.scanCount).Result()
		if err != nil {
			return err
		}
		if err := onBatch(batch); err != nil {
			return err
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

func (s *Store) hscanHash(ctx context.Context, key string, onPair func(field, value string) error) error {
	var cursor uint64
	for {
		batch, next, err := s.client.HScan(ctx, key, cursor, "", s.scanCount).Result()
		if err != nil {
			return err
		}
		if len(batch)%2 != 0 {
			return fmt.Errorf("redisstore: HSCAN %s returned odd-length data (%d)", key, len(batch))
		}
		for i := 0; i < len(batch); i += 2 {
			if err := onPair(batch[i], batch[i+1]); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}
