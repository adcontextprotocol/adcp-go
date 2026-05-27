package adcp

import (
	"encoding/json"
	"testing"
)

type customOptimizationGoalTarget struct{}

func (customOptimizationGoalTarget) isOptimizationGoalTarget() {}

func hasValidationIssue(issues []ValidationIssue, field, code string) bool {
	for _, issue := range issues {
		if issue.Field == field && issue.Code == code {
			return true
		}
	}
	return false
}

func TestValidateOptimizationGoal_ValidMetricReach(t *testing.T) {
	goal := OptimizationGoal{
		Kind:      "metric",
		Metric:    "reach",
		ReachUnit: "households",
		TargetFrequency: &OptimizationGoalTargetFrequency{
			Min:    2,
			Max:    4,
			Window: Duration{Interval: 7, Unit: "days"},
		},
		Target: OptimizationGoalCostPerTarget{Value: 1.25},
	}

	if issues := goal.Validate(WithStrictEnums()); len(issues) != 0 {
		t.Fatalf("Validate issues = %#v, want none", issues)
	}
}

func TestValidateOptimizationGoal_ValidEventGoal(t *testing.T) {
	goal := OptimizationGoal{
		Kind: "event",
		EventSources: []OptimizationGoalEventSource{{
			EventSourceID: "pixel-1",
			EventType:     "purchase",
			ValueField:    "value",
		}},
		Target: OptimizationGoalPerAdSpendTarget{Value: 4},
		AttributionWindow: &OptimizationGoalAttributionWindow{
			PostClick: Duration{Interval: 7, Unit: "days"},
		},
	}

	if issues := ValidateOptimizationGoal(goal, WithStrictEnums()); len(issues) != 0 {
		t.Fatalf("ValidateOptimizationGoal issues = %#v, want none", issues)
	}
}

func TestValidateOptimizationGoal_RequiredFields(t *testing.T) {
	goal := OptimizationGoal{
		Kind: "event",
		EventSources: []OptimizationGoalEventSource{{
			EventType: "custom",
		}},
		AttributionWindow: &OptimizationGoalAttributionWindow{},
	}

	issues := goal.Validate()
	for _, want := range []struct {
		field string
		code  string
	}{
		{"event_sources[0].event_source_id", "REQUIRED_FIELD"},
		{"event_sources[0].custom_event_name", "REQUIRED_FIELD"},
		{"attribution_window.post_click.interval", "REQUIRED_FIELD"},
		{"attribution_window.post_click.unit", "REQUIRED_FIELD"},
	} {
		if !hasValidationIssue(issues, want.field, want.code) {
			t.Fatalf("Validate missing issue %s/%s in %#v", want.field, want.code, issues)
		}
	}
}

func TestValidateOptimizationGoal_TargetFrequencyRequiredWindow(t *testing.T) {
	goal := OptimizationGoal{
		Kind:      "metric",
		Metric:    "reach",
		ReachUnit: "individuals",
		TargetFrequency: &OptimizationGoalTargetFrequency{
			Min: 2,
		},
	}

	issues := goal.Validate()
	for _, want := range []struct {
		field string
		code  string
	}{
		{"target_frequency.window.interval", "REQUIRED_FIELD"},
		{"target_frequency.window.unit", "REQUIRED_FIELD"},
	} {
		if !hasValidationIssue(issues, want.field, want.code) {
			t.Fatalf("Validate missing issue %s/%s in %#v", want.field, want.code, issues)
		}
	}
}

func TestValidateOptimizationGoal_StrictEnumsAreOptIn(t *testing.T) {
	goal := OptimizationGoal{
		Kind:   "metric",
		Metric: "future_metric",
	}

	if issues := goal.Validate(); hasValidationIssue(issues, "metric", "UNKNOWN_ENUM_VALUE") {
		t.Fatalf("Validate reported strict enum issue without WithStrictEnums: %#v", issues)
	}
	if issues := goal.Validate(WithStrictEnums()); !hasValidationIssue(issues, "metric", "UNKNOWN_ENUM_VALUE") {
		t.Fatalf("Validate missing strict enum issue: %#v", issues)
	}

	future := OptimizationGoal{Kind: "future_goal_kind"}
	if issues := future.Validate(); hasValidationIssue(issues, "kind", "UNKNOWN_VARIANT") {
		t.Fatalf("Validate reported strict variant issue without WithStrictEnums: %#v", issues)
	}
	if issues := future.Validate(WithStrictEnums()); !hasValidationIssue(issues, "kind", "UNKNOWN_VARIANT") {
		t.Fatalf("Validate missing strict variant issue: %#v", issues)
	}
}

