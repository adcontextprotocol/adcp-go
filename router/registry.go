package router

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// RegistryProperty represents a property in the registry.
type RegistryProperty struct {
	PropertyID   string               `json:"property_id"`
	PropertyRID  string               `json:"property_rid"`
	PropertyType string               `json:"property_type"`
	Domain       string               `json:"domain"`
	Placements   []string             `json:"placements,omitempty"`
	SigningKeys  []tmproto.SigningKey `json:"signing_keys,omitempty"`
}

// RegistrySnapshot is a full point-in-time view of the registry.
type RegistrySnapshot struct {
	Sequence   uint64             `json:"sequence"`
	Timestamp  time.Time          `json:"timestamp"`
	Properties []RegistryProperty `json:"properties"`
}

// RegistryUpdate is an incremental change to the registry.
type RegistryUpdate struct {
	Sequence uint64           `json:"sequence"`
	Action   string           `json:"action"` // "add", "update", "remove"
	Property RegistryProperty `json:"property"`
}

// Registry provides fast property_id → property_rid lookups and metadata resolution.
// Thread-safe for concurrent reads with periodic writes from sync.
type Registry struct {
	mu sync.RWMutex

	// property_id → RegistryProperty (full metadata)
	byID map[string]*RegistryProperty

	// property_rid → RegistryProperty (UUID lookup)
	byRID map[string]*RegistryProperty

	// domain → property_id (reverse domain lookup)
	byDomain map[string]string

	// kid → SigningKey (cross-property signing key index)
	byKid map[string]*tmproto.SigningKey

	// Current sequence number
	sequence atomic.Uint64

	// Sync configuration
	snapshotURL    string
	incrementalURL string
	client         *http.Client
}

