package adcp

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// RegulatedHumanReviewCategories are policy categories whose presence on a plan
// requires plan.human_review_required = true under the schema's if/then
// invariant. These regimes are governed by GDPR Art 22 and EU AI Act Annex III,
// which prohibit solely automated decisions affecting data subjects.
var RegulatedHumanReviewCategories = []string{
	"fair_housing",
	"fair_lending",
	"fair_employment",
	"pharmaceutical_advertising",
}

// AnnexIIIPolicyIDs are policy IDs whose presence on a plan requires
// plan.human_review_required = true (EU AI Act Annex III high-risk categories).
var AnnexIIIPolicyIDs = []string{
	"eu_ai_act_annex_iii",
}

// Schema-derived maxLength caps. Enforced as defense-in-depth against
// prompt-injection-by-size: governance agents evaluate these fields with LLMs
// and unbounded input expands the attack surface.
const (
	maxObjectivesLen  = 2000 // sync-plans-request.plans[].objectives
	maxPolicyLen      = 5000 // policy-entry.policy
	maxDescriptionLen = 500  // policy-entry.description
	minOverrideReason = 20   // HumanOverride.reason minimum informational threshold
)

// PlanValidationError describes a single invariant violation on a Plan.
// Field is a JSON pointer-style path; Code is a stable machine-readable token
// suitable for direct mapping to AdCP INVALID_FIELD error responses.
type PlanValidationError struct {
	Field   string
	Code    string
	Message string
}

