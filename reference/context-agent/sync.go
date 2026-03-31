package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// RegistrySync defines the interface for fetching registry data.
// In production, this downloads binary snapshots from a CDN.
// The file-based implementation reads local JSON for development.
type RegistrySync interface {
	FetchSnapshot() (*PropertyRegistry, error)
	FetchEventsSince(sequence uint64) ([]RegistryEvent, error)
}

// RegistryEvent represents a single change to the property registry.
type RegistryEvent struct {
	Sequence uint64         `json:"sequence"`
	Action   string         `json:"action"` // "register", "update", "deactivate"
	Record   PropertyRecord `json:"record"`
}

// FileRegistrySync implements RegistrySync using local JSON files.
type FileRegistrySync struct {
	snapshotPath string
	eventsPath   string
}

func NewFileRegistrySync(snapshotPath, eventsPath string) *FileRegistrySync {
	return &FileRegistrySync{
		snapshotPath: snapshotPath,
		eventsPath:   eventsPath,
	}
}

func (f *FileRegistrySync) FetchSnapshot() (*PropertyRegistry, error) {
	registry := NewPropertyRegistry()
	if err := registry.LoadFromFile(f.snapshotPath); err != nil {
		return nil, fmt.Errorf("fetch snapshot: %w", err)
	}
	return registry, nil
}

func (f *FileRegistrySync) FetchEventsSince(sequence uint64) ([]RegistryEvent, error) {
	data, err := os.ReadFile(f.eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No events file means no events
		}
		return nil, fmt.Errorf("read events: %w", err)
	}

	var allEvents []RegistryEvent
	if err := json.Unmarshal(data, &allEvents); err != nil {
		return nil, fmt.Errorf("unmarshal events: %w", err)
	}

	var filtered []RegistryEvent
	for _, e := range allEvents {
		if e.Sequence > sequence {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// ApplyEvents applies a batch of registry events to the registry and targeting config.
func ApplyEvents(registry *PropertyRegistry, targeting *TargetingConfig, events []RegistryEvent) {
	for _, event := range events {
		switch event.Action {
		case "register", "update":
			rec := event.Record
			registry.Put(&rec)
			if !targeting.ContainsProperty(rec.RID) {
				targeting.AddProperties(rec.RID)
			}
		case "deactivate":
			registry.Remove(event.Record.RID)
			targeting.PropertyBitmap.Remove(event.Record.RID)
		}
		if event.Sequence > registry.Sequence {
			registry.Sequence = event.Sequence
		}
	}
}
