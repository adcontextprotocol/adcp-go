// Package main provides the property registry types and lookup for the context agent.
//
// The registry maps property RIDs (uint32) to property records containing
// domain, public key, and authorized agents. In production, this is downloaded
// as a binary snapshot. For now, it uses JSON serialization.
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// PropertyRecord is a single property in the registry.
type PropertyRecord struct {
	RID              uint32           `json:"rid"`
	Domain           string           `json:"domain"`
	PublicKey        ed25519.PublicKey `json:"public_key"`
	AuthorizedAgents []AuthorizedAgent `json:"authorized_agents,omitempty"`
}

// AuthorizedAgent is an agent authorized to act on behalf of a property.
type AuthorizedAgent struct {
	URL       string           `json:"url"`
	Role      string           `json:"role"` // "seller", "data_provider", "buyer"
	PublicKey ed25519.PublicKey `json:"public_key"`
}

// PropertyRegistry holds the full set of property records keyed by RID.
type PropertyRegistry struct {
	mu       sync.RWMutex
	Sequence uint64                    `json:"sequence"`
	Records  map[uint32]*PropertyRecord `json:"records"`
}

// NewPropertyRegistry creates an empty registry.
func NewPropertyRegistry() *PropertyRegistry {
	return &PropertyRegistry{
		Records: make(map[uint32]*PropertyRecord),
	}
}

// Get returns the property record for a given RID, or nil if not found.
func (r *PropertyRegistry) Get(rid uint32) *PropertyRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Records[rid]
}

// Put inserts or updates a property record.
func (r *PropertyRegistry) Put(rec *PropertyRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Records[rec.RID] = rec
}

// Remove deletes a property record by RID.
func (r *PropertyRegistry) Remove(rid uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Records, rid)
}

// Len returns the number of records in the registry.
func (r *PropertyRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.Records)
}

// AllRIDs returns all RIDs in the registry.
func (r *PropertyRegistry) AllRIDs() []uint32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rids := make([]uint32, 0, len(r.Records))
	for rid := range r.Records {
		rids = append(rids, rid)
	}
	return rids
}

// LoadFromFile loads a registry snapshot from a JSON file.
func (r *PropertyRegistry) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read registry file: %w", err)
	}
	return r.LoadFromJSON(data)
}

// LoadFromJSON loads a registry snapshot from JSON bytes.
func (r *PropertyRegistry) LoadFromJSON(data []byte) error {
	var snapshot struct {
		Sequence uint64            `json:"sequence"`
		Records  []*PropertyRecord `json:"records"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("unmarshal registry: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.Sequence = snapshot.Sequence
	r.Records = make(map[uint32]*PropertyRecord, len(snapshot.Records))
	for _, rec := range snapshot.Records {
		r.Records[rec.RID] = rec
	}
	return nil
}