func TestValidateOptimizationGoal_EventSourceStrictEnum(t *testing.T) {
	goal := OptimizationGoal{
		Kind: "event",
		EventSources: []OptimizationGoalEventSource{{
			EventSourceID: "pixel-1",
			EventType:     "future_event",
		}},
	}

	if issues := goal.Validate(); hasValidationIssue(issues, "event_sources[0].event_type", "UNKNOWN_ENUM_VALUE") {
		t.Fatalf("Validate reported strict enum issue without WithStrictEnums: %#v", issues)
	}
	if issues := goal.Validate(WithStrictEnums()); !hasValidationIssue(issues, "event_sources[0].event_type", "UNKNOWN_ENUM_VALUE") {
		t.Fatalf("Validate missing strict event enum issue: %#v", issues)
	}
}

func TestValidateOptimizationGoal_TargetRules(t *testing.T) {
	metric := OptimizationGoal{
		Kind:   "metric",
		Metric: "clicks",
		Target: OptimizationGoalPerAdSpendTarget{Value: 0},
	}
	metricIssues := metric.Validate()
	if !hasValidationIssue(metricIssues, "target.kind", "UNSUPPORTED_TARGET_KIND") {
		t.Fatalf("Validate missing unsupported target issue: %#v", metricIssues)
	}
	if !hasValidationIssue(metricIssues, "target.value", "INVALID_VALUE") {
		t.Fatalf("Validate missing target value issue: %#v", metricIssues)
	}

	event := OptimizationGoal{
		Kind: "event",
		EventSources: []OptimizationGoalEventSource{{
			EventSourceID: "pixel-1",
			EventType:     "purchase",
		}},
		Target: OptimizationGoalMaximizeValueTarget{},
	}
	eventIssues := event.Validate()
	if !hasValidationIssue(eventIssues, "event_sources", "REQUIRED_FIELD") {
		t.Fatalf("Validate missing value_field requirement: %#v", eventIssues)
	}

	futureTarget := OptimizationGoal{
		Kind:   "metric",
		Metric: "clicks",
		Target: OptimizationGoalRawTarget{Kind: "future_target"},
	}
	if issues := futureTarget.Validate(); hasValidationIssue(issues, "target.kind", "UNKNOWN_VARIANT") {
		t.Fatalf("Validate reported strict raw target issue without WithStrictEnums: %#v", issues)
	}
	if issues := futureTarget.Validate(WithStrictEnums()); !hasValidationIssue(issues, "target.kind", "UNKNOWN_VARIANT") {
		t.Fatalf("Validate missing strict raw target issue: %#v", issues)
	}

	customTarget := OptimizationGoal{
		Kind:   "metric",
		Metric: "clicks",
		Target: customOptimizationGoalTarget{},
	}
	if issues := customTarget.Validate(); hasValidationIssue(issues, "target.kind", "UNKNOWN_VARIANT") {
		t.Fatalf("Validate reported strict custom target issue without WithStrictEnums: %#v", issues)
	}
	if issues := customTarget.Validate(WithStrictEnums()); !hasValidationIssue(issues, "target.kind", "UNKNOWN_VARIANT") {
		t.Fatalf("Validate missing strict custom target issue: %#v", issues)
	}
}

func TestValidateOptimizationGoal_DurationRules(t *testing.T) {
	goal := OptimizationGoal{
		Kind:      "metric",
		Metric:    "reach",
		ReachUnit: "devices",
		TargetFrequency: &OptimizationGoalTargetFrequency{
			Max:    3,
			Window: Duration{Interval: 2, Unit: "campaign"},
		},
	}
	issues := goal.Validate(WithStrictEnums())
	if !hasValidationIssue(issues, "target_frequency.window.interval", "INVALID_VALUE") {
		t.Fatalf("Validate missing campaign interval issue: %#v", issues)
	}

	goal.TargetFrequency.Window.Unit = "fortnights"
	issues = goal.Validate(WithStrictEnums())
	if !hasValidationIssue(issues, "target_frequency.window.unit", "UNKNOWN_ENUM_VALUE") {
		t.Fatalf("Validate missing duration unit enum issue: %#v", issues)
	}
}

