package adcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests cover the cross-field invariants encoded in the AdCP sync_plans schema:
// the budget oneOf (threshold XOR unlimited) and the two if/then rules that tie
// regulated policy_categories and Annex III policy_ids to human_review_required.
// Plus defense-in-depth checks on HumanOverride, DataSubjectContestation, and
// length caps. Tests are written against the spec, not the code: they fail if
// the invariants are ever silently relaxed.

func validPlan() Plan {
	threshold := 50000.0
	return Plan{
		PlanID:     "plan-1",
		Brand:      &BrandReference{Domain: "nova.example"},
		Objectives: "Q4 awareness push",
		Budget: PlanBudget{
			Total:                 1000000,
			Currency:              "USD",
			ReallocationThreshold: &threshold,
		},
		Flight: PlanFlight{
			Start: "2026-04-01T00:00:00Z",
			End:   "2026-06-30T23:59:59Z",
		},
	}
}

// hasCode reports whether any error in errs carries the given code at the
// given field. Prefer this over require.Len so adding a new invariant does
// not silently mask an existing one.
func hasCode(errs []PlanValidationError, field, code string) bool {
	for _, e := range errs {
		if e.Field == field && e.Code == code {
			return true
		}
	}
	return false
}

func TestPlanValidate_Valid_Threshold(t *testing.T) {
	p := validPlan()
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_Valid_Unlimited(t *testing.T) {
	p := validPlan()
	p.Budget.ReallocationThreshold = nil
	p.Budget.ReallocationUnlimited = true
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_Budget_BothSet(t *testing.T) {
	p := validPlan()
	p.Budget.ReallocationUnlimited = true // threshold still set from validPlan
	errs := p.Validate()
	assert.True(t, hasCode(errs, "budget", "BUDGET_ONEOF_VIOLATED"))
}

func TestPlanValidate_Budget_NeitherSet(t *testing.T) {
	p := validPlan()
	p.Budget.ReallocationThreshold = nil
	errs := p.Validate()
	assert.True(t, hasCode(errs, "budget", "BUDGET_ONEOF_VIOLATED"))
}

func TestPlanValidate_ThresholdZero_IsValid(t *testing.T) {
	// reallocation_threshold: 0 is valid (schema allows minimum: 0) — the
	// buyer is declaring "every reallocation needs approval". Distinct from
	// "neither field set", which is the oneOf violation. Load-bearing
	// security behavior; pin the exact value so future refactors can't
	// silently nil it.
	zero := 0.0
	p := validPlan()
	p.Budget.ReallocationThreshold = &zero
	assert.Empty(t, p.Validate())
	require.NotNil(t, p.Budget.ReallocationThreshold)
	assert.Equal(t, 0.0, *p.Budget.ReallocationThreshold)
}

func TestPlanValidate_RegulatedVertical_RequiresHumanReview(t *testing.T) {
	for _, category := range RegulatedHumanReviewCategories {
		t.Run(category, func(t *testing.T) {
			p := validPlan()
			p.PolicyCategories = []string{category}
			assert.True(t, hasCode(p.Validate(), "human_review_required", "HUMAN_REVIEW_REQUIRED"))

			p.HumanReviewRequired = true
			assert.Empty(t, p.Validate())
		})
	}
}

func TestPlanValidate_RegulatedVertical_Obfuscated(t *testing.T) {
	// Defense-in-depth: casing/whitespace variants must still trigger the
	// invariant. A schema validator matches const literally, so a buyer
	// shipping "Fair_Housing" passes the schema's if/then — Validate is the
	// backstop that catches common obfuscations.
	for _, obf := range []string{
		"Fair_Housing",
		" fair_housing",
		"fair_housing\t",
		"FAIR_HOUSING",
		"  Pharmaceutical_Advertising  ",
	} {
		t.Run(obf, func(t *testing.T) {
			p := validPlan()
			p.PolicyCategories = []string{obf}
			assert.True(t, hasCode(p.Validate(), "human_review_required", "HUMAN_REVIEW_REQUIRED"),
				"expected HUMAN_REVIEW_REQUIRED for obfuscated category %q", obf)
		})
	}
}

func TestPlanValidate_AnnexIII_RequiresHumanReview(t *testing.T) {
	for _, pid := range []string{
		"eu_ai_act_annex_iii",
		"EU_AI_Act_Annex_III",
		" eu_ai_act_annex_iii ",
	} {
		t.Run(pid, func(t *testing.T) {
			p := validPlan()
			p.PolicyIDs = []string{pid}
			assert.True(t, hasCode(p.Validate(), "human_review_required", "HUMAN_REVIEW_REQUIRED"))

			p.HumanReviewRequired = true
			assert.Empty(t, p.Validate())
		})
	}
}

func TestPlanValidate_UnregulatedCategory_NoHumanReviewRequired(t *testing.T) {
	// Generic categories that don't trigger the schema's if/then do not force
	// human_review_required. Guards against over-eager validation.
	p := validPlan()
	p.PolicyCategories = []string{"children_directed", "age_restricted"}
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_Objectives_MaxLength(t *testing.T) {
	p := validPlan()
	p.Objectives = strings.Repeat("a", 2001)
	assert.True(t, hasCode(p.Validate(), "objectives", "FIELD_TOO_LONG"))

	p.Objectives = strings.Repeat("a", 2000)
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_CustomPolicy_MaxLengths(t *testing.T) {
	p := validPlan()
	p.CustomPolicies = []PolicyEntry{{
		PolicyID:    "inline_1",
		Enforcement: "must",
		Policy:      strings.Repeat("p", 5001),
		Description: strings.Repeat("d", 501),
	}}
	errs := p.Validate()
	assert.True(t, hasCode(errs, "custom_policies[0].policy", "FIELD_TOO_LONG"))
	assert.True(t, hasCode(errs, "custom_policies[0].description", "FIELD_TOO_LONG"))
}

func TestPlanValidate_HumanOverride_RequiresEvidence(t *testing.T) {
	p := validPlan()
	p.HumanReviewRequired = false
	p.HumanOverride = &HumanOverride{} // empty — must be rejected
	errs := p.Validate()
	assert.True(t, hasCode(errs, "human_override.reason", "INVALID_OVERRIDE"))
	assert.True(t, hasCode(errs, "human_override.approver", "INVALID_OVERRIDE"))
}

func TestPlanValidate_HumanOverride_ShortReasonRejected(t *testing.T) {
	p := validPlan()
	p.HumanOverride = &HumanOverride{
		Reason:   "ok",
		Approver: "dpo@example.com",
	}
	assert.True(t, hasCode(p.Validate(), "human_override.reason", "INVALID_OVERRIDE"))
}

func TestPlanValidate_HumanOverride_ValidShape(t *testing.T) {
	p := validPlan()
	p.HumanOverride = &HumanOverride{
		Reason:     "DPO reviewed and approved automated processing under Art 22(2)(a)",
		Approver:   "dpo@example.com",
		ApprovedAt: "2026-04-15T12:00:00Z",
	}
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_HumanOverride_BadTimestamp(t *testing.T) {
	p := validPlan()
	p.HumanOverride = &HumanOverride{
		Reason:     "DPO reviewed and approved automated processing under Art 22(2)(a)",
		Approver:   "dpo@example.com",
		ApprovedAt: "not a timestamp",
	}
	assert.True(t, hasCode(p.Validate(), "human_override.approved_at", "INVALID_OVERRIDE"))
}

func TestPlanValidate_DataSubjectContestation_RequiresURLorEmail(t *testing.T) {
	p := validPlan()
	p.Brand.DataSubjectContestation = &DataSubjectContestation{Languages: []string{"en"}}
	assert.True(t, hasCode(p.Validate(), "brand.data_subject_contestation", "MISSING_FIELD"))
}

func TestPlanValidate_DataSubjectContestation_URLMustBeHTTPS(t *testing.T) {
	for _, u := range []string{
		"http://example.com/contest",
		"javascript:alert(1)",
		"ftp://example.com/",
		"not a url at all",
	} {
		t.Run(u, func(t *testing.T) {
			p := validPlan()
			p.Brand.DataSubjectContestation = &DataSubjectContestation{URL: u}
			assert.True(t, hasCode(p.Validate(), "brand.data_subject_contestation.url", "INVALID_FIELD"),
				"expected INVALID_FIELD for URL %q", u)
		})
	}
}

func TestPlanValidate_DataSubjectContestation_Valid(t *testing.T) {
	p := validPlan()
	p.Brand.DataSubjectContestation = &DataSubjectContestation{
		URL:       "https://example.com/contest",
		Email:     "dpo@example.com",
		Languages: []string{"en", "de"},
	}
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_Portfolio_RequiresMembers(t *testing.T) {
	p := validPlan()
	p.Portfolio = &PlanPortfolio{}
	assert.True(t, hasCode(p.Validate(), "portfolio.member_plan_ids", "MISSING_FIELD"))

	p.Portfolio.MemberPlanIDs = []string{"member-1"}
	assert.Empty(t, p.Validate())
}

func TestPlanValidate_Delegation_RequiredFields(t *testing.T) {
	p := validPlan()
	p.Delegations = []PlanDelegation{{}} // missing agent_url and authority
	errs := p.Validate()
	assert.True(t, hasCode(errs, "delegations[0].agent_url", "MISSING_FIELD"))
	assert.True(t, hasCode(errs, "delegations[0].authority", "MISSING_FIELD"))

	p.Delegations[0] = PlanDelegation{
		AgentURL:  "https://agent.example",
		Authority: "full",
	}
	assert.Empty(t, p.Validate())
}

func TestRestrictedAttribute_AgeAndFamilialStatus(t *testing.T) {
	// rc.4 adds age + familial_status to the restricted-attribute enum.
	p := validPlan()
	p.RestrictedAttributes = []RestrictedAttribute{
		RestrictedAttributeAge,
		RestrictedAttributeFamilialStatus,
	}
	b, err := json.Marshal(p)
	require.NoError(t, err)

	var decoded Plan
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, []RestrictedAttribute{"age", "familial_status"}, decoded.RestrictedAttributes)
}

func TestBrandReference_InlineOverrides_RoundTrip(t *testing.T) {
	brand := BrandReference{
		Domain:     "example.com",
		BrandID:    "spark",
		Industries: []string{"financial_services"},
		DataSubjectContestation: &DataSubjectContestation{
			URL:       "https://example.com/contestation",
			Languages: []string{"en", "de"},
		},
	}
	b, err := json.Marshal(brand)
	require.NoError(t, err)

	var decoded BrandReference
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, brand, decoded)

	skinny := BrandReference{Domain: "example.com"}
	sb, err := json.Marshal(skinny)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(sb, &raw))
	assert.NotContains(t, raw, "industries")
	assert.NotContains(t, raw, "data_subject_contestation")
	assert.NotContains(t, raw, "brand_id")
}

func TestSyncPlansRequest_RoundTrip(t *testing.T) {
	threshold := 25000.0
	req := SyncPlansRequest{
		IdempotencyKey: "req-0000000000000001",
		Plans: []Plan{{
			PlanID:     "campaign-q4",
			Brand:      &BrandReference{Domain: "example.com"},
			Objectives: "brand awareness",
			Budget: PlanBudget{
				Total:                 500000,
				Currency:              "USD",
				ReallocationThreshold: &threshold,
			},
			Flight: PlanFlight{
				Start: "2026-04-01T00:00:00Z",
				End:   "2026-06-30T23:59:59Z",
			},
		}},
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded SyncPlansRequest
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Len(t, decoded.Plans, 1)
	assert.Empty(t, decoded.Plans[0].Validate())
}
