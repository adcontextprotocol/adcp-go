// Package glidestore implements registry.Store on top of Valkey
// using valkey-glide/go/v2. The persisted state lets a process restart
// rehydrate its in-memory registry indexes without re-bootstrapping the
// change feed from scratch.
//
// Only a single standalone Valkey endpoint is supported — the registry
// dataset is small relative to targeting/audience workloads and clusters
// add complexity without a clear benefit.
package glidestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/models"
	"github.com/valkey-io/valkey-glide/go/v2/options"

	"github.com/adcontextprotocol/adcp-go/registry"
)

var _ registry.Store = (*Store)(nil)

// DefaultKeyPrefix is the namespace prefix prepended to every key Store
// touches. Override with Options.KeyPrefix when sharing a Valkey
// instance between environments.
const DefaultKeyPrefix = "adcp:registry"

// Options configures a Store.
type Options struct {
	// KeyPrefix namespaces every key. Defaults to DefaultKeyPrefix when
	// empty.
	KeyPrefix string
	// ScanCount sets the COUNT hint passed to SCAN/HSCAN. Defaults to
	// 1000 when zero. Higher values reduce round-trips during hydration
	// at the cost of a larger single response.
	ScanCount int64
}

// Store persists registry state in Valkey.
type Store struct {
	client    *glide.Client
	keyPrefix string
	scanCount int64
}

// New constructs a Store backed by the given GLIDE client.
func New(client *glide.Client, opts Options) *Store {
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

// --- key derivation ---

func (s *Store) cursorKey() string     { return s.keyPrefix + ":cursor" }
func (s *Store) propertiesKey() string { return s.keyPrefix + ":properties" }
func (s *Store) agentsKey() string     { return s.keyPrefix + ":agents" }
func (s *Store) authKey(agentURL string) string {
	return s.keyPrefix + ":auth:" + agentURL
}
func (s *Store) authMatchPattern() string { return s.keyPrefix + ":auth:*" }

// authField is the field name within an auth hash. The publisher domain
// and authorization type round-trip through HSET/HSCAN field names, so
// the encoding must be unambiguous and reversible.
func authField(publisherDomain, authorizationType string) string {
	return publisherDomain + "|" + authorizationType
}

// parseAuthField recovers the (publisherDomain, authorizationType) pair
// from a hash field name. Returns ok=false on malformed input rather
// than panicking so a stray key in the namespace can't crash hydration.
func parseAuthField(field string) (publisherDomain, authorizationType string, ok bool) {
	i := strings.IndexByte(field, '|')
	if i < 0 {
		return "", "", false
	}
	return field[:i], field[i+1:], true
}

// --- cursor ---

// Load returns the persisted cursor, or "" if none.
func (s *Store) Load(ctx context.Context) (string, error) {
	res, err := s.client.Get(ctx, s.cursorKey())
	if err != nil {
		return "", err
	}
	if res.IsNil() {
		return "", nil
	}
	return res.Value(), nil
}

// Save replaces the persisted cursor.
func (s *Store) Save(ctx context.Context, cursor string) error {
	if cursor == "" {
		_, err := s.client.Del(ctx, []string{s.cursorKey()})
		return err
	}
	_, err := s.client.Set(ctx, s.cursorKey(), cursor)
	return err
}

// --- properties ---

func (s *Store) PutProperty(ctx context.Context, p *registry.Property) error {
	if p == nil {
		return errors.New("glidestore: PutProperty with nil property")
	}
	if p.PropertyID == "" {
		return errors.New("glidestore: PutProperty with empty property_id")
	}
	blob, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("glidestore: marshal property: %w", err)
	}
	_, err = s.client.HSet(ctx, s.propertiesKey(), map[string]string{p.PropertyID: string(blob)})
	return err
}

func (s *Store) RemoveProperty(ctx context.Context, propertyID string) error {
	if propertyID == "" {
		return nil
	}
	_, err := s.client.HDel(ctx, s.propertiesKey(), []string{propertyID})
	return err
}

func (s *Store) ClearProperties(ctx context.Context) error {
	_, err := s.client.Del(ctx, []string{s.propertiesKey()})
	return err
}

