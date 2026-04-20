package adcp

// Plan is a campaign governance plan — the authorized parameters for a campaign.
// Inline nested object in sync_plans_request.plans; must be hand-written because
// the generator does not descend into inline arrays.
type Plan struct {
	PlanID                     string                `json:"plan_id"`
	Brand                      *BrandReference       `json:"brand"`
	Objectives                 string                `json:"objectives"`
	Budget                     PlanBudget            `json:"budget"`
	Channels                   *PlanChannels         `json:"channels,omitempty"`
	Flight                     PlanFlight            `json:"flight"`
	Countries                  []string              `json:"countries,omitempty"`
	Regions                    []string              `json:"regions,omitempty"`
	PolicyIDs                  []string              `json:"policy_ids,omitempty"`
	PolicyCategories           []string              `json:"policy_categories,omitempty"`
	Audience                   *AudienceConstraints  `json:"audience,omitempty"`
	RestrictedAttributes       []RestrictedAttribute `json:"restricted_attributes,omitempty"`
	RestrictedAttributesCustom []string              `json:"restricted_attributes_custom,omitempty"`
	MinAudienceSize            int                   `json:"min_audience_size,omitempty"`
	HumanReviewRequired        bool                  `json:"human_review_required,omitempty"`
	HumanOverride              *HumanOverride        `json:"human_override,omitempty"`
	CustomPolicies             []PolicyEntry         `json:"custom_policies,omitempty"`
	ApprovedSellers            []string              `json:"approved_sellers,omitempty"`
	Delegations                []PlanDelegation      `json:"delegations,omitempty"`
	Portfolio                  *PlanPortfolio        `json:"portfolio,omitempty"`
	Ext                        map[string]any        `json:"ext,omitempty"`
}

// PlanBudget authorizes spend for a plan. Exactly one of ReallocationThreshold
// or ReallocationUnlimited must be set (the schema's oneOf constraint).
// Use Validate to enforce this invariant at runtime; struct types alone cannot.
type PlanBudget struct {
	Total                 float64                         `json:"total"`
	Currency              string                          `json:"currency"`
	PerSellerMaxPct       float64                         `json:"per_seller_max_pct,omitempty"`
	ReallocationThreshold *float64                        `json:"reallocation_threshold,omitempty"`
	ReallocationUnlimited bool                            `json:"reallocation_unlimited,omitempty"`
	Allocations           map[string]PlanBudgetAllocation `json:"allocations,omitempty"`
}

// PlanBudgetAllocation caps spend for a single purchase type within a plan.
type PlanBudgetAllocation struct {
	Amount float64 `json:"amount,omitempty"`
	MaxPct float64 `json:"max_pct,omitempty"`
}

// PlanFlight defines the authorized flight window (ISO 8601 timestamps).
type PlanFlight struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// PlanChannels constrains channel selection for a plan.
type PlanChannels struct {
	Required   []string                        `json:"required,omitempty"`
	Allowed    []string                        `json:"allowed,omitempty"`
	MixTargets map[string]PlanChannelMixTarget `json:"mix_targets,omitempty"`
}

// PlanChannelMixTarget is a per-channel target allocation range.
type PlanChannelMixTarget struct {
	MinPct float64 `json:"min_pct,omitempty"`
	MaxPct float64 `json:"max_pct,omitempty"`
}

// PlanDelegation grants an agent authority to execute against a plan.
type PlanDelegation struct {
	AgentURL    string                `json:"agent_url"`
	Authority   string                `json:"authority"`
	BudgetLimit *PlanDelegationBudget `json:"budget_limit,omitempty"`
	Markets     []string              `json:"markets,omitempty"`
	ExpiresAt   string                `json:"expires_at,omitempty"`
}

// PlanDelegationBudget caps the budget a delegated agent can commit.
type PlanDelegationBudget struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// PlanPortfolio marks a plan as a portfolio plan governing member plans.
type PlanPortfolio struct {
	MemberPlanIDs    []string                `json:"member_plan_ids"`
	TotalBudgetCap   *PlanPortfolioBudgetCap `json:"total_budget_cap,omitempty"`
	SharedPolicyIDs  []string                `json:"shared_policy_ids,omitempty"`
	SharedExclusions []PolicyEntry           `json:"shared_exclusions,omitempty"`
}

// PlanPortfolioBudgetCap caps aggregate spend across member plans.
type PlanPortfolioBudgetCap struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// HumanOverride records the human approval that downgrades a plan's
// human_review_required from true to false on re-sync. Required as an artifact
// under GDPR Art 22 / EU AI Act Annex III when an override is applied.
type HumanOverride struct {
	Reason     string `json:"reason"`
	Approver   string `json:"approver"`
	ApprovedAt string `json:"approved_at,omitempty"`
}

// DataSubjectContestation is a contestation contact point for data subjects
// (GDPR Art 22(3)). Either URL or Email is required.
type DataSubjectContestation struct {
	URL       string   `json:"url,omitempty"`
	Email     string   `json:"email,omitempty"`
	Languages []string `json:"languages,omitempty"`
}
