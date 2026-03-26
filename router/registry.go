package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// RegistryProperty represents a property in the registry.
type RegistryProperty struct {
	PropertyID   string   `json:"property_id"`
	PropertyRID  uint64   `json:"property_rid"`
	PropertyType string   `json:"property_type"`
	Domain       string   `json:"domain"`
	Placements   []string `json:"placements,omitempty"`
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

	// property_rid → RegistryProperty (integer ID lookup)
	byRID map[uint64]*RegistryProperty

	// domain → property_id (reverse domain lookup)
	byDomain map[string]string

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
		byRID:          make(map[uint64]*RegistryProperty),
		byDomain:       make(map[string]string),
		snapshotURL:    snapshotURL,
		incrementalURL: incrementalURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// LookupByID returns a property by its string ID. O(1).
func (r *Registry) LookupByID(propertyID string) (*RegistryProperty, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[propertyID]
	return p, ok
}

// LookupByRID returns a property by its integer registry ID. O(1).
func (r *Registry) LookupByRID(rid uint64) (*RegistryProperty, bool) {
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

// PropertyRID returns the integer registry ID for a property_id.
// Returns 0 if not found (unregistered property).
func (r *Registry) PropertyRID(propertyID string) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.byID[propertyID]; ok {
		return p.PropertyRID
	}
	return 0
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("snapshot returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
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
	r.mu.Lock()
	defer r.mu.Unlock()

	// Rebuild all indexes from scratch
	r.byID = make(map[string]*RegistryProperty, len(snapshot.Properties))
	r.byRID = make(map[uint64]*RegistryProperty, len(snapshot.Properties))
	r.byDomain = make(map[string]string, len(snapshot.Properties))

	for i := range snapshot.Properties {
		p := &snapshot.Properties[i]
		r.byID[p.PropertyID] = p
		r.byRID[p.PropertyRID] = p
		if p.Domain != "" {
			r.byDomain[p.Domain] = p.PropertyID
		}
	}

	r.sequence.Store(snapshot.Sequence)
}

// ApplyUpdate applies a single incremental update.
func (r *Registry) ApplyUpdate(update *RegistryUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch update.Action {
	case "add", "update":
		p := &update.Property
		r.byID[p.PropertyID] = p
		r.byRID[p.PropertyRID] = p
		if p.Domain != "" {
			r.byDomain[p.Domain] = p.PropertyID
		}

	case "remove":
		if existing, ok := r.byID[update.Property.PropertyID]; ok {
			delete(r.byID, existing.PropertyID)
			delete(r.byRID, existing.PropertyRID)
			if existing.Domain != "" {
				delete(r.byDomain, existing.Domain)
			}
		}
	}

	r.sequence.Store(update.Sequence)
}

// LoadFromData loads a registry from a pre-fetched snapshot (for testing).
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
	json.NewEncoder(w).Encode(snapshot)
}
