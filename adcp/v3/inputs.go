package adcp

import "encoding/json"

// Request types for AdCP tools are generated from JSON schemas in types_gen.go
// (e.g., GetProductsRequest, CreateMediaBuyRequest, SyncAccountsRequest).
//
// This file contains types the generator can't produce: EmptyInput (no schema)
// and nested item types that are inline objects in the schemas rather than $ref
// targets.

// EmptyInput is the input type for tools that accept no parameters (e.g. get_adcp_capabilities).
type EmptyInput struct{}

// Bool returns a pointer to v for optional boolean request fields.
func Bool(v bool) *bool {
	return &v
}

// Float64 returns a pointer to v for optional numeric fields where zero is meaningful.
func Float64(v float64) *float64 {
	return &v
}

// Ptr returns a pointer to v for optional typed fields.
func Ptr[T any](v T) *T {
	return &v
}

// CreativeAssignment assigns an existing creative to a package.
type CreativeAssignment struct {
	CreativeID       string         `json:"creative_id"`
	Weight           *float64       `json:"weight,omitempty"`
	RotationMode     string         `json:"rotation_mode,omitempty"`
	GroupID          string         `json:"group_id,omitempty"`
	SequencePosition *int           `json:"sequence_position,omitempty"`
	PlacementRefs    []PlacementRef `json:"placement_refs,omitempty"`
	PlacementIDs     []string       `json:"placement_ids,omitempty"`
	Extra            map[string]any `json:"-"`
}

// SyncCreativeAssignment assigns a synced creative to an existing package.
type SyncCreativeAssignment struct {
	CreativeID   string   `json:"creative_id"`
	PackageID    string   `json:"package_id"`
	Weight       *float64 `json:"weight,omitempty"`
	PlacementIDs []string `json:"placement_ids,omitempty"`
}

// MarshalJSON preserves schema-allowed vendor fields while keeping typed fields
// authoritative when keys collide.
func (a CreativeAssignment) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(a.Extra)+7)
	for k, v := range a.Extra {
		if isCreativeAssignmentField(k) {
			continue
		}
		out[k] = v
	}
	out["creative_id"] = a.CreativeID
	if a.Weight != nil {
		out["weight"] = *a.Weight
	}
	if a.RotationMode != "" {
		out["rotation_mode"] = a.RotationMode
	}
	if a.GroupID != "" {
		out["group_id"] = a.GroupID
	}
	if a.SequencePosition != nil {
		out["sequence_position"] = *a.SequencePosition
	}
	if len(a.PlacementRefs) > 0 {
		out["placement_refs"] = a.PlacementRefs
	}
	if len(a.PlacementIDs) > 0 {
		out["placement_ids"] = a.PlacementIDs
	}
	return json.Marshal(out)
}

// UnmarshalJSON captures schema-allowed vendor fields so agents can round-trip
// platform-specific assignment metadata.
func (a *CreativeAssignment) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	if v, ok := raw["creative_id"]; ok {
		if err := json.Unmarshal(v, &a.CreativeID); err != nil {
			return err
		}
		delete(raw, "creative_id")
	}
	if v, ok := raw["weight"]; ok {
		var weight float64
		if err := json.Unmarshal(v, &weight); err != nil {
			return err
		}
		a.Weight = &weight
		delete(raw, "weight")
	}
	if v, ok := raw["rotation_mode"]; ok {
		if err := json.Unmarshal(v, &a.RotationMode); err != nil {
			return err
		}
		delete(raw, "rotation_mode")
	}
	if v, ok := raw["group_id"]; ok {
		if err := json.Unmarshal(v, &a.GroupID); err != nil {
			return err
		}
		delete(raw, "group_id")
	}
	if v, ok := raw["sequence_position"]; ok {
		var position int
		if err := json.Unmarshal(v, &position); err != nil {
			return err
		}
		a.SequencePosition = &position
		delete(raw, "sequence_position")
	}
	if v, ok := raw["placement_refs"]; ok {
		if err := json.Unmarshal(v, &a.PlacementRefs); err != nil {
			return err
		}
		delete(raw, "placement_refs")
	}
	if v, ok := raw["placement_ids"]; ok {
		if err := json.Unmarshal(v, &a.PlacementIDs); err != nil {
			return err
		}
		delete(raw, "placement_ids")
	}
	if len(raw) == 0 {
		a.Extra = nil
		return nil
	}
	a.Extra = make(map[string]any, len(raw))
	for k, v := range raw {
		var value any
		if err := json.Unmarshal(v, &value); err != nil {
			return err
		}
		a.Extra[k] = value
	}
	return nil
}

func isCreativeAssignmentField(key string) bool {
	switch key {
	case "creative_id", "weight", "rotation_mode", "group_id", "sequence_position", "placement_refs", "placement_ids":
		return true
	default:
		return false
	}
}

// --- Nested item types (inline objects in schemas, no $ref) ---

// AccountInput is a single account in a sync_accounts request.
type AccountInput struct {
	Brand        *BrandReference `json:"brand,omitempty"`
	Operator     string          `json:"operator,omitempty"`
	Billing      string          `json:"billing,omitempty"`
	PaymentTerms string          `json:"payment_terms,omitempty"`
	AccountID    string          `json:"account_id,omitempty"`
	Sandbox      bool            `json:"sandbox,omitempty"`
}

// GovernanceAccountInput is a single account in a sync_governance request.
type GovernanceAccountInput struct {
	Account          *GovernanceAccount `json:"account,omitempty"`
	Brand            *BrandReference    `json:"brand,omitempty"`
	Operator         string             `json:"operator,omitempty"`
	GovernanceAgents []GovernanceAgent  `json:"governance_agents,omitempty"`
}

// CreativeInput is a single creative in a sync_creatives request.
type CreativeInput struct {
	CreativeID string         `json:"creative_id"`
	FormatID   *FormatRef     `json:"format_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Assets     map[string]any `json:"assets,omitempty"`
}

// CreativeFilters contains filters for list_creatives.
type CreativeFilters struct {
	CreativeIDs []string    `json:"creative_ids,omitempty"`
	FormatIDs   []FormatRef `json:"format_ids,omitempty"`
}

// CatalogInput is a single catalog in a sync_catalogs request.
type CatalogInput struct {
	CatalogID string           `json:"catalog_id"`
	Items     []map[string]any `json:"items,omitempty"`
}

// EventSourceInput is a single event source.
type EventSourceInput struct {
	EventSourceID string `json:"event_source_id"`
}

// DestinationInput is a single destination in an activate_signal request.
type DestinationInput struct {
	Type     string `json:"type"`
	Platform string `json:"platform,omitempty"`
	AgentURL string `json:"agent_url,omitempty"`
	Account  string `json:"account,omitempty"`
}

// SignalFilters contains optional filters for get_signals.
type SignalFilters struct {
	MaxCPM                float64  `json:"max_cpm,omitempty"`
	MinCoveragePercentage float64  `json:"min_coverage_percentage,omitempty"`
	CatalogTypes          []string `json:"catalog_types,omitempty"`
}