func (s *Store) LoadProperties(ctx context.Context) ([]registry.Property, error) {
	var out []registry.Property
	if err := s.hscanHash(ctx, s.propertiesKey(), func(_, value string) error {
		var p registry.Property
		if err := json.Unmarshal([]byte(value), &p); err != nil {
			return fmt.Errorf("glidestore: unmarshal property: %w", err)
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
		return errors.New("glidestore: PutAgent with nil profile")
	}
	if p.AgentURL == "" {
		return errors.New("glidestore: PutAgent with empty agent_url")
	}
	blob, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("glidestore: marshal agent: %w", err)
	}
	_, err = s.client.HSet(ctx, s.agentsKey(), map[string]string{p.AgentURL: string(blob)})
	return err
}

func (s *Store) RemoveAgent(ctx context.Context, agentURL string) error {
	if agentURL == "" {
		return nil
	}
	_, err := s.client.HDel(ctx, s.agentsKey(), []string{agentURL})
	return err
}

func (s *Store) ClearAgents(ctx context.Context) error {
	_, err := s.client.Del(ctx, []string{s.agentsKey()})
	return err
}

func (s *Store) LoadAgents(ctx context.Context) ([]registry.AgentProfile, error) {
	var out []registry.AgentProfile
	if err := s.hscanHash(ctx, s.agentsKey(), func(_, value string) error {
		var p registry.AgentProfile
		if err := json.Unmarshal([]byte(value), &p); err != nil {
			return fmt.Errorf("glidestore: unmarshal agent: %w", err)
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
		return errors.New("glidestore: PutAuth requires agent_url, publisher_domain, authorization_type")
	}
	blob, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("glidestore: marshal auth: %w", err)
	}
	_, err = s.client.HSet(ctx, s.authKey(e.AgentURL),
		map[string]string{authField(e.PublisherDomain, e.AuthorizationType): string(blob)})
	return err
}

func (s *Store) RemoveAuthEntry(ctx context.Context, agentURL, publisherDomain string) error {
	if agentURL == "" || publisherDomain == "" {
		return nil
	}
	key := s.authKey(agentURL)
	// Find every authorization_type field for this domain. The dataset
	// per agent is small (≤4 auth types per domain in practice), so HKEYS
	// is fine.
	fields, err := s.client.HKeys(ctx, key)
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
	_, err = s.client.HDel(ctx, key, toDelete)
	return err
}

func (s *Store) RemoveAuthAgent(ctx context.Context, agentURL string) error {
	if agentURL == "" {
		return nil
	}
	_, err := s.client.Del(ctx, []string{s.authKey(agentURL)})
	return err
}

func (s *Store) ClearAuth(ctx context.Context) error {
	return s.scanKeys(ctx, s.authMatchPattern(), func(batch []string) error {
		if len(batch) == 0 {
			return nil
		}
		_, err := s.client.Del(ctx, batch)
		return err
	})
}

func (s *Store) LoadAuth(ctx context.Context) ([]registry.AuthorizationEntry, error) {
	var out []registry.AuthorizationEntry
	err := s.scanKeys(ctx, s.authMatchPattern(), func(batch []string) error {
		for _, key := range batch {
			if err := s.hscanHash(ctx, key, func(_, value string) error {
				var e registry.AuthorizationEntry
				if err := json.Unmarshal([]byte(value), &e); err != nil {
					return fmt.Errorf("glidestore: unmarshal auth: %w", err)
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

// scanKeys pages through every key matching pattern and hands each batch
// of key names to onBatch.
func (s *Store) scanKeys(ctx context.Context, pattern string, onBatch func(batch []string) error) error {
	opts := *options.NewScanOptions().SetMatch(pattern).SetCount(s.scanCount)
	cursor := models.NewCursor()
	for {
		res, err := s.client.ScanWithOptions(ctx, cursor, opts)
		if err != nil {
			return err
		}
		if err := onBatch(res.Data); err != nil {
			return err
		}
		if res.Cursor.IsFinished() {
			return nil
		}
		cursor = res.Cursor
	}
}

// hscanHash pages through every (field, value) under key and invokes
// onPair for each. If the key does not exist HSCAN reports an empty
// result and onPair is never called.
func (s *Store) hscanHash(ctx context.Context, key string, onPair func(field, value string) error) error {
	opts := *options.NewHashScanOptions().SetCount(s.scanCount)
	cursor := models.NewCursor()
	for {
		res, err := s.client.HScanWithOptions(ctx, key, cursor, opts)
		if err != nil {
			return err
		}
		// HSCAN returns field/value pairs interleaved in Data.
		if len(res.Data)%2 != 0 {
			return fmt.Errorf("glidestore: HSCAN %s returned odd-length data (%d)", key, len(res.Data))
		}
		for i := 0; i < len(res.Data); i += 2 {
			if err := onPair(res.Data[i], res.Data[i+1]); err != nil {
				return err
			}
		}
		if res.Cursor.IsFinished() {
			return nil
		}
		cursor = res.Cursor
	}
}