func TestValidateOptimizationGoal_PointerTargets(t *testing.T) {
	tests := []struct {
		name         string
		goalKind     string
		eventSources []OptimizationGoalEventSource
		target       OptimizationGoalTarget
		field        string
		code         string
	}{
		{
			name:     "cost per requires positive value",
			goalKind: "metric",
			target:   &OptimizationGoalCostPerTarget{},
			field:    "target.value",
			code:     "INVALID_VALUE",
		},
		{
			name:     "threshold rate rejected for event",
			goalKind: "event",
			eventSources: []OptimizationGoalEventSource{{
				EventSourceID: "pixel-1",
				EventType:     "purchase",
			}},
			target: &OptimizationGoalThresholdRateTarget{Value: 1},
			field:  "target.kind",
			code:   "UNSUPPORTED_TARGET_KIND",
		},
		{
			name:     "per ad spend rejected for metric",
			goalKind: "metric",
			target:   &OptimizationGoalPerAdSpendTarget{Value: 1},
			field:    "target.kind",
			code:     "UNSUPPORTED_TARGET_KIND",
		},
		{
			name:     "maximize value rejected for metric",
			goalKind: "metric",
			target:   &OptimizationGoalMaximizeValueTarget{},
			field:    "target.kind",
			code:     "UNSUPPORTED_TARGET_KIND",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goal := OptimizationGoal{
				Kind:         tt.goalKind,
				Metric:       "clicks",
				EventSources: tt.eventSources,
				Target:       tt.target,
			}
			issues := goal.Validate()
			if !hasValidationIssue(issues, tt.field, tt.code) {
				t.Fatalf("Validate missing issue %s/%s in %#v", tt.field, tt.code, issues)
			}
		})
	}
}

func TestValidateOptimizationGoal_UnmarshaledPointerTarget(t *testing.T) {
	var goal OptimizationGoal
	if err := json.Unmarshal([]byte(`{
		"kind": "event",
		"event_sources": [{"event_source_id": "pixel-1", "event_type": "purchase"}],
		"target": {"kind": "per_ad_spend", "value": 4}
	}`), &goal); err != nil {
		t.Fatalf("json.Unmarshal optimization goal: %v", err)
	}
	if _, ok := goal.Target.(*OptimizationGoalPerAdSpendTarget); !ok {
		t.Fatalf("target should decode as *OptimizationGoalPerAdSpendTarget, got %#v", goal.Target)
	}
	if issues := goal.Validate(); !hasValidationIssue(issues, "event_sources", "REQUIRED_FIELD") {
		t.Fatalf("Validate missing value_field requirement for decoded target: %#v", issues)
	}

	goal.EventSources[0].ValueField = "value"
	if issues := goal.Validate(); len(issues) != 0 {
		t.Fatalf("Validate issues after value_field = %#v, want none", issues)
	}
}

func TestValidateOptimizationGoal_NumericRules(t *testing.T) {
	goal := OptimizationGoal{
		Kind:                "metric",
		Metric:              "views",
		Priority:            -1,
		ViewDurationSeconds: -1,
		TargetFrequency: &OptimizationGoalTargetFrequency{
			Window: Duration{Interval: 7, Unit: "days"},
		},
	}

	issues := goal.Validate()
	for _, want := range []struct {
		field string
		code  string
	}{
		{"priority", "INVALID_VALUE"},
		{"view_duration_seconds", "INVALID_VALUE"},
		{"target_frequency", "REQUIRED_FIELD"},
	} {
		if !hasValidationIssue(issues, want.field, want.code) {
			t.Fatalf("Validate missing issue %s/%s in %#v", want.field, want.code, issues)
		}
	}

	goal.TargetFrequency.Min = -1
	issues = goal.Validate()
	if !hasValidationIssue(issues, "target_frequency.min", "INVALID_VALUE") {
		t.Fatalf("Validate missing target_frequency.min issue: %#v", issues)
	}

	goal.TargetFrequency.Min = 1
	goal.TargetFrequency.Max = -1
	issues = goal.Validate()
	if !hasValidationIssue(issues, "target_frequency.max", "INVALID_VALUE") {
		t.Fatalf("Validate missing target_frequency.max issue: %#v", issues)
	}
}