// NewRegistry creates a registry with the given sync endpoints.
func NewRegistry(snapshotURL, incrementalURL string) *Registry {
	return &Registry{
		byID:           make(map[string]*RegistryProperty),
		byRID:          make(map[string]*RegistryProperty),
		byDomain:       make(map[string]string),
		byKid:          make(map[string]*tmproto.SigningKey),
		snapshotURL:    snapshotURL,
		incrementalURL: incrementalURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// LookupKey resolves a kid to its SigningKey by scanning every property's
// signing-key list. Implements tmproto.KeyStore so a Registry can drive a
// verifier directly.
func (r *Registry) LookupKey(kid string) (*tmproto.SigningKey, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.byKid[kid]
	return k, ok
}

// AttachSigningKey adds a signing key to the property record for propertyRID.
// Idempotent on (kid, propertyRID): replaces any existing key with the same
// kid on that property. The key also becomes resolvable via LookupKey.
// Returns false if propertyRID is unknown.
func (r *Registry) AttachSigningKey(propertyRID string, key tmproto.SigningKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	prop, ok := r.byRID[propertyRID]
	if !ok {
		return false
	}
	for i := range prop.SigningKeys {
		if prop.SigningKeys[i].Kid == key.Kid {
			prop.SigningKeys[i] = key
			r.byKid[key.Kid] = &prop.SigningKeys[i]
			return true
		}
	}
	prop.SigningKeys = append(prop.SigningKeys, key)
	r.byKid[key.Kid] = &prop.SigningKeys[len(prop.SigningKeys)-1]
	return true
}

// LookupByID returns a property by its string ID. O(1).
func (r *Registry) LookupByID(propertyID string) (*RegistryProperty, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[propertyID]
	return p, ok
}

// LookupByRID returns a property by its registry ID. O(1).
func (r *Registry) LookupByRID(rid string) (*RegistryProperty, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byRID[rid]
	return p, ok
}

// LookupByDomain returns a property_id for a domain. O(1).
func (r *Registry) LookupByDomain(domain string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byDomain[domain]
	return id, ok
}

// PropertyRID returns the registry ID for a property_id.
// Returns "" if not found (unregistered property).
func (r *Registry) PropertyRID(propertyID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.byID[propertyID]; ok {
		return p.PropertyRID
	}
	return ""
}

// Count returns the number of properties in the registry.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// Sequence returns the current registry sequence number.
func (r *Registry) Sequence() uint64 {
	return r.sequence.Load()
}

// --- Sync ---

// LoadSnapshot fetches and applies a full registry snapshot.
func (r *Registry) LoadSnapshot() error {
	if r.snapshotURL == "" {
		return nil
	}

	resp, err := r.client.Get(r.snapshotURL)
	if err != nil {
		return fmt.Errorf("fetch snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("snapshot returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	var snapshot RegistrySnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}

	r.applySnapshot(&snapshot)
	return nil
}

func (r *Registry) applySnapshot(snapshot *RegistrySnapshot) {
	// Build new indexes without holding any lock so readers are not blocked
	// during the O(n) construction phase.
	byID := make(map[string]*RegistryProperty, len(snapshot.Properties))
	byRID := make(map[string]*RegistryProperty, len(snapshot.Properties))
	byDomain := make(map[string]string, len(snapshot.Properties))
	byKid := make(map[string]*tmproto.SigningKey)
	kidOwner := make(map[string]string)

	for i := range snapshot.Properties {
		p := &snapshot.Properties[i]
		byID[p.PropertyID] = p
		byRID[p.PropertyRID] = p
		if p.Domain != "" {
			byDomain[p.Domain] = p.PropertyID
		}
		for j := range p.SigningKeys {
			k := &p.SigningKeys[j]
			if k.Kid == "" {
				continue
			}
			if existing, conflict := kidOwner[k.Kid]; conflict && existing != p.PropertyRID {
				slog.Warn("registry signing-key kid collision — keeping first-seen entry",
					"kid", k.Kid,
					"first_property_rid", existing,
					"duplicate_property_rid", p.PropertyRID)
				continue
			}
			byKid[k.Kid] = k
			kidOwner[k.Kid] = p.PropertyRID
		}
	}

	// Swap the map pointers under the lock (O(1)), then publish the new sequence
	// number after the unlock. Storing sequence after Unlock ensures that any
	// goroutine observing the advanced sequence via the lock-free Sequence() call
	// will also see the new maps once it acquires RLock.
	r.mu.Lock()
	r.byID = byID
	r.byRID = byRID
	r.byDomain = byDomain
	r.byKid = byKid
	r.mu.Unlock()
	r.sequence.Store(snapshot.Sequence)
}

// ApplyUpdate applies a single incremental update.
func (r *Registry) ApplyUpdate(update *RegistryUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch update.Action {
	case "add", "update":
		if existing, ok := r.byID[update.Property.PropertyID]; ok {
			for j := range existing.SigningKeys {
				delete(r.byKid, existing.SigningKeys[j].Kid)
			}
		}
		p := &update.Property
		r.byID[p.PropertyID] = p
		r.byRID[p.PropertyRID] = p
		if p.Domain != "" {
			r.byDomain[p.Domain] = p.PropertyID
		}
		for j := range p.SigningKeys {
			k := &p.SigningKeys[j]
			if k.Kid == "" {
				continue
			}
			// kids belonging to this property were deleted above, so a
			// remaining entry under the same kid is owned by a different
			// property — keep the first-seen and refuse to shadow it.
			if _, conflict := r.byKid[k.Kid]; conflict {
				slog.Warn("registry signing-key kid collision on incremental update — keeping first-seen entry",
					"kid", k.Kid,
					"duplicate_property_rid", p.PropertyRID)
				continue
			}
			r.byKid[k.Kid] = k
		}

	case "remove":
		if existing, ok := r.byID[update.Property.PropertyID]; ok {
			delete(r.byID, existing.PropertyID)
			if existing.PropertyRID != "" {
				delete(r.byRID, existing.PropertyRID)
			}
			if existing.Domain != "" {
				delete(r.byDomain, existing.Domain)
			}
			for j := range existing.SigningKeys {
				delete(r.byKid, existing.SigningKeys[j].Kid)
			}
		}
	}

	r.sequence.Store(update.Sequence)
}

// LoadFromData replaces the entire registry with the given properties and
// sequence number. Callers pass a snapshot they have assembled elsewhere
// (a remote feed sync loop, a hand-built test fixture, etc.); the internal
// maps are swapped atomically. Any state a prior loader attached via
// ApplyUpdate / AttachSigningKey is dropped by the swap — callers that
// layer supplementary state (e.g. the router's own signing keys) must
// re-attach it after each LoadFromData call.
func (r *Registry) LoadFromData(properties []RegistryProperty, sequence uint64) {
	snapshot := &RegistrySnapshot{
		Sequence:   sequence,
		Timestamp:  time.Now(),
		Properties: properties,
	}
	r.applySnapshot(snapshot)
}

// --- Serve Registry to Agents ---

// HandleSnapshot serves the current registry as a JSON snapshot.
// Agents call this to bootstrap or refresh their local copy.
func (r *Registry) HandleSnapshot(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	properties := make([]RegistryProperty, 0, len(r.byID))
	for _, p := range r.byID {
		properties = append(properties, *p)
	}
	seq := r.sequence.Load()
	r.mu.RUnlock()

	snapshot := RegistrySnapshot{
		Sequence:   seq,
		Timestamp:  time.Now(),
		Properties: properties,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Registry-Sequence", fmt.Sprintf("%d", seq))
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		slog.Debug("failed to write registry snapshot", "error", err)
	}
}
