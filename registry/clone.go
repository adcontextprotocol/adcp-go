package registry

import "slices"

func cloneProperty(p *Property) *Property {
	cp := *p
	cp.Placements = slices.Clone(p.Placements)
	return &cp
}

func cloneAgentProfile(p *AgentProfile) *AgentProfile {
	cp := *p
	cp.Channels = slices.Clone(p.Channels)
	cp.PropertyTypes = slices.Clone(p.PropertyTypes)
	cp.Markets = slices.Clone(p.Markets)
	cp.Categories = slices.Clone(p.Categories)
	cp.Tags = slices.Clone(p.Tags)
	cp.DeliveryTypes = slices.Clone(p.DeliveryTypes)
	cp.MatchedFilters = slices.Clone(p.MatchedFilters)
	cp.FormatIDs = slices.Clone(p.FormatIDs)
	return &cp
}

func cloneAuthEntry(e AuthorizationEntry) AuthorizationEntry {
	e.PropertyIDs = slices.Clone(e.PropertyIDs)
	e.PlacementIDs = slices.Clone(e.PlacementIDs)
	e.Countries = slices.Clone(e.Countries)
	return e
}
