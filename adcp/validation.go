package adcp

import "fmt"

// ValidationIssue describes one opt-in SDK validation finding.
// Field is a JSON-style field path; Code is a stable machine-readable token
// suitable for mapping to AdCP INVALID_FIELD responses.
type ValidationIssue struct {
	Field   string
	Code    string
	Message string
}

func (i ValidationIssue) Error() string {
	return fmt.Sprintf("%s: %s", i.Field, i.Message)
}

type validationConfig struct {
	strictEnums bool
}

// ValidationOption configures opt-in SDK validators.
type ValidationOption func(*validationConfig)

// WithStrictEnums reports values outside the current schema enum or oneOf
// variant set. Validators are forward-compatible by default and only require
// current-schema membership when this option is supplied.
func WithStrictEnums() ValidationOption {
	return func(cfg *validationConfig) {
		cfg.strictEnums = true
	}
}

func newValidationConfig(opts []ValidationOption) validationConfig {
	var cfg validationConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// ValidateOptimizationGoal checks required fields and current-schema invariants
// that Go zero values cannot express. It is intentionally not a full JSON
// Schema validator: unknown enum values are allowed unless WithStrictEnums is
// supplied, and product/account capability checks still belong to the seller.
func ValidateOptimizationGoal(goal OptimizationGoal, opts ...ValidationOption) []ValidationIssue {
	return goal.Validate(opts...)
}

// Validate checks required fields and current-schema invariants that Go zero
// values cannot express. Call it before submitting package requests or updates
// that include optimization_goals.
func (g OptimizationGoal) Validate(opts ...ValidationOption) []ValidationIssue {
	cfg := newValidationConfig(opts)
	var issues []ValidationIssue

	switch g.Kind {
	case "":
		issues = appendRequired(issues, "kind")
		return issues
	case "metric":
		issues = append(issues, validateMetricOptimizationGoal(g, cfg)...)
	case "event":
		issues = append(issues, validateEventOptimizationGoal(g, cfg)...)
	default:
		if cfg.strictEnums {
			issues = appendUnknownVariant(issues, "kind")
		}
	}

	if g.Priority != 0 && g.Priority < 1 {
		issues = append(issues, ValidationIssue{
			Field:   "priority",
			Code:    "INVALID_VALUE",
			Message: "priority must be at least 1 when set",
		})
	}

	return issues
}

func validateMetricOptimizationGoal(g OptimizationGoal, cfg validationConfig) []ValidationIssue {
	var issues []ValidationIssue

	if g.Metric == "" {
		issues = appendRequired(issues, "metric")
	} else if cfg.strictEnums && !IsKnownOptimizationMetric(g.Metric) {
		issues = appendUnknownEnum(issues, "metric")
	}

	if g.Metric == "reach" {
		if g.ReachUnit == "" {
			issues = appendRequired(issues, "reach_unit")
		} else if cfg.strictEnums && !IsKnownReachUnit(g.ReachUnit) {
			issues = appendUnknownEnum(issues, "reach_unit")
		}
	}
	if g.TargetFrequency != nil {
		issues = append(issues, g.TargetFrequency.validate("target_frequency", cfg)...)
	}
	if g.ViewDurationSeconds < 0 {
		issues = append(issues, ValidationIssue{
			Field:   "view_duration_seconds",
			Code:    "INVALID_VALUE",
			Message: "view_duration_seconds must not be negative",
		})
	}
	if !isNilOptimizationGoalTarget(g.Target) {
		issues = append(issues, validateOptimizationGoalTarget(g.Target, "metric", "target", cfg)...)
	}

	return issues
}

func validateEventOptimizationGoal(g OptimizationGoal, cfg validationConfig) []ValidationIssue {
	var issues []ValidationIssue

	if len(g.EventSources) == 0 {
		issues = appendRequired(issues, "event_sources")
	}
	for i, source := range g.EventSources {
		issues = append(issues, source.validate(fmt.Sprintf("event_sources[%d]", i), cfg)...)
	}
	if !isNilOptimizationGoalTarget(g.Target) {
		issues = append(issues, validateOptimizationGoalTarget(g.Target, "event", "target", cfg)...)
		if optimizationTargetNeedsValueField(g.Target) && !eventSourcesIncludeValueField(g.EventSources) {
			issues = append(issues, ValidationIssue{
				Field:   "event_sources",
				Code:    "REQUIRED_FIELD",
				Message: "at least one event source value_field is required for this target kind",
			})
		}
	}
	if g.AttributionWindow != nil {
		issues = append(issues, g.AttributionWindow.validate("attribution_window", cfg)...)
	}

	return issues
}

func (f *OptimizationGoalTargetFrequency) validate(path string, cfg validationConfig) []ValidationIssue {
	if f == nil {
		return nil
	}
	var issues []ValidationIssue

	if f.Min == 0 && f.Max == 0 {
		issues = append(issues, ValidationIssue{
			Field:   path,
			Code:    "REQUIRED_FIELD",
			Message: "min or max must be set to a positive value",
		})
	}
	if f.Min < 0 {
		issues = append(issues, ValidationIssue{
			Field:   path + ".min",
			Code:    "INVALID_VALUE",
			Message: "min must be greater than 0 when set",
		})
	}
	if f.Max < 0 {
		issues = append(issues, ValidationIssue{
			Field:   path + ".max",
			Code:    "INVALID_VALUE",
			Message: "max must be greater than 0 when set",
		})
	}
	if f.Min > 0 && f.Max > 0 && f.Max < f.Min {
		issues = append(issues, ValidationIssue{
			Field:   path + ".max",
			Code:    "INVALID_VALUE",
			Message: "max must be greater than or equal to min",
		})
	}
	issues = append(issues, f.Window.validate(path+".window", cfg)...)

	return issues
}

func (s OptimizationGoalEventSource) validate(path string, cfg validationConfig) []ValidationIssue {
	var issues []ValidationIssue

	if s.EventSourceID == "" {
		issues = appendRequired(issues, path+".event_source_id")
	}
	if s.EventType == "" {
		issues = appendRequired(issues, path+".event_type")
	} else {
		if cfg.strictEnums && !IsKnownEventType(s.EventType) {
			issues = appendUnknownEnum(issues, path+".event_type")
		}
		if s.EventType == "custom" && s.CustomEventName == "" {
			issues = appendRequired(issues, path+".custom_event_name")
		}
	}

	return issues
}

func (w *OptimizationGoalAttributionWindow) validate(path string, cfg validationConfig) []ValidationIssue {
	if w == nil {
		return nil
	}
	issues := w.PostClick.validate(path+".post_click", cfg)
	if w.PostView != nil {
		issues = append(issues, w.PostView.validate(path+".post_view", cfg)...)
	}
	return issues
}

func (d Duration) validate(path string, cfg validationConfig) []ValidationIssue {
	var issues []ValidationIssue

	if d.Interval == 0 {
		issues = appendRequired(issues, path+".interval")
	} else if d.Interval < 0 {
		issues = append(issues, ValidationIssue{
			Field:   path + ".interval",
			Code:    "INVALID_VALUE",
			Message: "interval must be at least 1",
		})
	}
	if d.Unit == "" {
		issues = appendRequired(issues, path+".unit")
	} else {
		if cfg.strictEnums && !isKnownDurationUnit(d.Unit) {
			issues = appendUnknownEnum(issues, path+".unit")
		}
		if d.Unit == "campaign" && d.Interval > 0 && d.Interval != 1 {
			issues = append(issues, ValidationIssue{
				Field:   path + ".interval",
				Code:    "INVALID_VALUE",
				Message: "interval must be 1 when unit is campaign",
			})
		}
	}

	return issues
}

func validateOptimizationGoalTarget(target OptimizationGoalTarget, goalKind, path string, cfg validationConfig) []ValidationIssue {
	var issues []ValidationIssue

	switch t := target.(type) {
	case *OptimizationGoalCostPerTarget:
		issues = append(issues, validatePositiveTargetValue(t.Value, path+".value")...)
	case OptimizationGoalCostPerTarget:
		issues = append(issues, validatePositiveTargetValue(t.Value, path+".value")...)
	case *OptimizationGoalThresholdRateTarget:
		if goalKind != "metric" {
			issues = append(issues, unsupportedTarget(path+".kind", "threshold_rate", goalKind))
		}
		issues = append(issues, validatePositiveTargetValue(t.Value, path+".value")...)
	case OptimizationGoalThresholdRateTarget:
		if goalKind != "metric" {
			issues = append(issues, unsupportedTarget(path+".kind", "threshold_rate", goalKind))
		}
		issues = append(issues, validatePositiveTargetValue(t.Value, path+".value")...)
	case *OptimizationGoalPerAdSpendTarget:
		if goalKind != "event" {
			issues = append(issues, unsupportedTarget(path+".kind", "per_ad_spend", goalKind))
		}
		issues = append(issues, validatePositiveTargetValue(t.Value, path+".value")...)
	case OptimizationGoalPerAdSpendTarget:
		if goalKind != "event" {
			issues = append(issues, unsupportedTarget(path+".kind", "per_ad_spend", goalKind))
		}
		issues = append(issues, validatePositiveTargetValue(t.Value, path+".value")...)
	case *OptimizationGoalMaximizeValueTarget:
		if goalKind != "event" {
			issues = append(issues, unsupportedTarget(path+".kind", "maximize_value", goalKind))
		}
	case OptimizationGoalMaximizeValueTarget:
		if goalKind != "event" {
			issues = append(issues, unsupportedTarget(path+".kind", "maximize_value", goalKind))
		}
	case *OptimizationGoalRawTarget:
		issues = append(issues, validateRawOptimizationGoalTarget(t, path, cfg)...)
	case OptimizationGoalRawTarget:
		issues = append(issues, validateRawOptimizationGoalTarget(&t, path, cfg)...)
	default:
		if cfg.strictEnums {
			issues = appendUnknownVariant(issues, path+".kind")
		}
	}

	return issues
}

func validateRawOptimizationGoalTarget(target *OptimizationGoalRawTarget, path string, cfg validationConfig) []ValidationIssue {
	if target == nil {
		return nil
	}
	if target.Kind != "" {
		if cfg.strictEnums {
			return appendUnknownVariant(nil, path+".kind")
		}
		return nil
	}
	return []ValidationIssue{{
		Field:   path + ".kind",
		Code:    "REQUIRED_FIELD",
		Message: "kind is required",
	}}
}

func validatePositiveTargetValue(value float64, path string) []ValidationIssue {
	if value > 0 {
		return nil
	}
	return []ValidationIssue{{
		Field:   path,
		Code:    "INVALID_VALUE",
		Message: "value must be greater than 0",
	}}
}

func optimizationTargetNeedsValueField(target OptimizationGoalTarget) bool {
	// The schema describes per_ad_spend/maximize_value as value-based goals. The
	// value_field requirement is an SDK invariant derived from those descriptions,
	// not a complete JSON Schema validation pass.
	switch target.(type) {
	case *OptimizationGoalPerAdSpendTarget, OptimizationGoalPerAdSpendTarget,
		*OptimizationGoalMaximizeValueTarget, OptimizationGoalMaximizeValueTarget:
		return true
	default:
		return false
	}
}

func eventSourcesIncludeValueField(sources []OptimizationGoalEventSource) bool {
	for _, source := range sources {
		if source.ValueField != "" {
			return true
		}
	}
	return false
}

func unsupportedTarget(path, targetKind, goalKind string) ValidationIssue {
	return ValidationIssue{
		Field:   path,
		Code:    "UNSUPPORTED_TARGET_KIND",
		Message: fmt.Sprintf("%s target is not supported for %s goals", targetKind, goalKind),
	}
}

func appendRequired(issues []ValidationIssue, field string) []ValidationIssue {
	return append(issues, ValidationIssue{
		Field:   field,
		Code:    "REQUIRED_FIELD",
		Message: "field is required",
	})
}

func appendUnknownEnum(issues []ValidationIssue, field string) []ValidationIssue {
	return append(issues, ValidationIssue{
		Field:   field,
		Code:    "UNKNOWN_ENUM_VALUE",
		Message: "value is not in the current schema enum",
	})
}

func appendUnknownVariant(issues []ValidationIssue, field string) []ValidationIssue {
	return append(issues, ValidationIssue{
		Field:   field,
		Code:    "UNKNOWN_VARIANT",
		Message: "value is not a current schema variant",
	})
}

func isKnownDurationUnit(v string) bool {
	switch v {
	case "seconds", "minutes", "hours", "days", "campaign":
		return true
	default:
		return false
	}
}