func (e PlanValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Validate enforces the cross-field invariants that the AdCP governance schema
// encodes as oneOf and if/then rules plus defense-in-depth size caps. Callers
// should run Validate before submitting a plan to a governance agent;
// governance-agent implementors MUST run it on receipt — Validate is advisory,
// not enforcing, and an agent that skips it accepts plans that violate the
// schema's load-bearing human-oversight invariants.
//
// Enforced invariants:
//   - budget.reallocation_threshold XOR budget.reallocation_unlimited must be set
//   - policy_categories ∋ regulated vertical ⇒ human_review_required = true
//     (case- and whitespace-insensitive to catch common obfuscations)
//   - policy_ids ∋ eu_ai_act_annex_iii ⇒ human_review_required = true (ditto)
//   - objectives ≤ 2000 chars (schema maxLength)
//   - custom_policies[].policy ≤ 5000 chars; .description ≤ 500 chars
//   - human_override.reason ≥ 20 chars; .approver parses as an email address;
//     .approved_at parses as RFC 3339 when non-empty
//   - portfolio.member_plan_ids required when portfolio is set
//   - delegations[].agent_url and .authority required
//   - brand.data_subject_contestation URL must be https; email must parse
//
// Not enforced (governance-agent responsibility): semantic industry matching,
// prompt-injection content filtering beyond size, registry vs inline policy
// segmentation in LLM prompts.
//
// Returns nil when the plan has no violations. Errors use stable codes —
// callers embedding Validate in a server should return the code, not the raw
// message, to avoid leaking the untrusted input values back to the caller.
func (p *Plan) Validate() []PlanValidationError {
	var errs []PlanValidationError

	errs = append(errs, p.Budget.validate("budget")...)

	if !p.HumanReviewRequired {
		if cat := firstMatchNormalized(p.PolicyCategories, RegulatedHumanReviewCategories); cat != "" {
			errs = append(errs, PlanValidationError{
				Field:   "human_review_required",
				Code:    "HUMAN_REVIEW_REQUIRED",
				Message: fmt.Sprintf("policy_categories %q requires human_review_required=true (GDPR Art 22 / EU AI Act Annex III)", cat),
			})
		}
		if pid := firstMatchNormalized(p.PolicyIDs, AnnexIIIPolicyIDs); pid != "" {
			errs = append(errs, PlanValidationError{
				Field:   "human_review_required",
				Code:    "HUMAN_REVIEW_REQUIRED",
				Message: fmt.Sprintf("policy_ids %q requires human_review_required=true (EU AI Act Annex III)", pid),
			})
		}
	}

	if utf8.RuneCountInString(p.Objectives) > maxObjectivesLen {
		errs = append(errs, PlanValidationError{
			Field:   "objectives",
			Code:    "FIELD_TOO_LONG",
			Message: fmt.Sprintf("objectives exceeds %d characters", maxObjectivesLen),
		})
	}

	for i, policy := range p.CustomPolicies {
		if utf8.RuneCountInString(policy.Policy) > maxPolicyLen {
			errs = append(errs, PlanValidationError{
				Field:   fmt.Sprintf("custom_policies[%d].policy", i),
				Code:    "FIELD_TOO_LONG",
				Message: fmt.Sprintf("policy exceeds %d characters", maxPolicyLen),
			})
		}
		if utf8.RuneCountInString(policy.Description) > maxDescriptionLen {
			errs = append(errs, PlanValidationError{
				Field:   fmt.Sprintf("custom_policies[%d].description", i),
				Code:    "FIELD_TOO_LONG",
				Message: fmt.Sprintf("description exceeds %d characters", maxDescriptionLen),
			})
		}
	}

	if p.HumanOverride != nil {
		errs = append(errs, p.HumanOverride.validate("human_override")...)
	}

	if p.Portfolio != nil && len(p.Portfolio.MemberPlanIDs) == 0 {
		errs = append(errs, PlanValidationError{
			Field:   "portfolio.member_plan_ids",
			Code:    "MISSING_FIELD",
			Message: "portfolio requires at least one member_plan_id",
		})
	}

	for i, d := range p.Delegations {
		if d.AgentURL == "" {
			errs = append(errs, PlanValidationError{
				Field:   fmt.Sprintf("delegations[%d].agent_url", i),
				Code:    "MISSING_FIELD",
				Message: "delegation requires agent_url",
			})
		}
		if d.Authority == "" {
			errs = append(errs, PlanValidationError{
				Field:   fmt.Sprintf("delegations[%d].authority", i),
				Code:    "MISSING_FIELD",
				Message: "delegation requires authority",
			})
		}
	}

	if p.Brand != nil && p.Brand.DataSubjectContestation != nil {
		errs = append(errs, p.Brand.DataSubjectContestation.validate("brand.data_subject_contestation")...)
	}

	return errs
}

// validate enforces the budget's oneOf constraint:
// exactly one of reallocation_threshold / reallocation_unlimited must be set.
func (b *PlanBudget) validate(path string) []PlanValidationError {
	hasThreshold := b.ReallocationThreshold != nil
	hasUnlimited := b.ReallocationUnlimited

	switch {
	case hasThreshold && hasUnlimited:
		return []PlanValidationError{{
			Field:   path,
			Code:    "BUDGET_ONEOF_VIOLATED",
			Message: "budget.reallocation_threshold and budget.reallocation_unlimited are mutually exclusive",
		}}
	case !hasThreshold && !hasUnlimited:
		return []PlanValidationError{{
			Field:   path,
			Code:    "BUDGET_ONEOF_VIOLATED",
			Message: "budget must set exactly one of reallocation_threshold or reallocation_unlimited",
		}}
	}
	return nil
}

// validate checks that a HumanOverride artifact carries enough evidence to
// justify downgrading plan.human_review_required from true to false.
func (h *HumanOverride) validate(path string) []PlanValidationError {
	var errs []PlanValidationError
	if utf8.RuneCountInString(strings.TrimSpace(h.Reason)) < minOverrideReason {
		errs = append(errs, PlanValidationError{
			Field:   path + ".reason",
			Code:    "INVALID_OVERRIDE",
			Message: fmt.Sprintf("reason must be at least %d characters", minOverrideReason),
		})
	}
	if _, err := mail.ParseAddress(h.Approver); err != nil {
		errs = append(errs, PlanValidationError{
			Field:   path + ".approver",
			Code:    "INVALID_OVERRIDE",
			Message: "approver must be a valid email address",
		})
	}
	if h.ApprovedAt != "" {
		if _, err := time.Parse(time.RFC3339, h.ApprovedAt); err != nil {
			errs = append(errs, PlanValidationError{
				Field:   path + ".approved_at",
				Code:    "INVALID_OVERRIDE",
				Message: "approved_at must be an RFC 3339 timestamp",
			})
		}
	}
	return errs
}

// validate enforces the schema's anyOf(url|email) and the https-only URL
// constraint on Art 22(3) contestation contacts.
func (d *DataSubjectContestation) validate(path string) []PlanValidationError {
	var errs []PlanValidationError
	if d.URL == "" && d.Email == "" {
		return []PlanValidationError{{
			Field:   path,
			Code:    "MISSING_FIELD",
			Message: "data_subject_contestation requires at least one of url or email",
		}}
	}
	if d.URL != "" {
		u, err := url.Parse(d.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, PlanValidationError{
				Field:   path + ".url",
				Code:    "INVALID_FIELD",
				Message: "url must be an https:// URL",
			})
		}
	}
	if d.Email != "" {
		if _, err := mail.ParseAddress(d.Email); err != nil {
			errs = append(errs, PlanValidationError{
				Field:   path + ".email",
				Code:    "INVALID_FIELD",
				Message: "email must be a valid email address",
			})
		}
	}
	return errs
}

// firstMatchNormalized returns the first `have` value whose normalized form is
// in `want`, or "" if none. Normalization lowercases and trims whitespace so a
// buyer cannot bypass an Annex III invariant by shipping "Fair_Housing" or
// " fair_housing " — the schema's if/then matches exact strings, but a
// defense-in-depth validator should catch common obfuscations too.
// The original (un-normalized) value is returned so error messages echo what
// the caller actually sent.
func firstMatchNormalized(have, want []string) string {
	set := make(map[string]struct{}, len(want))
	for _, v := range want {
		set[strings.ToLower(strings.TrimSpace(v))] = struct{}{}
	}
	for _, v := range have {
		if _, ok := set[strings.ToLower(strings.TrimSpace(v))]; ok {
			return v
		}
	}
	return ""
}
