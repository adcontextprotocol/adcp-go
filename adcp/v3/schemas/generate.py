#!/usr/bin/env python3
"""Generate Go types from AdCP JSON schemas.

Usage:
    python3 generate.py > ../types_gen.go

Reads all JSON schemas from the current directory (downloaded from
adcontextprotocol/adcp/static/schemas/source/) and generates Go structs
with proper json tags matching the wire format.
"""

import argparse
import contextlib
import io
import json
import re
import subprocess
import sys
from pathlib import Path
from collections import OrderedDict

SCRIPT_DIR = Path(__file__).resolve().parent
SCHEMA_RESOLUTION_ERRORS = (
    OSError,
    json.JSONDecodeError,
    KeyError,
    ValueError,
    IndexError,
    TypeError,
)

# Types that exist in hand-written files or will be generated.
# $ref targets not in this set get replaced with `any`.
#
# To remove a type from this list: the hand-written definition in types.go
# (or inputs.go, etc.) must be deleted first, then the generator will emit
# the type from its JSON schema. Types remain here when:
#   - schema is oneOf with union fields that need hand-written flattening
#   - type is an inline response-item with no standalone schema file
#   - generator cannot produce the shape we want (nested inline arrays)
#   - type has attached methods or helpers
KNOWN_TYPES = {
    # Inline response items / nested shapes — no standalone schema file
    'CapabilitiesData', 'ADCPVersion', 'BrandReference', 'AccountReference',
    'AccountResult', 'AccountSetup', 'GovernanceResult', 'GovernanceAccount',
    'GovernanceAgent', 'ProductsData',
    'MediaBuyListItem', 'MediaBuyData', 'MediaBuyHistoryEntry',
    'PackageStatus', 'PackageCreativeApproval', 'PackageSnapshot',
    'DeliveryData', 'ReportingPeriod',
    'Render', 'AssetSlot', 'CreativeResult', 'CreativeListItem', 'PreviewResult',
    'Preview', 'PreviewRender', 'BuildCreativeResult', 'SignalID',
    'SignalPricing', 'ActivationKey', 'CatalogResult',
    'EventSourceResult', 'LogEventResult',
    'CheckGovernanceCondition',
    # oneOf schemas — hand-writing flattens the union into a single struct with
    # all variant fields.
    'PricingOption', 'Deployment', 'PublisherPropertySelector',
    'OptimizationGoal',
    'OptimizationGoalCostPerTarget', 'OptimizationGoalThresholdRateTarget',
    'OptimizationGoalPerAdSpendTarget', 'OptimizationGoalMaximizeValueTarget',
    # From inputs.go (hand-written types that need custom Go code)
    'EmptyInput',
    'AccountInput', 'GovernanceAccountInput',
    'CreativeInput', 'SyncCreativeAssignment',
    'CatalogInput', 'EventSourceInput', 'DestinationInput',
    'CreativeFilters', 'SignalFilters',
    # From errors.go
    'Error', 'ErrorOptions',
    # From responses.go
    'SyncCreativesResponse', 'SyncAccountsResponse', 'GovernanceResponse',
    'ListCreativesResponse', 'PreviewCreativeResponse', 'BuildCreativeResponse',
    'CreativeFormatsResponse', 'SignalsResponse', 'ActivateSignalResponse',
    'SyncCatalogsResponse', 'SyncEventSourcesResponse', 'LogEventResponse',
    'PerformanceFeedbackResponse',
    # From testcontroller.go
    'TestControllerStore', 'StateTransition', 'SimulateDeliveryParams',
    'ReportedSpend', 'SimulateBudgetParams', 'SimulationResult',
    'TestControllerError',
    # Collection domain (from types.go)
    'CollectionList', 'CollectionListFilters', 'BaseCollectionSource',
    'DistributionID', 'ContentRating', 'ResolvedCollection',
    'CollectionPagination',
    # Collection response names (conflict with response builder funcs in responses.go)
    'CreateCollectionListResponse', 'GetCollectionListResponse',
    'UpdateCollectionListResponse', 'DeleteCollectionListResponse',
    'ListCollectionListsResponse',
    # New core types (from types.go)
    'Duration', 'CancellationPolicy', 'CancellationFee',
    'CollectionListRef', 'CreativeAssignment', 'CreativeConsumption', 'IndustryIdentifier',
    'MeasurementTerms', 'BillingMeasurement', 'MakegoodPolicy',
    'MeasurementWindow', 'PerformanceStandard', 'VendorPricingOption',
    # Hand-written in types.go; listed here so $ref resolution uses the typed name.
    'AttributionWindow',
    # 3.0 capability blocks (hand-written in types.go)
    'IdempotencyCaps', 'AccountCapabilities', 'MediaBuyCapabilities',
    'MediaBuyExecution', 'TrustedMatchCaps', 'CreativeSpecsCaps',
    'TargetingCaps', 'GeoMetrosCaps', 'GeoPostalAreasCaps',
    'GeoProximityCaps', 'AgeRestrictionCaps', 'KeywordMatchCaps',
    'AudienceTargetingCaps', 'MatchingLatencyRange', 'ConversionTrackingCaps',
    'AttributionWindowOption', 'ContentStandardsCaps', 'PortfolioCaps',
    'SignalsCapabilities', 'GovernanceCapabilities', 'GovernanceFeature',
    'FeatureRange', 'SICapabilities', 'SIEndpoint', 'SITransport',
    'BrandCapabilities', 'CreativeCapabilities', 'RequestSigningCapabilities',
    'WebhookSigningCapabilities', 'IdentityCapabilities', 'IdentityKeyOrigins',
    'IdentityCompromiseNotification', 'ComplianceTestingCapabilities',
    # Governance plan types (from governance_types.go) — the plans array
    # in sync-plans-request.json is an inline nested object, not a $ref,
    # so the generator cannot produce these on its own.
    'Plan', 'PlanBudget', 'PlanBudgetAllocation', 'PlanFlight',
    'PlanChannels', 'PlanChannelMixTarget', 'PlanDelegation',
    'PlanDelegationBudget', 'PlanPortfolio', 'PlanPortfolioBudgetCap',
    'HumanOverride', 'DataSubjectContestation',
}

# Schemas we want to generate Go types for (request/response pairs for each tool)
TOOL_SCHEMAS = [
    # Protocol
    "protocol/get-adcp-capabilities-request.json",
    "protocol/get-adcp-capabilities-response.json",
    # Account
    "account/sync-accounts-request.json",
    "account/sync-accounts-response.json",
    "account/sync-governance-request.json",
    "account/sync-governance-response.json",
    "account/list-accounts-request.json",
    "account/list-accounts-response.json",
    # Media buy
    "media-buy/get-products-request.json",
    "media-buy/get-products-response.json",
    "media-buy/create-media-buy-request.json",
    "media-buy/create-media-buy-response.json",
    "media-buy/update-media-buy-request.json",
    "media-buy/get-media-buys-request.json",
    "media-buy/get-media-buys-response.json",
    "media-buy/get-media-buy-delivery-request.json",
    "media-buy/get-media-buy-delivery-response.json",
    "media-buy/list-creative-formats-request.json",
    "media-buy/list-creative-formats-response.json",
    "media-buy/sync-catalogs-request.json",
    "media-buy/sync-catalogs-response.json",
    "media-buy/sync-event-sources-request.json",
    "media-buy/sync-event-sources-response.json",
    "media-buy/log-event-request.json",
    "media-buy/log-event-response.json",
    "media-buy/provide-performance-feedback-request.json",
    "media-buy/provide-performance-feedback-response.json",
    "media-buy/build-creative-request.json",
    "media-buy/build-creative-response.json",
    "media-buy/refine-proposals-request.json",
    "media-buy/refine-proposals-response.json",
    # Creative
    "creative/sync-creatives-request.json",
    "creative/sync-creatives-response.json",
    "creative/list-creatives-request.json",
    "creative/list-creatives-response.json",
    "creative/preview-creative-request.json",
    "creative/preview-creative-response.json",
    "creative/list-creative-formats-request.json",
    "creative/list-creative-formats-response.json",
    # Signals
    "signals/get-signals-request.json",
    "signals/get-signals-response.json",
    "signals/activate-signal-request.json",
    "signals/activate-signal-response.json",
    # Compliance
    "compliance/comply-test-controller-request.json",
    "compliance/comply-test-controller-response.json",
    # Governance
    "governance/sync-plans-request.json",
    "governance/sync-plans-response.json",
    "governance/check-governance-request.json",
    "governance/check-governance-response.json",
    "governance/report-plan-outcome-request.json",
    "governance/report-plan-outcome-response.json",
    "governance/get-plan-audit-logs-request.json",
    "governance/get-plan-audit-logs-response.json",
    # Collection
    "collection/create-collection-list-request.json",
    "collection/create-collection-list-response.json",
    "collection/get-collection-list-request.json",
    "collection/get-collection-list-response.json",
    "collection/update-collection-list-request.json",
    "collection/update-collection-list-response.json",
    "collection/delete-collection-list-request.json",
    "collection/delete-collection-list-response.json",
    "collection/list-collection-lists-request.json",
    "collection/list-collection-lists-response.json",
]

# Webhook payload schemas. PR adcontextprotocol/adcp#2417 made `idempotency_key`
# a required field on every webhook payload so receivers have a single canonical
# dedup field. Generating these types makes that field a typed `string` at the
# source level, not `any`.
WEBHOOK_SCHEMAS = [
    "core/mcp-webhook-payload.json",
    "collection/collection-list-changed-webhook.json",
    "property/property-list-changed-webhook.json",
    "content-standards/artifact-webhook-payload.json",
    "brand/revocation-notification.json",
]

# Core types that tools reference via $ref
CORE_SCHEMAS = [
    "governance/policy-entry.json",
    "governance/policy-category-definition.json",
    "governance/audience-constraints.json",
    "core/audience-selector.json",
    "core/product.json",
    "core/product-filters.json",
    "core/data-provider-signal-selector.json",
    "core/placement.json",
    "core/delivery-forecast.json",
    "core/forecast-point.json",
    "core/forecast-range.json",
    "core/proposal.json",
    "core/canonical-proposal.json",
    "core/canonical-product.json",
    "core/placement-ref.json",
    "core/product-format-declaration.json",
    "core/media-buy-available-action.json",
    "core/product-signal-targeting-option.json",
    "core/postal-area.json",
    "core/product-allocation.json",
    "core/insertion-order.json",
    "core/outcome-measurement.json",
    "core/reporting-capabilities.json",
    "core/geo-breakdown-support.json",
    "core/creative-policy.json",
    "core/measurement-readiness.json",
    "core/diagnostic-issue.json",
    "core/collection-selector.json",
    "core/package.json",
    "core/optimization-goal.json",
    "pricing-options/price-breakdown.json",
    "core/media-buy.json",
    "core/pricing-option.json",
    "core/format.json",
    "core/format-id.json",
    "core/creative-asset.json",
    "core/creative-manifest.json",
    "core/provenance.json",
    "core/deployment.json",
    "core/destination.json",
    "core/activation-key.json",
    "core/signal-definition.json",
    "core/signal-coverage-forecast.json",
    "core/signal-id.json",
    "core/signal-targeting.json",
    "core/signal-pricing-option.json",
    "core/signal-pricing.json",
    "core/delivery-metrics.json",
    "core/account.json",
    "core/account-ref.json",
    "core/targeting.json",
    "core/context.json",
    "core/version-envelope.json",
    "core/ext.json",
    "core/error.json",
    "core/pagination-response.json",
    "core/pagination-request.json",
    "core/catalog.json",
    "core/catalog-field-mapping.json",
    "core/event.json",
    "core/event-custom-data.json",
    "core/performance-feedback.json",
    "core/creative-brief.json",
    "core/reference-asset.json",
    "core/business-entity.json",
    "core/cancellation-policy.json",
    "core/collection-list-ref.json",
    "core/creative-consumption.json",
    "core/datetime-range.json",
    "core/daypart-target.json",
    "core/frequency-cap.json",
    "core/industry-identifier.json",
    "core/measurement-terms.json",
    "core/measurement-window.json",
    "core/planned-delivery.json",
    "core/performance-standard.json",
    "core/property-list-ref.json",
    "core/push-notification-config.json",
    "core/reporting-webhook.json",
    "core/rights-constraint.json",
    "core/user-match.json",
    "core/vendor-pricing-option.json",
    "core/content-rating.json",
    "core/installment.json",
    "core/special.json",
    "core/talent.json",
    "content-standards/artifact.json",
    "core/ad-inventory-config.json",
    "core/account-authorization.json",
    "core/committed-metric.json",
    "core/installment-deadlines.json",
    "core/material-deadline.json",
    "core/duration.json",
    "creative/audit-observation.json",
]

# Schemas that are not standalone tool requests/responses, but are important
# SDK input shapes referenced by tool schemas.
SUPPORT_SCHEMAS = [
    "media-buy/package-request.json",
    "media-buy/package-update.json",
    "media-buy/proposal-refinement.json",
    "media-buy/proposal-budget-constraint.json",
    "media-buy/product-discovery-criteria.json",
    "core/signal-ref.json",
    "core/delivery-metric-aggregate.json",
    "core/account-with-authorization.json",
    "core/missing-metric.json",
]

# Named types generated from inline JSON Schema pointers. This is the first step
# toward making the Go SDK generator own composed/nested shapes instead of
# relying on hand-written approximations.
INLINE_SCHEMA_TYPES = OrderedDict([
    (
        "RefinementConstraints",
        "media-buy/proposal-refinement.json#/properties/constraints",
    ),
    (
        "CPMConstraint",
        "media-buy/proposal-refinement.json#/properties/constraints/properties/cpm",
    ),
    (
        "ImpressionsConstraint",
        "media-buy/proposal-refinement.json#/properties/constraints/properties/impressions",
    ),
    (
        "FlightConstraint",
        "media-buy/proposal-refinement.json#/properties/constraints/properties/flight",
    ),
    (
        "AlternativesRequest",
        "media-buy/proposal-refinement.json#/properties/alternatives",
    ),
    (
        "RefinementResult",
        "media-buy/refine-proposals-response.json#/properties/results/items",
    ),
    (
        "RefinementCapability",
        "protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/proposal_refinement",
    ),
    (
        "ForecastPointDimension",
        "core/forecast-point-dimensions.json#/items",
    ),
    (
        "ReachWindow",
        "core/delivery-metrics.json#/properties/reach_window",
    ),
    (
        "MetricQualifier",
        "core/committed-metric.json#/oneOf/0/properties/qualifier",
    ),
    (
        "RequestedCommittedMetric",
        "media-buy/package-request.json#/properties/committed_metrics/items",
    ),
    (
        "ForecastViewability",
        "core/forecast-point.json#/properties/viewability",
    ),
    (
        "ReportingVendorMetric",
        "core/reporting-capabilities.json#/properties/vendor_metrics/items",
    ),
    (
        "RequiredVendorMetric",
        "core/product-filters.json#/properties/required_vendor_metrics/items",
    ),
    (
        "CreativeProvenanceRequirements",
        "core/creative-policy.json#/properties/provenance_requirements",
    ),
    (
        "CreativeAcceptedVerifier",
        "core/creative-policy.json#/properties/accepted_verifiers/items",
    ),
    (
        "ProvenanceEmbeddedProvenance",
        "core/provenance.json#/properties/embedded_provenance/items",
    ),
    (
        "ProvenanceWatermark",
        "core/provenance.json#/properties/watermarks/items",
    ),
    (
        "ProvenanceVerifyAgent",
        "core/provenance.json#/properties/embedded_provenance/items/properties/verify_agent",
    ),
    (
        "SignalTaxonomy",
        "core/signal-definition.json#/properties/taxonomy",
    ),
    (
        "SignalTaxonomyValue",
        "core/signal-definition.json#/properties/taxonomy/properties/values/items",
    ),
    (
        "SignalTaxonomyValueMapping",
        "core/signal-definition.json#/properties/taxonomy/properties/value_mappings/items",
    ),
    (
        "SignalOnboarder",
        "core/signal-definition.json#/properties/onboarder",
    ),
    (
        "SignalModeling",
        "core/signal-definition.json#/properties/modeling",
    ),
    (
        "SignalModelingSeedSource",
        "core/signal-definition.json#/properties/modeling/properties/seed_source",
    ),
    (
        "SignalModelingDisclosure",
        "core/signal-modeling-disclosure.json",
    ),
    (
        "SignalModelingDisclosureJurisdiction",
        "core/signal-modeling-disclosure.json#/properties/jurisdictions/items",
    ),
    (
        "SignalDataSubjectRights",
        "core/signal-definition.json#/properties/data_subject_rights",
    ),
    (
        "SignalDataSubjectRightsChannel",
        "core/signal-definition.json#/properties/data_subject_rights/properties/channels/items",
    ),
    (
        "DeliveryWindow",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/windows/items",
    ),
    (
        "DeliveryWindowPackage",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/windows/items/properties/by_package/items",
    ),
    (
        "ProductCardSpecification",
        "core/product.json#/properties/product_card_detailed/properties/specifications/items",
    ),
    (
        "GetProductsFilterDiagnostics",
        "media-buy/get-products-response.json#/properties/filter_diagnostics",
    ),
    (
        "GetProductsRefineItem",
        "media-buy/get-products-request.json#/properties/refine/items",
    ),
    (
        "GetProductsRefinementAppliedItem",
        "media-buy/get-products-response.json#/properties/refinement_applied/items",
    ),
    (
        "FilterExclusionDiagnostic",
        "media-buy/get-products-response.json"
        "#/properties/filter_diagnostics/properties/excluded_by/additionalProperties",
    ),
    (
        "CapabilitiesWholesaleFeedVersioning",
        "protocol/get-adcp-capabilities-response.json#/properties/wholesale_feed_versioning",
    ),
    (
        "CapabilitiesWholesaleFeedWebhooks",
        "protocol/get-adcp-capabilities-response.json#/properties/wholesale_feed_webhooks",
    ),
    (
        "GetSignalsIncompleteItem",
        "signals/get-signals-response.json#/properties/incomplete/items",
    ),
    (
        "GetSignalsResponseSignal",
        "signals/get-signals-response.json#/properties/signals/items",
    ),
    (
        "SignalCoverageForecastScope",
        "core/signal-coverage-forecast.json#/properties/scope",
    ),
    (
        "ArtifactWebhookArtifact",
        "content-standards/artifact-webhook-payload.json#/properties/artifacts/items",
    ),
    (
        "ArtifactMetadata",
        "content-standards/artifact.json#/properties/metadata",
    ),
    (
        "ArtifactIdentifiers",
        "content-standards/artifact.json#/properties/identifiers",
    ),
    (
        "AuditObservationDetails",
        "creative/audit-observation.json#/properties/details",
    ),
    (
        "AuditObservationClaimedValue",
        "creative/audit-observation.json#/properties/details/properties/claimed_value",
    ),
    (
        "UpstreamRecordedCall",
        "compliance/comply-test-controller-response.json#/oneOf/6/properties/recorded_calls/items",
    ),
    (
        "IdentifierMatchProof",
        "compliance/comply-test-controller-response.json"
        "#/oneOf/6/properties/recorded_calls/items/properties/identifier_match_proofs/items",
    ),
    (
        "KeywordTargetUpdate",
        "media-buy/package-update.json#/properties/keyword_targets_add/items",
    ),
    (
        "KeywordTargetRef",
        "media-buy/package-update.json#/properties/keyword_targets_remove/items",
    ),
    (
        "DeliveryAggregatedTotals",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/aggregated_totals",
    ),
    (
        "MediaBuyDeliveryTotals",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/totals",
    ),
    (
        "MediaBuyDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items",
    ),
    (
        "PackageDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items",
    ),
    (
        "ProductDeliveryMeasurement",
        "core/product.json#/properties/delivery_measurement",
    ),
    (
        "ProductCatalogMatch",
        "core/product.json#/properties/catalog_match",
    ),
    (
        "AccountCreditLimit",
        "core/account.json#/properties/credit_limit",
    ),
    (
        "AccountGovernanceAgent",
        "core/account.json#/properties/governance_agents/items",
    ),
    (
        "ReportingBucket",
        "core/account.json#/properties/reporting_bucket",
    ),
    (
        "GeoMetroTarget",
        "core/targeting.json#/properties/geo_metros/items",
    ),
    (
        "GeoProximityTarget",
        "core/targeting.json#/properties/geo_proximity/items",
    ),
    (
        "GeoProximityTravelTime",
        "core/targeting.json#/properties/geo_proximity/items/properties/travel_time",
    ),
    (
        "GeoProximityRadius",
        "core/targeting.json#/properties/geo_proximity/items/properties/radius",
    ),
    (
        "GeoProximityGeometry",
        "core/targeting.json#/properties/geo_proximity/items/properties/geometry",
    ),
    (
        "AgeRestriction",
        "core/targeting.json#/properties/age_restriction",
    ),
    (
        "KeywordTarget",
        "core/targeting.json#/properties/keyword_targets/items",
    ),
    (
        "NegativeKeywordTarget",
        "core/targeting.json#/properties/negative_keywords/items",
    ),
    (
        "BusinessAddress",
        "core/business-entity.json#/properties/address",
    ),
    (
        "BusinessContact",
        "core/business-entity.json#/properties/contacts/items",
    ),
    (
        "BankAccount",
        "core/business-entity.json#/properties/bank",
    ),
    (
        "LegacyWebhookAuthentication",
        "core/push-notification-config.json#/properties/authentication",
    ),
    (
        "PackageCancellation",
        "core/package.json#/properties/cancellation",
    ),
    (
        "CreativeFormatAccessibility",
        "core/format.json#/properties/accessibility",
    ),
    (
        "SignalRange",
        "core/signal-definition.json#/properties/range",
    ),
    (
        "DeliveryQuartileData",
        "core/delivery-metrics.json#/properties/quartile_data",
    ),
    (
        "MediaBuyBudget",
        "media-buy/create-media-buy-request.json#/properties/total_budget",
    ),
    (
        "IOAcceptance",
        "media-buy/create-media-buy-request.json#/properties/io_acceptance",
    ),
    (
        "ArtifactWebhookConfig",
        "media-buy/create-media-buy-request.json#/properties/artifact_webhook",
    ),
    (
        "SyncGovernanceAccountResult",
        "account/sync-governance-response.json#/oneOf/0/properties/accounts/items",
    ),
    (
        "SyncGovernanceAgentResult",
        "account/sync-governance-response.json#/oneOf/0/properties/accounts/items"
        "/properties/governance_agents/items",
    ),
    (
        "DeliveryAttributionWindow",
        "media-buy/get-media-buy-delivery-request.json#/properties/attribution_window",
    ),
    (
        "DeliveryReportingDimensions",
        "media-buy/get-media-buy-delivery-request.json#/properties/reporting_dimensions",
    ),
    (
        "DeliveryReportingGeoDimension",
        "media-buy/get-media-buy-delivery-request.json#/properties/reporting_dimensions/properties/geo",
    ),
    (
        "DeliveryReportingDimension",
        "media-buy/get-media-buy-delivery-request.json#/properties/reporting_dimensions/properties/device_type",
    ),
    (
        "GovernanceDeliveryMetrics",
        "governance/check-governance-request.json#/properties/delivery_metrics",
    ),
    (
        "GovernanceDeliveryReportingPeriod",
        "governance/check-governance-request.json#/properties/delivery_metrics"
        "/properties/reporting_period",
    ),
    (
        "GovernanceAudienceDistribution",
        "governance/check-governance-request.json#/properties/delivery_metrics"
        "/properties/audience_distribution",
    ),
    (
        "ReportPlanOutcomeDelivery",
        "governance/report-plan-outcome-request.json#/properties/delivery",
    ),
    (
        "ReportPlanOutcomeSellerResponse",
        "governance/report-plan-outcome-request.json#/properties/seller_response",
    ),
    (
        "ReportPlanOutcomeSellerPackage",
        "governance/report-plan-outcome-request.json#/properties/seller_response"
        "/properties/packages/items",
    ),
    (
        "ReportPlanOutcomeDeliveryReportingPeriod",
        "governance/report-plan-outcome-request.json#/properties/delivery"
        "/properties/reporting_period",
    ),
    (
        "ReportPlanOutcomeError",
        "governance/reported-outcome-error.json",
    ),
    (
        "ReportPlanOutcomePlanSummary",
        "governance/report-plan-outcome-response.json#/properties/plan_summary",
    ),
    (
        "PolicyExemplars",
        "governance/policy-entry.json#/properties/exemplars",
    ),
    (
        "PolicyExemplar",
        "governance/policy-entry.json#/$defs/exemplar",
    ),
    (
        "GetProductsIncompleteItem",
        "media-buy/get-products-response.json#/properties/incomplete/items",
    ),
    (
        "SyncPlansPlan",
        "governance/sync-plans-response.json#/properties/plans/items",
    ),
    (
        "SyncPlansPlanCategory",
        "governance/sync-plans-response.json#/properties/plans/items/properties/categories/items",
    ),
    (
        "SyncPlansResolvedPolicy",
        "governance/sync-plans-response.json#/properties/plans/items/properties/resolved_policies/items",
    ),
    (
        "CheckGovernanceFinding",
        "governance/check-governance-response.json#/properties/findings/items",
    ),
    (
        "ReportPlanOutcomeFinding",
        "governance/report-plan-outcome-response.json#/properties/findings/items",
    ),
    (
        "PlanAuditLog",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items",
    ),
    (
        "PlanAuditBudget",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/budget",
    ),
    (
        "PlanAuditChannelAllocation",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/channel_allocation/additionalProperties",
    ),
    (
        "PlanAuditSummary",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/summary",
    ),
    (
        "PlanAuditStatusCounts",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/summary/properties/statuses",
    ),
    (
        "PlanAuditEscalation",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/summary/properties/escalations/items",
    ),
    (
        "PlanAuditDriftMetrics",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/summary/properties/drift_metrics",
    ),
    (
        "PlanAuditDriftThresholds",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/summary/properties/drift_metrics/properties/thresholds",
    ),
    (
        "PlanAuditEntry",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/entries/items",
    ),
    (
        "PlanAuditFinding",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/entries/items/properties/findings/items",
    ),
    (
        "PlanAuditGovernedAction",
        "governance/get-plan-audit-logs-response.json#/properties/plans/items"
        "/properties/governed_actions/items",
    ),
    (
        "CreativeAgentRef",
        "media-buy/list-creative-formats-response.json#/properties/creative_agents/items",
    ),
    (
        "BuildCreativePreviewInput",
        "media-buy/build-creative-request.json#/properties/preview_inputs/items",
    ),
    (
        "BuildCreativeMaxSpend",
        "media-buy/build-creative-request.json#/properties/max_spend",
    ),
    (
        "BuildCreativeVariantAxis",
        "media-buy/build-creative-request.json#/properties/variant_axis",
    ),
    (
        "BuildCreativeSignalCondition",
        "media-buy/build-creative-request.json#/properties/signal_conditions/items",
    ),
    (
        "CollectionRequestPagination",
        "collection/get-collection-list-request.json#/properties/pagination",
    ),
    (
        "CollectionChangeSummary",
        "collection/collection-list-changed-webhook.json#/properties/change_summary",
    ),
    (
        "PropertyChangeSummary",
        "property/property-list-changed-webhook.json#/properties/change_summary",
    ),
    (
        "RightsAgentRef",
        "core/rights-constraint.json#/properties/rights_agent",
    ),
    (
        "ProductCard",
        "core/product.json#/properties/product_card",
    ),
    (
        "ProductCardDetailed",
        "core/product.json#/properties/product_card_detailed",
    ),
    (
        "CreativeFormatCard",
        "core/format.json#/properties/format_card",
    ),
    (
        "CreativeFormatCardDetailed",
        "core/format.json#/properties/format_card_detailed",
    ),
    (
        "ProvenanceAITool",
        "core/provenance.json#/properties/ai_tool",
    ),
    (
        "ProvenanceDeclaredBy",
        "core/provenance.json#/properties/declared_by",
    ),
    (
        "ProvenanceC2PA",
        "core/provenance.json#/properties/c2pa",
    ),
    (
        "ProvenanceDisclosure",
        "core/provenance.json#/properties/disclosure",
    ),
    (
        "ProvenanceDisclosureJurisdiction",
        "core/provenance.json#/properties/disclosure/properties/jurisdictions/items",
    ),
    (
        "ProvenanceDisclosureRenderGuidance",
        "core/provenance.json#/properties/disclosure/properties/jurisdictions/items"
        "/properties/render_guidance",
    ),
    (
        "ProvenanceVerification",
        "core/provenance.json#/properties/verification/items",
    ),
    (
        "ProposalBudgetGuidance",
        "core/proposal.json#/properties/total_budget_guidance",
    ),
    (
        "InsertionOrderTerms",
        "core/insertion-order.json#/properties/terms",
    ),
    (
        "InsertionOrderBudget",
        "core/insertion-order.json#/properties/terms/properties/total_budget",
    ),
    (
        "InstallmentDerivative",
        "core/installment.json#/properties/derivative_of",
    ),
    (
        "ProductMetricOptimization",
        "core/product.json#/properties/metric_optimization",
    ),
    (
        "ProductConversionTracking",
        "core/product.json#/properties/conversion_tracking",
    ),
    (
        "ProductTrustedMatch",
        "core/product.json#/properties/trusted_match",
    ),
    (
        "ProductTrustedMatchProvider",
        "core/product.json#/properties/trusted_match/properties/providers/items",
    ),
    (
        "ProductMaterialSubmission",
        "core/product.json#/properties/material_submission",
    ),
    (
        "ProductFilterBudgetRange",
        "core/product-filters.json#/properties/budget_range",
    ),
    (
        "ProductFilterMetro",
        "core/product-filters.json#/properties/metros/items",
    ),
    (
        "ProductFilterTrustedMatch",
        "core/product-filters.json#/properties/trusted_match",
    ),
    (
        "ProductFilterTrustedMatchProvider",
        "core/product-filters.json#/properties/trusted_match/properties/providers/items",
    ),
    (
        "ProductFilterGeoTargetingRequirement",
        "core/product-filters.json#/properties/required_geo_targeting/items",
    ),
    (
        "ProductFilterGeoProximity",
        "core/product-filters.json#/properties/geo_proximity/items",
    ),
    (
        "ProductFilterTravelTime",
        "core/product-filters.json#/properties/geo_proximity/items/properties/travel_time",
    ),
    (
        "ProductFilterRadius",
        "core/product-filters.json#/properties/geo_proximity/items/properties/radius",
    ),
    (
        "ProductFilterGeometry",
        "core/product-filters.json#/properties/geo_proximity/items/properties/geometry",
    ),
    (
        "ProductFilterKeyword",
        "core/product-filters.json#/properties/keywords/items",
    ),
    (
        "PriceAdjustment",
        "pricing-options/price-breakdown.json#/properties/adjustments/items",
    ),
    (
        "CreativeFormatDisclosureCapability",
        "core/format.json#/properties/disclosure_capabilities/items",
    ),
    (
        "CreativeAssetInput",
        "core/creative-asset.json#/properties/inputs/items",
    ),
    (
        "TargetingStoreCatchment",
        "core/targeting.json#/properties/store_catchments/items",
    ),
    (
        "DeliveryEventTypeMetrics",
        "core/delivery-metrics.json#/properties/by_event_type/items",
    ),
    (
        "DeliveryDOOHMetrics",
        "core/delivery-metrics.json#/properties/dooh_metrics",
    ),
    (
        "DeliveryDOOHVenueBreakdown",
        "core/delivery-metrics.json#/properties/dooh_metrics/properties/venue_breakdown/items",
    ),
    (
        "DeliveryViewability",
        "core/delivery-metrics.json#/properties/viewability",
    ),
    (
        "DeliveryActionSourceMetrics",
        "core/delivery-metrics.json#/properties/by_action_source/items",
    ),
    (
        "MediaBuyDailyBreakdown",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/daily_breakdown/items",
    ),
    (
        "PackageCatalogItemDelivery",
        "core/catalog-item-delivery-metrics.json",
    ),
    (
        "PackageCreativeDelivery",
        "core/creative-delivery-metrics.json",
    ),
    (
        "PackageKeywordDelivery",
        "core/keyword-delivery-metrics.json",
    ),
    (
        "PackageGeoDelivery",
        "core/geo-delivery-metrics.json",
    ),
    (
        "PackageDeviceTypeDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_device_type/items",
    ),
    (
        "PackageDevicePlatformDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_device_platform/items",
    ),
    (
        "PackageAudienceDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_audience/items",
    ),
    (
        "PackagePlacementDelivery",
        "core/placement-delivery-metrics.json",
    ),
    (
        "PackageDailyBreakdown",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/daily_breakdown/items",
    ),
    (
        "OptimizationGoalTargetFrequency",
        "core/optimization-goal.json#/oneOf/0/properties/target_frequency",
    ),
    (
        "OptimizationGoalEventSource",
        "core/optimization-goal.json#/oneOf/1/properties/event_sources/items",
    ),
    (
        "ListCreativesSort",
        "creative/list-creatives-request.json#/properties/sort",
    ),
    (
        "PreviewCreativeInput",
        "creative/preview-creative-request.json#/properties/inputs/items",
    ),
    (
        "PreviewCreativeBatchRequest",
        "creative/preview-creative-request.json#/properties/requests/items",
    ),
    (
        "ForcedDirective",
        "compliance/comply-test-controller-response.json#/oneOf/3/properties/forced",
    ),
    (
        "ArtifactWebhookPagination",
        "content-standards/artifact-webhook-payload.json#/properties/pagination",
    ),
    (
        "PlannedDeliveryGeo",
        "core/planned-delivery.json#/properties/geo",
    ),
    (
        "CreativeBriefMessaging",
        "core/creative-brief.json#/properties/messaging",
    ),
    (
        "CreativeBriefCompliance",
        "core/creative-brief.json#/properties/compliance",
    ),
    (
        "CreativeBriefDisclosure",
        "core/creative-brief.json#/properties/compliance"
        "/properties/required_disclosures/items",
    ),
    (
        "EventContentItem",
        "core/event-custom-data.json#/properties/contents/items",
    ),
    (
        "PolicyRegulatoryFramework",
        "governance/policy-category-definition.json"
        "#/properties/regulatory_frameworks/items",
    ),
    (
        "UserMatchUID",
        "core/user-match.json#/properties/uids/items",
    ),
])

INLINE_SCHEMA_DESCRIPTIONS = {
    'GetSignalsResponseSignal': (
        'GetSignals response row with listing identity, enrichment, activation, and pricing fields.'
    ),
    'SignalCoverageForecastScope': 'Explicit denominator for the coverage forecast.',
}

FIELD_DESCRIPTION_OVERRIDES = {
    ('GetAdcpCapabilitiesResponse', 'supported_protocols'): 'AdCP protocols this agent supports.',
}

ENUM_DESCRIPTION_OVERRIDES = {
    'ProposalStatus': 'Lifecycle status of a proposal.',
}

FIELD_DESCRIPTION_FALLBACK_SPECS = {
    ('GetSignalsResponseSignal', name): f'core/signal-definition.json#/properties/{name}'
    for name in (
        'segmentation_criteria',
        'criteria_url',
        'data_sources',
        'methodology',
        'audience_expansion',
        'device_expansion',
        'refresh_cadence',
        'lookback_window',
        'onboarder',
        'countries',
        'consent_basis',
        'art9_basis',
        'modeling',
        'dts_compliant_version',
    )
}

# Inline object helpers generated as plain structs drop unknown properties on
# unmarshal/remarshal. Closed registrations are only safe while the schema keeps
# `additionalProperties: false`; open registrations are explicit so a new inline
# helper cannot silently default to data-dropping open-object semantics.
CLOSED_INLINE_SCHEMA_TYPES = frozenset({
    'RefinementConstraints',
    'CPMConstraint',
    'ImpressionsConstraint',
    'FlightConstraint',
    'AlternativesRequest',
    'RefinementResult',
    'ReportPlanOutcomeDelivery',
    'MetricQualifier',
    'RequestedCommittedMetric',
    'ReportingVendorMetric',
    'RequiredVendorMetric',
    'CreativeAcceptedVerifier',
    'ProvenanceVerifyAgent',
    'SignalTaxonomy',
    'SignalTaxonomyValue',
    'SignalTaxonomyValueMapping',
    'SignalOnboarder',
    'SignalModeling',
    'SignalModelingSeedSource',
    'SignalModelingDisclosure',
    'SignalModelingDisclosureJurisdiction',
    'SignalDataSubjectRights',
    'SignalDataSubjectRightsChannel',
    'FilterExclusionDiagnostic',
    'GetSignalsIncompleteItem',
    'AuditObservationDetails',
    'AuditObservationClaimedValue',
    'UpstreamRecordedCall',
    'IdentifierMatchProof',
    'KeywordTargetUpdate',
    'KeywordTargetRef',
    'AccountGovernanceAgent',
    'ReportingBucket',
    'GeoMetroTarget',
    'GeoProximityTravelTime',
    'GeoProximityRadius',
    'GeoProximityGeometry',
    'AgeRestriction',
    'KeywordTarget',
    'NegativeKeywordTarget',
    'BusinessAddress',
    'BusinessContact',
    'BankAccount',
    'LegacyWebhookAuthentication',
    'PackageCancellation',
    'SignalRange',
    'MediaBuyBudget',
    'GovernanceDeliveryMetrics',
    'GovernanceDeliveryReportingPeriod',
    'GovernanceAudienceDistribution',
    'ReportPlanOutcomeDeliveryReportingPeriod',
    'ReportPlanOutcomePlanSummary',
    'PolicyExemplars',
    'PolicyExemplar',
    'GetProductsIncompleteItem',
    'SyncPlansPlan',
    'SyncPlansPlanCategory',
    'SyncPlansResolvedPolicy',
    'CheckGovernanceFinding',
    'ReportPlanOutcomeFinding',
    'PlanAuditLog',
    'PlanAuditBudget',
    'PlanAuditChannelAllocation',
    'PlanAuditSummary',
    'PlanAuditStatusCounts',
    'PlanAuditEscalation',
    'PlanAuditDriftMetrics',
    'PlanAuditDriftThresholds',
    'PlanAuditEntry',
    'PlanAuditFinding',
    'PlanAuditGovernedAction',
    'CollectionRequestPagination',
    'CollectionChangeSummary',
    'PropertyChangeSummary',
    'GetProductsRefineItem',
    'GetProductsRefinementAppliedItem',
    'SyncGovernanceAgentResult',
    'ForcedDirective',
    'InstallmentDerivative',
    'ProductFilterMetro',
    'ProductFilterTrustedMatch',
    'ProductFilterGeoTargetingRequirement',
    'ProductFilterTravelTime',
    'ProductFilterRadius',
    'ProductFilterGeometry',
    'ProductFilterKeyword',
    'PolicyRegulatoryFramework',
})

OPEN_INLINE_SCHEMA_TYPES = frozenset({
    'RefinementCapability',
    'ReportPlanOutcomeError',
    'ForecastPointDimension',
    'ReachWindow',
    'ForecastViewability',
    'CreativeProvenanceRequirements',
    'ProvenanceEmbeddedProvenance',
    'ProvenanceWatermark',
    'DeliveryWindow',
    'DeliveryWindowPackage',
    'ProductCardSpecification',
    'GetProductsFilterDiagnostics',
    'CapabilitiesWholesaleFeedVersioning',
    'CapabilitiesWholesaleFeedWebhooks',
    'GetSignalsResponseSignal',
    'SignalCoverageForecastScope',
    'DeliveryAggregatedTotals',
    'MediaBuyDeliveryTotals',
    'MediaBuyDelivery',
    'PackageDelivery',
    'ProductDeliveryMeasurement',
    'ProductCatalogMatch',
    'SyncGovernanceAccountResult',
    'AccountCreditLimit',
    'GeoProximityTarget',
    'CreativeFormatAccessibility',
    'DeliveryQuartileData',
    'IOAcceptance',
    'ArtifactWebhookConfig',
    'PreviewCreativeInput',
    'PreviewCreativeBatchRequest',
    'DeliveryAttributionWindow',
    'DeliveryReportingDimensions',
    'DeliveryReportingGeoDimension',
    'DeliveryReportingDimension',
    'ReportPlanOutcomeSellerResponse',
    'ReportPlanOutcomeSellerPackage',
    'CreativeAgentRef',
    'BuildCreativePreviewInput',
    'BuildCreativeMaxSpend',
    'BuildCreativeVariantAxis',
    'BuildCreativeSignalCondition',
    'RightsAgentRef',
    'ProductCard',
    'ProductCardDetailed',
    'CreativeFormatCard',
    'CreativeFormatCardDetailed',
    'ProvenanceAITool',
    'ProvenanceDeclaredBy',
    'ProvenanceC2PA',
    'ProvenanceDisclosure',
    'ProvenanceDisclosureJurisdiction',
    'ProvenanceDisclosureRenderGuidance',
    'ProvenanceVerification',
    'ProposalBudgetGuidance',
    'InsertionOrderTerms',
    'InsertionOrderBudget',
    'ProductMetricOptimization',
    'ProductConversionTracking',
    'ProductTrustedMatch',
    'ProductTrustedMatchProvider',
    'ProductMaterialSubmission',
    'ProductFilterBudgetRange',
    'ProductFilterTrustedMatchProvider',
    'ProductFilterGeoProximity',
    'PriceAdjustment',
    'CreativeFormatDisclosureCapability',
    'CreativeAssetInput',
    'TargetingStoreCatchment',
    'DeliveryEventTypeMetrics',
    'DeliveryDOOHMetrics',
    'DeliveryDOOHVenueBreakdown',
    'DeliveryViewability',
    'DeliveryActionSourceMetrics',
    'MediaBuyDailyBreakdown',
    'PackageCatalogItemDelivery',
    'PackageCreativeDelivery',
    'PackageKeywordDelivery',
    'PackageGeoDelivery',
    'PackageDeviceTypeDelivery',
    'PackageDevicePlatformDelivery',
    'PackageAudienceDelivery',
    'PackagePlacementDelivery',
    'PackageDailyBreakdown',
    'OptimizationGoalTargetFrequency',
    'OptimizationGoalEventSource',
    'ListCreativesSort',
    'ArtifactWebhookPagination',
    'ArtifactWebhookArtifact',
    'ArtifactMetadata',
    'ArtifactIdentifiers',
    'PlannedDeliveryGeo',
    'CreativeBriefMessaging',
    'CreativeBriefCompliance',
    'CreativeBriefDisclosure',
    'EventContentItem',
    'UserMatchUID',
})

# Shared inline helper types are generated from one schema pointer but reused by
# INLINE_TYPE_HINTS for sibling schema pointers. lint.py verifies these siblings
# keep the same property set before we keep reusing a single Go type.
SHARED_INLINE_OVERRIDES = {
    "MetricQualifier": [
        "core/delivery-metric-aggregate.json#/oneOf/0/properties/qualifier",
        "core/missing-metric.json#/oneOf/0/properties/qualifier",
    ],
    "DeliveryReportingDimension": [
        "media-buy/get-media-buy-delivery-request.json"
        "#/properties/reporting_dimensions/properties/device_type",
        "media-buy/get-media-buy-delivery-request.json"
        "#/properties/reporting_dimensions/properties/device_platform",
        "media-buy/get-media-buy-delivery-request.json"
        "#/properties/reporting_dimensions/properties/audience",
        "media-buy/get-media-buy-delivery-request.json"
        "#/properties/reporting_dimensions/properties/placement",
    ],
    "PreviewCreativeInput": [
        "creative/preview-creative-request.json#/properties/inputs/items",
        "creative/preview-creative-request.json"
        "#/properties/requests/items/properties/inputs/items",
    ],
}

# Inline hints that deliberately reuse an existing generated type for a sibling
# inline schema. lint.py verifies the source inline schema and target type keep
# the same field and requiredness shape.
CROSS_TYPE_INLINE_HINTS = {
    ('PerformanceFeedback', 'measurement_period'): 'DatetimeRange',
}

# Named enum helpers generated from inline JSON Schema pointers. These cover
# important SDK validation values that are not standalone enum schema files.
INLINE_ENUM_TYPES = OrderedDict([
    (
        "OptimizationMetric",
        {
            "schema": "core/optimization-goal.json",
            "one_of_kind": "metric",
            "property": "metric",
        },
    ),
])

# Named helper types generated for simple union schemas. These cover the common
# protocol shape "single scalar or array of the same scalar" without falling
# back to `any`.
UNION_SCHEMA_TYPES = OrderedDict([
    (
        "MediaBuyStatusFilter",
        (
            "media-buy/get-media-buys-request.json#/properties/status_filter",
            "media-buy/get-media-buy-delivery-request.json#/properties/status_filter",
        ),
    ),
])

# Hand-written types that should be drift-checked against a schema path or JSON
# pointer. lint.py imports this table; keep it here so generator/lint ownership
# lives in one place.
HAND_WRITTEN_SCHEMA_SPECS = {
    'Product': 'core/product.json',
    'Package': 'core/package.json',
    'CreativeAssignment': 'core/creative-assignment.json',
    'Targeting': 'core/targeting.json',
    'FormatRef': 'core/format-id.json',
    'PricingOption': 'core/pricing-option.json',
    'Signal': 'core/signal-definition.json',
    'SignalPricing': 'core/signal-pricing.json',
    'Deployment': 'core/deployment.json',
    'CreativeFormat': 'core/format.json',
    'VendorPricingOption': 'core/vendor-pricing-option.json',
    'MeasurementTerms': 'core/measurement-terms.json',
    'MeasurementWindow': 'core/measurement-window.json',
    'PerformanceStandard': 'core/performance-standard.json',
    'Duration': 'core/duration.json',
    'CancellationPolicy': 'core/cancellation-policy.json',
    'CollectionListRef': 'core/collection-list-ref.json',
    'CreativeConsumption': 'core/creative-consumption.json',
    'IndustryIdentifier': 'core/industry-identifier.json',
    'ContentRating': 'core/content-rating.json',
    'AttributionWindow': 'core/attribution-window.json',
    'CapabilitiesData': 'protocol/get-adcp-capabilities-response.json',
    'ADCPVersion': 'protocol/get-adcp-capabilities-response.json#/properties/adcp',
    'IdempotencyCaps': 'protocol/get-adcp-capabilities-response.json#/properties/adcp/properties/idempotency',
    'AccountCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/account',
    'MediaBuyCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy',
    'MediaBuyExecution': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution',
    'TrustedMatchCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/trusted_match',
    'CreativeSpecsCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/creative_specs',
    'TargetingCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/targeting',
    'GeoMetrosCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/targeting/properties/geo_metros',
    'GeoPostalAreasCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/targeting/properties/geo_postal_areas',
    'GeoProximityCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/targeting/properties/geo_proximity',
    'AgeRestrictionCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/targeting/properties/age_restriction',
    'KeywordMatchCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/execution/properties/targeting/properties/keyword_targets',
    'AudienceTargetingCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/audience_targeting',
    'MatchingLatencyRange': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/audience_targeting/properties/matching_latency_hours',
    'ConversionTrackingCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/conversion_tracking',
    'AttributionWindowOption': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/conversion_tracking/properties/attribution_windows/items',
    'ContentStandardsCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/content_standards',
    'PortfolioCaps': 'protocol/get-adcp-capabilities-response.json#/properties/media_buy/properties/portfolio',
    'SignalsCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/signals',
    'GovernanceCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/governance',
    'GovernanceFeature': 'protocol/get-adcp-capabilities-response.json#/properties/governance/properties/property_features/items',
    'FeatureRange': 'protocol/get-adcp-capabilities-response.json#/properties/governance/properties/property_features/items/properties/range',
    'SICapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/sponsored_intelligence',
    'SIEndpoint': 'protocol/get-adcp-capabilities-response.json#/properties/sponsored_intelligence/properties/endpoint',
    'SITransport': 'protocol/get-adcp-capabilities-response.json#/properties/sponsored_intelligence/properties/endpoint/properties/transports/items',
    'BrandCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/brand',
    'CreativeCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/creative',
    'RequestSigningCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/request_signing',
    'WebhookSigningCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/webhook_signing',
    'IdentityCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/identity',
    'IdentityKeyOrigins': 'protocol/get-adcp-capabilities-response.json#/properties/identity/properties/key_origins',
    'IdentityCompromiseNotification': 'protocol/get-adcp-capabilities-response.json#/properties/identity/properties/compromise_notification',
    'ComplianceTestingCapabilities': 'protocol/get-adcp-capabilities-response.json#/properties/compliance_testing',
    'OptimizationGoalCostPerTarget': 'core/optimization-goal.json#/oneOf/0/properties/target/oneOf/0',
    'OptimizationGoalThresholdRateTarget': 'core/optimization-goal.json#/oneOf/0/properties/target/oneOf/1',
    'OptimizationGoalPerAdSpendTarget': 'core/optimization-goal.json#/oneOf/1/properties/target/oneOf/1',
    'OptimizationGoalMaximizeValueTarget': 'core/optimization-goal.json#/oneOf/1/properties/target/oneOf/2',
}

# Hand-written inline types that do not have standalone schema files but should
# still be structurally drift-checked against their owning schema pointers.
HAND_WRITTEN_INLINE_SCHEMA_SPECS = {
    'AccountSetup': [
        'core/account.json#/properties/setup',
        'account/sync-accounts-response.json#/oneOf/0/properties/accounts/items/properties/setup',
        'bundled/creative/list-creatives-response.json#/properties/creatives/items/properties/account/properties/setup',
        'bundled/creative/sync-creatives-response.json#/oneOf/0/properties/creatives/items/properties/account/properties/setup',
        'bundled/media-buy/create-media-buy-response.json#/oneOf/0/properties/account/properties/setup',
        'bundled/media-buy/get-media-buys-response.json#/properties/media_buys/items/properties/account/properties/setup',
    ],
    'CheckGovernanceCondition': [
        'governance/check-governance-response.json#/properties/conditions/items',
    ],
}

# Map schema-derived Go names to the preferred Go name. Applied both when
# resolving $ref targets and when emitting core/tool schema structs, so a
# schema named brand-ref.json emits as `type BrandReference struct` and every
# reference to it uses that same alias.
# pascal_case('format-id') produces 'FormatID' today (acronym-aware). Only the
# current casing needs mapping here; if pascal_case output changes, update both.
REF_ALIASES = {
    'BrandRef': 'BrandReference',
    'AccountRef': 'AccountReference',
    'PackageRequest': 'PackageInput',
    'FormatID': 'FormatRef',
    'MediaBuy': 'MediaBuyData',
    'Format': 'CreativeFormat',
    'SignalDefinition': 'Signal',
    'SignalPricingOption': 'SignalPricing',
    'ProposalBudgetConstraint': 'BudgetConstraint',
    'RefineProposalsResponse': 'RefineProposalsData',
    'DeliveryMetrics': 'DeliveryTotals',
    'StartTiming': 'string',  # start_time is a string or "asap"
    'AccountInput': 'AccountInput',
    'GovernanceAccountInput': 'GovernanceAccountInput',
    'CreativeInput': 'CreativeInput',
    'DestinationInput': 'DestinationInput',
}

# Inline array/item type hints: when a schema has an inline object or oneOf with
# no named $ref, map (struct_name, field) -> Go type. Non-array hints may include
# a leading `*` for optional inline object fields; array fields wrap the hinted
# item type as `[]{hint}`, so pointer hints on arrays produce `[]*T`.
INLINE_TYPE_HINTS = {
    ('SyncAccountsRequest', 'accounts'): 'AccountInput',
    ('SyncGovernanceRequest', 'accounts'): 'GovernanceAccountInput',
    ('SyncCreativesRequest', 'creatives'): 'CreativeInput',
    ('SyncCreativesRequest', 'assignments'): 'SyncCreativeAssignment',
    ('ListCreativesRequest', 'filters'): 'CreativeFilters',
    ('SyncCatalogsRequest', 'catalogs'): 'CatalogInput',
    ('SyncEventSourcesRequest', 'event_sources'): 'EventSourceInput',
    ('LogEventRequest', 'events'): 'map[string]any',
    ('ActivateSignalRequest', 'destinations'): 'DestinationInput',
    ('GetSignalsRequest', 'filters'): 'SignalFilters',
    ('GetProductsRequest', 'filters'): '*ProductFilters',
    ('ListCreativesRequest', 'sort'): '*ListCreativesSort',
    ('PreviewCreativeRequest', 'inputs'): 'PreviewCreativeInput',
    ('PreviewCreativeRequest', 'requests'): 'PreviewCreativeBatchRequest',
    ('PreviewCreativeBatchRequest', 'inputs'): 'PreviewCreativeInput',
    ('ForcedDirectiveSuccess', 'forced'): 'ForcedDirective',
    ('SyncPlansRequest', 'plans'): 'Plan',
    ('GetProductsResponse', 'proposals'): 'Proposal',
    ('RefineProposalsRequest', 'adcp_major_version'): '*int',
    ('RefineProposalsRequest', 'refinements'): 'ProposalRefinement',
    ('ProposalRefinement', 'constraints'): '*RefinementConstraints',
    ('ProposalRefinement', 'criteria'): '*ProductDiscoveryCriteria',
    ('ProposalRefinement', 'product_changes'): 'map[string]string',
    ('ProposalRefinement', 'alternatives'): '*AlternativesRequest',
    ('RefinementConstraints', 'total_budget'): '*BudgetConstraint',
    ('BudgetConstraint', 'min'): '*float64',
    ('BudgetConstraint', 'max'): '*float64',
    ('RefinementConstraints', 'cpm'): '*CPMConstraint',
    ('RefinementConstraints', 'impressions'): '*ImpressionsConstraint',
    ('RefinementConstraints', 'flight'): '*FlightConstraint',
    ('RefineProposalsData', 'results'): 'RefinementResult',
    ('RefineProposalsData', 'products'): 'CanonicalProduct',
    ('RefinementResult', 'proposal'): '*CanonicalProposal',
    ('RefinementResult', 'proposals'): 'CanonicalProposal',
    ('RefinementResult', 'unsatisfied_product_changes'): 'map[string]string',
    ('PackageUpdate', 'keyword_targets_add'): 'KeywordTargetUpdate',
    ('PackageUpdate', 'keyword_targets_remove'): 'KeywordTargetRef',
    ('PackageUpdate', 'negative_keywords_add'): 'KeywordTargetRef',
    ('PackageUpdate', 'negative_keywords_remove'): 'KeywordTargetRef',
    ('MediaBuyDelivery', 'totals'): 'MediaBuyDeliveryTotals',
    ('MediaBuyDelivery', 'by_package'): 'PackageDelivery',
    ('GetMediaBuyDeliveryResponse', 'reporting_period'): 'ReportingPeriod',
    ('GetMediaBuyDeliveryResponse', 'aggregated_totals'): '*DeliveryAggregatedTotals',
    ('GetMediaBuyDeliveryResponse', 'media_buy_deliveries'): 'MediaBuyDelivery',
    ('Product', 'delivery_measurement'): '*ProductDeliveryMeasurement',
    ('Product', 'catalog_match'): '*ProductCatalogMatch',
    ('Product', 'signal_targeting_options'): 'ProductSignalTargetingOption',
    ('Account', 'credit_limit'): '*AccountCreditLimit',
    ('Account', 'setup'): '*AccountSetup',
    ('Account', 'governance_agents'): 'AccountGovernanceAgent',
    ('Account', 'reporting_bucket'): '*ReportingBucket',
    ('Targeting', 'geo_metros'): 'GeoMetroTarget',
    ('Targeting', 'geo_metros_exclude'): 'GeoMetroTarget',
    ('Targeting', 'geo_postal_areas'): 'PostalArea',
    ('Targeting', 'geo_postal_areas_exclude'): 'PostalArea',
    ('Targeting', 'geo_proximity'): 'GeoProximityTarget',
    ('GeoProximityTarget', 'lat'): '*float64',
    ('GeoProximityTarget', 'lng'): '*float64',
    ('GeoProximityTarget', 'travel_time'): '*GeoProximityTravelTime',
    ('GeoProximityTarget', 'radius'): '*GeoProximityRadius',
    ('GeoProximityTarget', 'geometry'): '*GeoProximityGeometry',
    ('Targeting', 'age_restriction'): '*AgeRestriction',
    ('Targeting', 'keyword_targets'): 'KeywordTarget',
    ('Targeting', 'negative_keywords'): 'NegativeKeywordTarget',
    ('BusinessEntity', 'address'): '*BusinessAddress',
    ('BusinessEntity', 'contacts'): 'BusinessContact',
    ('BusinessEntity', 'bank'): '*BankAccount',
    ('PushNotificationConfig', 'authentication'): '*LegacyWebhookAuthentication',
    ('ReportingWebhook', 'authentication'): 'LegacyWebhookAuthentication',
    ('Package', 'cancellation'): '*PackageCancellation',
    ('Package', 'committed_metrics'): 'CommittedMetric',
    ('CreativeFormat', 'accessibility'): '*CreativeFormatAccessibility',
    ('CreativeFormat', 'supported_macros'): 'string',
    ('Signal', 'range'): '*SignalRange',
    # 3.1 forward-compatible hints for auto-discovered referenced schemas.
    ('SignalListing', 'range'): '*SignalRange',
    ('GetSignalsResponse', 'signals'): 'GetSignalsResponseSignal',
    ('GetSignalsResponseSignal', 'range'): '*SignalRange',
    ('GetSignalsResponseSignal', 'taxonomy'): '*SignalTaxonomy',
    ('GetSignalsResponseSignal', 'onboarder'): '*SignalOnboarder',
    ('GetSignalsResponseSignal', 'modeling'): '*SignalModeling',
    ('GetSignalsResponseSignal', 'data_subject_rights'): '*SignalDataSubjectRights',
    ('GetSignalsResponseSignal', 'coverage_forecast'): '*SignalCoverageForecast',
    ('SignalCoverageForecast', 'points'): 'ForecastPoint',
    ('SignalCoverageForecast', 'scope'): 'SignalCoverageForecastScope',
    ('ProductSignalTargetingOption', 'range'): '*SignalRange',
    ('PackageSignalTargeting', 'min_value'): '*float64',
    ('PackageSignalTargeting', 'max_value'): '*float64',
    ('SignalTargetingExpression', 'min_value'): '*float64',
    ('SignalTargetingExpression', 'max_value'): '*float64',
    ('BuildCreativeSignalCondition', 'min_value'): '*float64',
    ('BuildCreativeSignalCondition', 'max_value'): '*float64',
    ('SignalTargetingRules', 'min_selected_signals'): '*int',
    ('CommittedMetric', 'qualifier'): '*MetricQualifier',
    ('DeliveryMetricAggregate', 'qualifier'): '*MetricQualifier',
    ('MissingMetric', 'qualifier'): '*MetricQualifier',
    ('NotificationConfig', 'authentication'): '*LegacyWebhookAuthentication',
    ('DeliveryTotals', 'quartile_data'): '*DeliveryQuartileData',
    ('DeliveryTotals', 'reach_window'): '*ReachWindow',
    ('PerformanceFeedback', 'measurement_period'): 'DatetimeRange',
    ('PlannedDelivery', 'geo'): '*PlannedDeliveryGeo',
    ('CreativeBrief', 'messaging'): '*CreativeBriefMessaging',
    ('CreativeBrief', 'compliance'): '*CreativeBriefCompliance',
    ('CreativeBriefCompliance', 'required_disclosures'): 'CreativeBriefDisclosure',
    ('EventCustomData', 'contents'): 'EventContentItem',
    ('PolicyCategoryDefinition', 'regulatory_frameworks'): 'PolicyRegulatoryFramework',
    ('UserMatch', 'uids'): 'UserMatchUID',
    ('MediaBuyDeliveryTotals', 'quartile_data'): '*DeliveryQuartileData',
    ('MediaBuyDeliveryTotals', 'reach_window'): '*ReachWindow',
    ('PackageDelivery', 'quartile_data'): '*DeliveryQuartileData',
    ('PackageDelivery', 'reach_window'): '*ReachWindow',
    ('PackageDelivery', 'missing_metrics'): 'MissingMetric',
    ('CreateMediaBuyRequest', 'total_budget'): '*MediaBuyBudget',
    ('CreateMediaBuyRequest', 'io_acceptance'): '*IOAcceptance',
    ('CreateMediaBuyRequest', 'artifact_webhook'): '*ArtifactWebhookConfig',
    ('ArtifactWebhookConfig', 'authentication'): 'LegacyWebhookAuthentication',
    ('ArtifactWebhookConfig', 'sampling_rate'): '*float64',
    ('GetMediaBuyDeliveryRequest', 'attribution_window'): '*DeliveryAttributionWindow',
    ('GetMediaBuyDeliveryRequest', 'reporting_dimensions'): '*DeliveryReportingDimensions',
    ('DeliveryAttributionWindow', 'post_click'): '*Duration',
    ('DeliveryAttributionWindow', 'post_view'): '*Duration',
    ('DeliveryReportingDimensions', 'geo'): '*DeliveryReportingGeoDimension',
    ('DeliveryReportingDimensions', 'device_type'): '*DeliveryReportingDimension',
    ('DeliveryReportingDimensions', 'device_platform'): '*DeliveryReportingDimension',
    ('DeliveryReportingDimensions', 'audience'): '*DeliveryReportingDimension',
    ('DeliveryReportingDimensions', 'placement'): '*DeliveryReportingDimension',
    ('DeliveryReportingGeoDimension', 'system'): 'string',
    ('CheckGovernanceRequest', 'delivery_metrics'): '*GovernanceDeliveryMetrics',
    ('GovernanceDeliveryMetrics', 'reporting_period'): 'GovernanceDeliveryReportingPeriod',
    ('GovernanceDeliveryMetrics', 'spend'): '*float64',
    ('GovernanceDeliveryMetrics', 'cumulative_spend'): '*float64',
    ('GovernanceDeliveryMetrics', 'impressions'): '*int',
    ('GovernanceDeliveryMetrics', 'cumulative_impressions'): '*int',
    ('GovernanceDeliveryMetrics', 'audience_distribution'): '*GovernanceAudienceDistribution',
    ('ReportPlanOutcomeRequest', 'seller_response'): '*ReportPlanOutcomeSellerResponse',
    ('ReportPlanOutcomeSellerResponse', 'committed_budget'): '*float64',
    ('ReportPlanOutcomeSellerResponse', 'packages'): 'ReportPlanOutcomeSellerPackage',
    ('ReportPlanOutcomeSellerPackage', 'budget'): '*float64',
    ('ReportPlanOutcomeRequest', 'delivery'): '*ReportPlanOutcomeDelivery',
    ('ReportPlanOutcomeDelivery', 'reporting_period'): '*ReportPlanOutcomeDeliveryReportingPeriod',
    ('ReportPlanOutcomeDelivery', 'impressions'): '*int',
    ('ReportPlanOutcomeDelivery', 'spend'): '*float64',
    ('ReportPlanOutcomeDelivery', 'cpm'): '*float64',
    ('ReportPlanOutcomeDelivery', 'viewability_rate'): '*float64',
    ('ReportPlanOutcomeDelivery', 'completion_rate'): '*float64',
    ('ReportPlanOutcomeRequest', 'error'): '*ReportPlanOutcomeError',
    ('ReportPlanOutcomeResponse', 'plan_summary'): '*ReportPlanOutcomePlanSummary',
    ('ReportPlanOutcomePlanSummary', 'total_committed'): '*float64',
    ('ReportPlanOutcomePlanSummary', 'budget_remaining'): '*float64',
    ('PolicyEntry', 'exemplars'): '*PolicyExemplars',
    ('PolicyExemplars', 'pass'): 'PolicyExemplar',
    ('PolicyExemplars', 'fail'): 'PolicyExemplar',
    ('GetProductsResponse', 'incomplete'): 'GetProductsIncompleteItem',
    ('GetProductsIncompleteItem', 'estimated_wait'): '*Duration',
    ('SyncPlansResponse', 'plans'): 'SyncPlansPlan',
    ('SyncPlansPlan', 'categories'): 'SyncPlansPlanCategory',
    ('SyncPlansPlan', 'resolved_policies'): 'SyncPlansResolvedPolicy',
    ('SyncPlansResolvedPolicy', 'enforcement'): 'PolicyEnforcement',
    ('CheckGovernanceResponse', 'findings'): 'CheckGovernanceFinding',
    ('CheckGovernanceFinding', 'severity'): 'EscalationSeverity',
    ('CheckGovernanceFinding', 'confidence'): '*float64',
    ('ReportPlanOutcomeResponse', 'findings'): 'ReportPlanOutcomeFinding',
    ('ReportPlanOutcomeFinding', 'severity'): 'EscalationSeverity',
    ('CheckGovernanceResponse', 'conditions'): 'CheckGovernanceCondition',
    ('GetPlanAuditLogsResponse', 'plans'): 'PlanAuditLog',
    ('PlanAuditLog', 'budget'): 'PlanAuditBudget',
    ('PlanAuditLog', 'channel_allocation'): 'map[string]PlanAuditChannelAllocation',
    ('PlanAuditLog', 'summary'): 'PlanAuditSummary',
    ('PlanAuditLog', 'entries'): 'PlanAuditEntry',
    ('PlanAuditLog', 'governed_actions'): 'PlanAuditGovernedAction',
    ('PlanAuditBudget', 'authorized'): '*float64',
    ('PlanAuditBudget', 'committed'): '*float64',
    ('PlanAuditBudget', 'remaining'): '*float64',
    ('PlanAuditBudget', 'utilization_pct'): '*float64',
    ('PlanAuditChannelAllocation', 'committed'): '*float64',
    ('PlanAuditChannelAllocation', 'pct'): '*float64',
    ('PlanAuditSummary', 'checks_performed'): '*int',
    ('PlanAuditSummary', 'outcomes_reported'): '*int',
    ('PlanAuditSummary', 'statuses'): '*PlanAuditStatusCounts',
    ('PlanAuditSummary', 'findings_count'): '*int',
    ('PlanAuditSummary', 'escalations'): 'PlanAuditEscalation',
    ('PlanAuditSummary', 'drift_metrics'): '*PlanAuditDriftMetrics',
    ('PlanAuditStatusCounts', 'approved'): '*int',
    ('PlanAuditStatusCounts', 'denied'): '*int',
    ('PlanAuditStatusCounts', 'conditions'): '*int',
    ('PlanAuditStatusCounts', 'human_reviewed'): '*int',
    ('PlanAuditDriftMetrics', 'escalation_rate'): '*float64',
    ('PlanAuditDriftMetrics', 'auto_approval_rate'): '*float64',
    ('PlanAuditDriftMetrics', 'human_override_rate'): '*float64',
    ('PlanAuditDriftMetrics', 'mean_confidence'): '*float64',
    ('PlanAuditDriftMetrics', 'thresholds'): '*PlanAuditDriftThresholds',
    ('PlanAuditDriftThresholds', 'escalation_rate_max'): '*float64',
    ('PlanAuditDriftThresholds', 'escalation_rate_min'): '*float64',
    ('PlanAuditDriftThresholds', 'auto_approval_rate_max'): '*float64',
    ('PlanAuditDriftThresholds', 'human_override_rate_max'): '*float64',
    ('PlanAuditEntry', 'verdict'): '*GovernanceDecision',
    ('PlanAuditEntry', 'mode'): 'GovernanceMode',
    ('PlanAuditEntry', 'findings'): 'PlanAuditFinding',
    ('PlanAuditEntry', 'outcome'): 'OutcomeType',
    ('PlanAuditEntry', 'committed_budget'): '*float64',
    ('PlanAuditEntry', 'purchase_type'): 'PurchaseType',
    ('PlanAuditFinding', 'severity'): 'EscalationSeverity',
    ('PlanAuditFinding', 'confidence'): '*float64',
    ('PlanAuditGovernedAction', 'purchase_type'): 'PurchaseType',
    ('SyncGovernanceSuccess', 'accounts'): 'SyncGovernanceAccountResult',
    ('SyncGovernanceAccountResult', 'governance_agents'): 'SyncGovernanceAgentResult',
    ('ListCreativeFormatsResponse', 'creative_agents'): 'CreativeAgentRef',
    ('BuildCreativeRequest', 'preview_inputs'): 'BuildCreativePreviewInput',
    ('BuildCreativeRequest', 'max_spend'): '*BuildCreativeMaxSpend',
    ('BuildCreativeRequest', 'variant_axis'): '*BuildCreativeVariantAxis',
    ('BuildCreativeRequest', 'signal_conditions'): 'BuildCreativeSignalCondition',
    ('GetMediaBuysRequest', 'status_filter'): '*MediaBuyStatusFilter',
    ('GetMediaBuyDeliveryRequest', 'status_filter'): '*MediaBuyStatusFilter',
    ('GetCollectionListRequest', 'pagination'): '*CollectionRequestPagination',
    ('CollectionListChangedWebhook', 'change_summary'): '*CollectionChangeSummary',
    ('PropertyListChangedWebhook', 'change_summary'): '*PropertyChangeSummary',
    ('GetProductsRequest', 'refine'): 'GetProductsRefineItem',
    ('GetProductsResponse', 'refinement_applied'): 'GetProductsRefinementAppliedItem',
    ('ArtifactWebhookPayload', 'pagination'): '*ArtifactWebhookPagination',
    ('ArtifactWebhookPayload', 'artifacts'): 'ArtifactWebhookArtifact',
    ('RightsConstraint', 'rights_agent'): 'RightsAgentRef',
    ('Product', 'product_card'): '*ProductCard',
    ('Product', 'product_card_detailed'): '*ProductCardDetailed',
    ('ProductFilters', 'required_vendor_metrics'): 'RequiredVendorMetric',
    ('ForecastPoint', 'dimensions'): '[]ForecastPointDimension',
    ('ForecastPoint', 'viewability'): '*ForecastViewability',
    ('ReportingCapabilities', 'vendor_metrics'): 'ReportingVendorMetric',
    ('CreativePolicy', 'provenance_requirements'): '*CreativeProvenanceRequirements',
    ('CreativePolicy', 'accepted_verifiers'): 'CreativeAcceptedVerifier',
    ('CreativeFormat', 'format_card'): '*CreativeFormatCard',
    ('CreativeFormat', 'format_card_detailed'): '*CreativeFormatCardDetailed',
    ('CreativeAsset', 'provenance'): '*Provenance',
    ('CreativeManifest', 'provenance'): '*Provenance',
    ('Provenance', 'ai_tool'): '*ProvenanceAITool',
    ('Artifact', 'metadata'): '*ArtifactMetadata',
    ('Artifact', 'identifiers'): '*ArtifactIdentifiers',
    ('ComplyTestControllerRequest', 'params'): 'map[string]any',
    ('Provenance', 'declared_by'): '*ProvenanceDeclaredBy',
    ('Provenance', 'c2pa'): '*ProvenanceC2PA',
    ('Provenance', 'disclosure'): '*ProvenanceDisclosure',
    ('Provenance', 'verification'): 'ProvenanceVerification',
    ('Provenance', 'embedded_provenance'): 'ProvenanceEmbeddedProvenance',
    ('Provenance', 'watermarks'): 'ProvenanceWatermark',
    ('ProvenanceEmbeddedProvenance', 'verify_agent'): '*ProvenanceVerifyAgent',
    ('ProvenanceWatermark', 'verify_agent'): '*ProvenanceVerifyAgent',
    ('ProvenanceDisclosure', 'jurisdictions'): 'ProvenanceDisclosureJurisdiction',
    ('ProvenanceDisclosureJurisdiction', 'render_guidance'): '*ProvenanceDisclosureRenderGuidance',
    ('Product', 'installments'): 'Installment',
    ('Proposal', 'total_budget_guidance'): '*ProposalBudgetGuidance',
    ('InsertionOrder', 'terms'): '*InsertionOrderTerms',
    ('InsertionOrderTerms', 'total_budget'): '*InsertionOrderBudget',
    ('Installment', 'derivative_of'): '*InstallmentDerivative',
    ('Product', 'metric_optimization'): '*ProductMetricOptimization',
    ('Product', 'conversion_tracking'): '*ProductConversionTracking',
    ('Product', 'trusted_match'): '*ProductTrustedMatch',
    ('Product', 'material_submission'): '*ProductMaterialSubmission',
    ('ProductTrustedMatch', 'providers'): 'ProductTrustedMatchProvider',
    ('ProductFilters', 'budget_range'): '*ProductFilterBudgetRange',
    ('ProductFilters', 'metros'): 'ProductFilterMetro',
    ('ProductFilters', 'trusted_match'): '*ProductFilterTrustedMatch',
    ('ProductFilters', 'required_features'): 'map[string]any',
    ('ProductFilters', 'required_geo_targeting'): 'ProductFilterGeoTargetingRequirement',
    ('ProductFilters', 'postal_areas'): 'PostalArea',
    ('ProductFilters', 'geo_proximity'): 'ProductFilterGeoProximity',
    ('ProductFilters', 'keywords'): 'ProductFilterKeyword',
    ('ProductFilterBudgetRange', 'min'): '*float64',
    ('ProductFilterBudgetRange', 'max'): '*float64',
    ('ProductFilterTrustedMatch', 'providers'): 'ProductFilterTrustedMatchProvider',
    ('ProductFilterGeoProximity', 'lat'): '*float64',
    ('ProductFilterGeoProximity', 'lng'): '*float64',
    ('ProductFilterGeoProximity', 'travel_time'): '*ProductFilterTravelTime',
    ('ProductFilterGeoProximity', 'radius'): '*ProductFilterRadius',
    ('ProductFilterGeoProximity', 'geometry'): '*ProductFilterGeometry',
    ('SignalTargeting', 'min_value'): '*float64',
    ('SignalTargeting', 'max_value'): '*float64',
    ('Signal', 'taxonomy'): '*SignalTaxonomy',
    ('Signal', 'onboarder'): '*SignalOnboarder',
    ('Signal', 'modeling'): '*SignalModeling',
    ('Signal', 'data_subject_rights'): '*SignalDataSubjectRights',
    ('SignalTaxonomy', 'values'): 'SignalTaxonomyValue',
    ('SignalTaxonomy', 'value_mappings'): 'SignalTaxonomyValueMapping',
    ('SignalModeling', 'seed_source'): '*SignalModelingSeedSource',
    ('SignalModeling', 'disclosure'): '*SignalModelingDisclosure',
    ('SignalModelingDisclosure', 'jurisdictions'): 'SignalModelingDisclosureJurisdiction',
    ('SignalDataSubjectRights', 'channels'): 'SignalDataSubjectRightsChannel',
    ('PriceBreakdown', 'adjustments'): 'PriceAdjustment',
    ('CreativeFormat', 'disclosure_capabilities'): 'CreativeFormatDisclosureCapability',
    ('CreativeAsset', 'inputs'): 'CreativeAssetInput',
    ('Targeting', 'store_catchments'): 'TargetingStoreCatchment',
    ('DeliveryTotals', 'by_event_type'): 'DeliveryEventTypeMetrics',
    ('DeliveryTotals', 'dooh_metrics'): '*DeliveryDOOHMetrics',
    ('DeliveryTotals', 'viewability'): '*DeliveryViewability',
    ('DeliveryTotals', 'by_action_source'): 'DeliveryActionSourceMetrics',
    ('DeliveryDOOHMetrics', 'venue_breakdown'): 'DeliveryDOOHVenueBreakdown',
    ('MediaBuyDeliveryTotals', 'by_event_type'): 'DeliveryEventTypeMetrics',
    ('MediaBuyDeliveryTotals', 'dooh_metrics'): '*DeliveryDOOHMetrics',
    ('MediaBuyDeliveryTotals', 'viewability'): '*DeliveryViewability',
    ('MediaBuyDeliveryTotals', 'by_action_source'): 'DeliveryActionSourceMetrics',
    ('MediaBuyDelivery', 'windows'): 'DeliveryWindow',
    ('MediaBuyDelivery', 'daily_breakdown'): 'MediaBuyDailyBreakdown',
    ('DeliveryWindow', 'by_package'): 'DeliveryWindowPackage',
    ('DeliveryWindow', 'totals'): '*DeliveryTotals',
    ('PackageDelivery', 'by_event_type'): 'DeliveryEventTypeMetrics',
    ('PackageDelivery', 'dooh_metrics'): '*DeliveryDOOHMetrics',
    ('PackageDelivery', 'viewability'): '*DeliveryViewability',
    ('PackageDelivery', 'by_action_source'): 'DeliveryActionSourceMetrics',
    ('PackageDelivery', 'by_catalog_item'): 'PackageCatalogItemDelivery',
    ('PackageDelivery', 'by_creative'): 'PackageCreativeDelivery',
    ('PackageDelivery', 'by_keyword'): 'PackageKeywordDelivery',
    ('PackageDelivery', 'by_geo'): 'PackageGeoDelivery',
    ('PackageDelivery', 'by_device_type'): 'PackageDeviceTypeDelivery',
    ('PackageDelivery', 'by_device_platform'): 'PackageDevicePlatformDelivery',
    ('PackageDelivery', 'by_audience'): 'PackageAudienceDelivery',
    ('PackageDelivery', 'by_placement'): 'PackagePlacementDelivery',
    ('PackageDelivery', 'daily_breakdown'): 'PackageDailyBreakdown',
    ('ProductCardDetailed', 'specifications'): 'ProductCardSpecification',
    ('GetAdcpCapabilitiesResponse', 'adcp'): 'ADCPVersion',
    ('GetAdcpCapabilitiesResponse', 'account'): 'AccountCapabilities',
    ('GetAdcpCapabilitiesResponse', 'media_buy'): 'MediaBuyCapabilities',
    ('GetAdcpCapabilitiesResponse', 'signals'): 'SignalsCapabilities',
    ('GetAdcpCapabilitiesResponse', 'governance'): 'GovernanceCapabilities',
    ('GetAdcpCapabilitiesResponse', 'sponsored_intelligence'): 'SICapabilities',
    ('GetAdcpCapabilitiesResponse', 'brand'): 'BrandCapabilities',
    ('GetAdcpCapabilitiesResponse', 'creative'): 'CreativeCapabilities',
    ('GetAdcpCapabilitiesResponse', 'request_signing'): 'RequestSigningCapabilities',
    ('GetAdcpCapabilitiesResponse', 'webhook_signing'): 'WebhookSigningCapabilities',
    ('GetAdcpCapabilitiesResponse', 'identity'): 'IdentityCapabilities',
    ('GetAdcpCapabilitiesResponse', 'compliance_testing'): 'ComplianceTestingCapabilities',
    ('GetAdcpCapabilitiesResponse', 'wholesale_feed_versioning'): '*CapabilitiesWholesaleFeedVersioning',
    ('GetAdcpCapabilitiesResponse', 'wholesale_feed_webhooks'): '*CapabilitiesWholesaleFeedWebhooks',
    ('ListAccountsResponse', 'accounts'): 'AccountWithAuthorization',
    ('AccountWithAuthorization', 'credit_limit'): '*AccountCreditLimit',
    ('AccountWithAuthorization', 'setup'): '*AccountSetup',
    ('AccountWithAuthorization', 'governance_agents'): 'AccountGovernanceAgent',
    ('AccountWithAuthorization', 'reporting_bucket'): '*ReportingBucket',
    ('AccountWithAuthorization', 'authorization'): '*AccountAuthorization',
    ('PackageInput', 'committed_metrics'): 'RequestedCommittedMetric',
    ('RequestedCommittedMetric', 'qualifier'): '*MetricQualifier',
    ('DeliveryAggregatedTotals', 'metric_aggregates'): 'DeliveryMetricAggregate',
    ('GetProductsResponse', 'filter_diagnostics'): '*GetProductsFilterDiagnostics',
    ('GetProductsFilterDiagnostics', 'excluded_by'): 'map[string]FilterExclusionDiagnostic',
    ('GetSignalsResponse', 'incomplete'): 'GetSignalsIncompleteItem',
    ('ProvenanceAuditObservationsSuccess', 'audit_observations'): 'AuditObservation',
    ('AuditObservation', 'details'): '*AuditObservationDetails',
    ('AuditObservationDetails', 'claimed_value'): 'AuditObservationClaimedValue',
    ('UpstreamTrafficSuccess', 'recorded_calls'): 'UpstreamRecordedCall',
    ('UpstreamRecordedCall', 'identifier_match_proofs'): 'IdentifierMatchProof',
    # format.json: renders[] and assets[] are oneOf items. Map to hand-written
    # Render/AssetSlot so reference-agent code can keep using typed literals.
    ('CreativeFormat', 'renders'): 'Render',
    ('CreativeFormat', 'assets'): 'AssetSlot',
    ('GetMediaBuysResponse', 'media_buys'): 'MediaBuyData',
    ('Package', 'optimization_goals'): 'OptimizationGoal',
    ('PackageInput', 'optimization_goals'): 'OptimizationGoal',
    ('PackageUpdate', 'optimization_goals'): 'OptimizationGoal',
    # Optional numeric policy: use a pointer only when omission and explicit
    # zero are both valid and semantically distinct. value_factor defaults to 1
    # and explicit 0 zeroes this event source. view_duration_seconds has
    # exclusiveMinimum: 0, so explicit 0 is invalid and the hand-written
    # OptimizationGoal field intentionally stays float64.
    ('CreativeAsset', 'weight'): '*float64',
    ('KeywordTarget', 'bid_price'): '*float64',
    ('AudienceSelector', 'min_value'): '*float64',
    ('AudienceSelector', 'max_value'): '*float64',
    ('ForecastPoint', 'budget'): '*float64',
    ('OptimizationGoalEventSource', 'value_factor'): '*float64',
    ('PackageInput', 'bid_price'): '*float64',
    ('PackageInput', 'budget'): '*float64',
    ('PackageInput', 'impressions'): '*float64',
    ('DemographicAgeRange', 'min'): '*int',
    ('DemographicAgeRange', 'max'): '*int',
    ('PackageUpdate', 'budget'): '*float64',
    ('PackageUpdate', 'bid_price'): '*float64',
    ('PackageUpdate', 'impressions'): '*float64',
    ('KeywordTargetUpdate', 'bid_price'): '*float64',
}

for _delivery_metric_type in (
    'PackageCatalogItemDelivery',
    'PackageCreativeDelivery',
    'PackageKeywordDelivery',
    'PackageGeoDelivery',
    'PackageDeviceTypeDelivery',
    'PackageDevicePlatformDelivery',
    'PackageAudienceDelivery',
    'PackagePlacementDelivery',
    'DeliveryWindowPackage',
):
    INLINE_TYPE_HINTS.update({
        (_delivery_metric_type, 'by_event_type'): 'DeliveryEventTypeMetrics',
        (_delivery_metric_type, 'quartile_data'): '*DeliveryQuartileData',
        (_delivery_metric_type, 'dooh_metrics'): '*DeliveryDOOHMetrics',
        (_delivery_metric_type, 'viewability'): '*DeliveryViewability',
        (_delivery_metric_type, 'by_action_source'): 'DeliveryActionSourceMetrics',
        (_delivery_metric_type, 'reach_window'): '*ReachWindow',
    })

# Initial allowlist for generated `any` fallbacks that are intentional protocol
# escape hatches rather than generator gaps. The coverage report still includes
# them, but marks them as allowed so CI can later fail only on unreviewed `any`.
INTENTIONAL_ANY_FIELD_NAMES = {
    'context',
    'ext',
    'payload',
}

INTENTIONAL_ANY_FIELDS = {
    ('CreativeFormat', 'delivery'): 'delivery specs are format-specific',
    ('CreativeAsset', 'assets'): 'asset payload shape depends on asset type',
    ('CreativeManifest', 'assets'): 'asset payload shape depends on asset type',
    ('ProductCard', 'manifest'): 'visual card manifest shape is format-defined',
    ('ProductCardDetailed', 'manifest'): 'visual card manifest shape is format-defined',
    ('CreativeFormatCard', 'manifest'): 'visual card manifest shape is format-defined',
    ('CreativeFormatCardDetailed', 'manifest'): 'visual card manifest shape is format-defined',
    ('LogEventRequest', 'events'): 'event payloads are seller/buyer-defined',
    ('Catalog', 'items'): 'inline catalog item schema depends on catalog type',
    ('CatalogFieldMapping', 'value'): 'catalog static literal values can be strings, numbers, booleans, arrays, or objects',
    ('CatalogFieldMapping', 'default'): 'catalog fallback literal values can be strings, numbers, booleans, arrays, or objects',
    ('MCPWebhookPayload', 'result'): 'core/async-response-data.json is a task-specific response union',
    ('ProductFilterGeometry', 'coordinates'): 'GeoJSON coordinates are shape-dependent for Polygon/MultiPolygon',
    ('GeoProximityGeometry', 'coordinates'): 'GeoJSON coordinates are shape-dependent for Polygon/MultiPolygon',
    ('SimulationSuccess', 'simulated'): 'test-controller simulation payload is scenario-specific',
    ('SimulationSuccess', 'cumulative'): 'test-controller cumulative state is scenario-specific',
    ('CheckGovernanceRequest', 'payload'): 'governance can evaluate different protocol payloads',
    ('CheckGovernanceFinding', 'details'): 'governance finding details are structured but category-specific',
    ('ReportPlanOutcomeFinding', 'details'): 'outcome finding details are structured but category-specific',
    # 3.1 forward-compatible allowances for auto-discovered referenced schemas.
    ('ProductFormatDeclaration', 'params'): 'canonical format parameters are schema-specific',
    ('VendorMetricValue', 'breakdown'): 'vendor metric breakdowns are vendor-defined',
    ('ForecastVendorMetricValue', 'breakdown'): 'vendor metric breakdowns are vendor-defined',
    ('Package', 'params'): 'package params are product/seller-specific',
    ('PackageInput', 'params'): 'package params are product/seller-specific',
    ('PerformanceFeedback', 'metric'): 'performance feedback metric payloads are metric-specific',
    ('ForecastPointDimension', 'signal_value'): 'forecast signal dimension values are signal-type-specific',
    ('AuditObservationDetails', 'observed_value'): 'audit observation values may be boolean, number, string, or null',
    ('AccountAuthorization', 'scope_name'): 'scope names are standard or custom-prefixed strings',
    ('GetAdcpCapabilitiesResponse', 'measurement'): 'measurement capabilities are vendor catalog payloads',
    ('GetProductsResponse', 'extensions'): 'response extensions are seller-defined extension payloads',
    ('FilterExclusionDiagnostic', 'values'): 'filter diagnostic values are filter-specific strings or objects',
    ('ComplyTestControllerRequest', 'account'): 'compliance controller account selectors are scenario-specific',
    ('Artifact', 'assets'): 'artifact assets are a discriminated content union',
    ('ArtifactMetadata', 'open_graph'): 'Open Graph metadata accepts arbitrary protocol properties',
    ('ArtifactMetadata', 'twitter_card'): 'Twitter Card metadata accepts arbitrary card properties',
    ('ArtifactMetadata', 'json_ld'): 'JSON-LD entries are schema.org objects with arbitrary properties',
    ('ComplyTestControllerRequest', 'params'): 'compliance controller params are scenario-specific and must preserve explicit scalar values',
    ('BuildCreativeRequest', 'config'): 'build-creative-request config is a free-form transformer render map (type:object, additionalProperties only, no defined properties); legal keys are dynamic per transformer',
    ('BuildCreativeRequest', 'evaluator'): 'core/evaluator-spec.json is an experimental oneOf union (exemplar/identifier/agent forms, mutually exclusive) with additionalProperties:true; flattening would lose the union constraint, so it stays an open escape hatch',
    ('BuildCreativeVariantAxis', 'values'): 'variant_axis.values items are schema-open ({}) — caller-fixed axis values are typed per dimension (string voices, etc.), so the element type is intentionally any',
    ('GeoBreakdownSupport', 'postal_area'): 'core/postal-area-support.json is an open propertyNames-constrained map keyed by arbitrary ISO country codes (and deprecated legacy aliases) with mixed value types (arrays of postal-system enums vs deprecated boolean aliases); it has no clean closed struct',
    ('ProductFilters', 'required_features'): '3.2 feature filters mix boolean flags with structured capability requirements',
}

# The 3.2 prerelease expands many existing schemas with shapes that this
# generator cannot yet represent (nested objects, unions, and composed refs).
# Keep the exact beta.9 compatibility surface explicit so adding another
# dynamic fallback still fails coverage. Proposal-negotiation fields are not in
# this set: those are fully typed above. Remove entries as generator support is
# added, and clear the set before adopting stable 3.2.
SCHEMA_32_BETA_ANY_FIELDS = {
    ('Account', 'destination_billing_entity'),
    ('Account', 'identity_change'),
    ('AccountWithAuthorization', 'destination_billing_entity'),
    ('AccountWithAuthorization', 'identity_change'),
    ('BuildCreativeRequest', 'creative_representation_set'),
    ('CanonicalProduct', 'acceptance_policy_profile_ids'),
    ('CanonicalProduct', 'allowed_actions'),
    ('CanonicalProduct', 'audience_evidence'),
    ('CanonicalProduct', 'audience_evidence_selections'),
    ('CanonicalProduct', 'catalog_match'),
    ('CanonicalProduct', 'demographic_targeting'),
    ('CanonicalProduct', 'forecast'),
    ('CanonicalProduct', 'format_options'),
    ('CanonicalProduct', 'list_applications'),
    ('CanonicalProduct', 'measurement_terms'),
    ('CanonicalProduct', 'placements'),
    ('CanonicalProduct', 'pricing_options'),
    ('CanonicalProduct', 'reporting_capabilities'),
    ('CanonicalProposal', 'commercial_terms'),
    ('CanonicalProposal', 'forecast'),
    ('CanonicalProposal', 'total_budget_guidance'),
    ('CheckGovernanceRequest', 'execution_commitment'),
    ('CheckGovernanceRequest', 'proposed_commitment'),
    ('CheckGovernanceRequest', 'runtime_attestations'),
    ('CheckGovernanceResponse', 'delivery_statement'),
    ('CheckGovernanceResponse', 'runtime_attestation_evaluations'),
    ('CreateMediaBuyRequest', 'bidding'),
    ('CreateMediaBuySuccess', 'bidding'),
    ('CreateMediaBuySuccess', 'warnings'),
    ('CreativeAsset', 'component_assets'),
    ('CreativeManifest', 'component_assets'),
    ('DeliveryReportingDimensions', 'catalog_item'),
    ('DeliveryReportingDimensions', 'creative'),
    ('DeliveryReportingDimensions', 'demographic'),
    ('DeliveryReportingDimensions', 'format'),
    ('DeliveryReportingDimensions', 'keyword'),
    ('DeliveryReportingDimensions', 'spot'),
    ('DeliveryTotals', 'ooh_metrics'),
    ('DeliveryTotals', 'time_based_views'),
    ('DeliveryTotals', 'vendor_metric_values'),
    ('DeliveryViewability', 'viewed_seconds_histogram'),
    ('DeliveryViewability', 'viewed_seconds_percentiles'),
    ('DeliveryWindowPackage', 'ooh_metrics'),
    ('DeliveryWindowPackage', 'time_based_views'),
    ('DeliveryWindowPackage', 'vendor_metric_values'),
    ('GetAdcpCapabilitiesResponse', 'measurement_gateway'),
    ('GetAdcpCapabilitiesResponse', 'oauth'),
    ('GetProductsRequest', 'acceptance_context'),
    ('GetProductsRequest', 'fields'),
    ('GetProductsRequest', 'required_overlay_support'),
    ('MediaBuyDeliveryTotals', 'ooh_metrics'),
    ('MediaBuyDeliveryTotals', 'time_based_views'),
    ('MediaBuyDeliveryTotals', 'vendor_metric_values'),
    ('Package', 'audience_evidence_selections'),
    ('Package', 'bidding'),
    ('Package', 'formats_pending'),
    ('Package', 'formats_to_provide'),
    ('Package', 'targeting_resolution'),
    ('PackageAudienceDelivery', 'ooh_metrics'),
    ('PackageAudienceDelivery', 'time_based_views'),
    ('PackageAudienceDelivery', 'vendor_metric_values'),
    ('PackageCatalogItemDelivery', 'ooh_metrics'),
    ('PackageCatalogItemDelivery', 'time_based_views'),
    ('PackageCatalogItemDelivery', 'vendor_metric_values'),
    ('PackageCreativeDelivery', 'ooh_metrics'),
    ('PackageCreativeDelivery', 'time_based_views'),
    ('PackageCreativeDelivery', 'vendor_metric_values'),
    ('PackageDelivery', 'by_collection'),
    ('PackageDelivery', 'by_collection_property'),
    ('PackageDelivery', 'by_demographic'),
    ('PackageDelivery', 'by_format'),
    ('PackageDelivery', 'by_installment'),
    ('PackageDelivery', 'by_installment_property'),
    ('PackageDelivery', 'by_placement_property'),
    ('PackageDelivery', 'by_property'),
    ('PackageDelivery', 'by_spot'),
    ('PackageDelivery', 'ooh_metrics'),
    ('PackageDelivery', 'time_based_views'),
    ('PackageDelivery', 'vendor_metric_values'),
    ('PackageDevicePlatformDelivery', 'ooh_metrics'),
    ('PackageDevicePlatformDelivery', 'time_based_views'),
    ('PackageDevicePlatformDelivery', 'vendor_metric_values'),
    ('PackageDeviceTypeDelivery', 'ooh_metrics'),
    ('PackageDeviceTypeDelivery', 'time_based_views'),
    ('PackageDeviceTypeDelivery', 'vendor_metric_values'),
    ('PackageGeoDelivery', 'ooh_metrics'),
    ('PackageGeoDelivery', 'time_based_views'),
    ('PackageGeoDelivery', 'vendor_metric_values'),
    ('PackageInput', 'bidding'),
    ('PackageKeywordDelivery', 'ooh_metrics'),
    ('PackageKeywordDelivery', 'time_based_views'),
    ('PackageKeywordDelivery', 'vendor_metric_values'),
    ('PackagePlacementDelivery', 'ooh_metrics'),
    ('PackagePlacementDelivery', 'placement_identity'),
    ('PackagePlacementDelivery', 'time_based_views'),
    ('PackagePlacementDelivery', 'vendor_metric_values'),
    ('PackageUpdate', 'bidding'),
    ('PlanAuditEntry', 'amount'),
    ('PlanAuditEntry', 'delivery'),
    ('PlanAuditEntry', 'delivery_statement'),
    ('PlanAuditEntry', 'error'),
    ('PlanAuditEntry', 'evidence'),
    ('PlanAuditEntry', 'runtime_attestations'),
    ('PlanAuditFinding', 'details'),
    ('PlanAuditGovernedAction', 'delivery_reporting_period'),
    ('PlannedDelivery', 'bidding'),
    ('PolicyCategoryDefinition', 'facets'),
    ('PolicyEntry', 'acceptance_profile'),
    ('PolicyEntry', 'issuer'),
    ('Product', 'acceptance_policy_profile_ids'),
    ('Product', 'allowed_actions'),
    ('Product', 'audience_activation'),
    ('Product', 'audience_evidence'),
    ('Product', 'audience_evidence_selections'),
    ('Product', 'demographic_targeting'),
    ('Product', 'list_applications'),
    ('Product', 'overlay_support'),
    ('Product', 'targeting_resolution'),
    ('ProductCardDetailed', 'reference_assets'),
    ('ProductDiscoveryCriteria', 'acceptance_context'),
    ('ProductDiscoveryCriteria', 'catalog'),
    ('ProductDiscoveryCriteria', 'offer_filters'),
    ('ProductDiscoveryCriteria', 'outcome_target'),
    ('ProductDiscoveryCriteria', 'required_overlay_support'),
    ('ProductFilters', 'audience_activation_methods'),
    ('ProductFormatDeclaration', 'tracker_execution_contract'),
    ('ProvidePerformanceFeedbackRequest', 'evidence'),
    ('ProvidePerformanceFeedbackRequest', 'metric'),
    ('ReportPlanOutcomeError', 'details'),
    ('ReportingCapabilities', 'supports_demographic_breakdown'),
    ('RightsConstraint', 'attestation_refs'),
    ('RightsConstraint', 'disclosure'),
    ('SyncCreativesRequest', 'assignment_operations'),
    ('Targeting', 'demographics'),
    ('Targeting', 'geo_places'),
    ('Targeting', 'geo_places_exclude'),
    ('Targeting', 'placement_selection'),
    ('UpdateMediaBuyRequest', 'bidding'),
    ('UpdateMediaBuyRequest', 'total_budget'),
}

# Enum schemas
ENUM_DIR = "enums"

def safe_comment(text, max_len=80):
    """Sanitize text for embedding in a Go // comment. Strips newlines to
    prevent code injection via schema descriptions."""
    if not text:
        return ''
    sanitized = re.sub(r'\s+', ' ', text.replace('\r', ' ')).strip()
    if len(sanitized) <= max_len:
        return sanitized

    cutoff = sanitized[:max_len].rstrip()
    word_end = cutoff.rfind(' ')
    if word_end > 0:
        return cutoff[:word_end].rstrip(' ,;:-')
    return cutoff

def field_description(type_name, json_name, prop):
    """Return the preferred field description, including reviewed fallbacks."""
    override = FIELD_DESCRIPTION_OVERRIDES.get((type_name, json_name))
    if override is not None:
        return override
    desc = prop.get('description', '')
    if desc:
        return desc
    spec = FIELD_DESCRIPTION_FALLBACK_SPECS.get((type_name, json_name))
    if spec is None:
        return ''
    try:
        fallback = load_schema_spec(spec)
    except SCHEMA_RESOLUTION_ERRORS:
        return ''
    if isinstance(fallback, dict):
        return fallback.get('description', '')
    return ''

def deprecated_comment(prop):
    """Return the Go deprecation notice for a deprecated schema property."""
    if not prop.get('deprecated'):
        return ''
    desc = prop.get('description', '').replace('\n', ' ').replace('\r', '').strip()
    desc = re.sub(r'^\s*deprecated\s*:\s*', '', desc, flags=re.IGNORECASE)
    first_sentence = re.match(r'(.+?\.)(?:\s|$)', desc)
    if first_sentence:
        desc = first_sentence.group(1)
    desc = safe_comment(desc, 120)
    if desc:
        return f'Deprecated: {desc}'
    return 'Deprecated: This field is deprecated.'

def load_schema(path):
    """Load a JSON schema file, preserving property order."""
    path = Path(path)
    if not path.is_absolute():
        path = SCRIPT_DIR / path
    with open(path) as f:
        # Use object_pairs_hook to preserve key order
        return json.load(f, object_pairs_hook=OrderedDict)

def schema_exists(path):
    path = Path(path)
    if not path.is_absolute():
        path = SCRIPT_DIR / path
    return path.exists()

def json_pointer_get(doc, pointer):
    """Resolve a JSON Pointer fragment against a decoded JSON document."""
    if pointer in ('', None):
        return doc
    if not pointer.startswith('/'):
        raise ValueError(f'unsupported JSON pointer: {pointer}')
    node = doc
    for raw_part in pointer.split('/')[1:]:
        part = raw_part.replace('~1', '/').replace('~0', '~')
        if isinstance(node, list):
            node = node[int(part)]
        else:
            node = node[part]
    return node

def load_schema_spec(spec):
    """Load `path.json` or `path.json#/json/pointer` relative to schemas/."""
    path_part, _, pointer = spec.partition('#')
    schema = load_schema(path_part)
    if pointer:
        return json_pointer_get(schema, pointer)
    return schema

def resolve_ref_schema(ref):
    """Load a schema referenced by $ref. Supports bundled refs of the form
    /schemas/{version}/{path}.json and optional JSON Pointer fragments."""
    if not isinstance(ref, str):
        return None
    m = re.match(
        r'^(?:https://adcontextprotocol\.org)?/schemas/[^/]+/(.+\.json)(#.*)?$',
        ref,
    )
    if not m:
        return None
    spec = m.group(1) + (m.group(2) or '')
    path_part = spec.split('#', 1)[0]
    path = (SCRIPT_DIR / path_part).resolve()
    root = SCRIPT_DIR.resolve()
    if root != path and root not in path.parents:
        return None
    if not path.exists():
        return None
    try:
        return load_schema_spec(spec)
    except SCHEMA_RESOLUTION_ERRORS as e:
        print(f'// Warning: skipped ref {ref}: {e}', file=sys.stderr)
        return None

def ref_to_schema_path(ref):
    """Return the repo-relative schema path for a bundled local $ref."""
    if not isinstance(ref, str):
        return None
    m = re.match(
        r'^(?:https://adcontextprotocol\.org)?/schemas/[^/]+/(.+\.json)(#.*)?$',
        ref,
    )
    if not m:
        return None
    rel = m.group(1)
    path = (SCRIPT_DIR / rel).resolve()
    root = SCRIPT_DIR.resolve()
    if root != path and root not in path.parents:
        return None
    if not path.exists():
        return None
    return rel

def iter_auto_discovery_refs(schema):
    """Yield $refs that are part of the ordinary object field graph.

    Top-level and nested oneOf/anyOf branches are deliberately skipped for
    auto-discovery. Those unions usually need explicit SDK policy (flatten,
    variant helpers, or intentional openness) instead of incidental type export.
    """
    if isinstance(schema, dict):
        ref = schema.get('$ref')
        if isinstance(ref, str):
            yield ref
        for key, value in schema.items():
            if key in ('oneOf', 'anyOf'):
                continue
            yield from iter_auto_discovery_refs(value)
    elif isinstance(schema, list):
        for item in schema:
            yield from iter_auto_discovery_refs(item)

def scalar_union_go_type(schema):
    """Return the element Go type for `scalar or []scalar` unions.

    This intentionally covers only the narrow wire shape used by status_filter:
    one branch is a string/enum scalar and the other is an array of that same
    scalar. More complex oneOf schemas need a discriminator-aware strategy.
    """
    if not isinstance(schema, dict):
        return None
    branches = schema.get('oneOf') or schema.get('anyOf') or []
    if len(branches) != 2:
        return None

    def branch_scalar_type(branch):
        if not isinstance(branch, dict):
            return None
        if '$ref' in branch and is_enum_ref(branch['$ref']):
            return ref_to_go_name(branch['$ref'])
        if branch.get('type') == 'string':
            return 'string'
        return None

    scalar_type = None
    array_item_type = None
    for branch in branches:
        current = branch_scalar_type(branch)
        if current:
            scalar_type = current
            continue
        if isinstance(branch, dict) and branch.get('type') == 'array':
            array_item_type = branch_scalar_type(branch.get('items', {}))

    if scalar_type and array_item_type and scalar_type == array_item_type:
        return scalar_type
    return None

def schema_accepts_empty_array(schema):
    """Report whether a scalar-or-array union permits an empty array branch."""
    if not isinstance(schema, dict):
        return True
    branches = schema.get('oneOf') or schema.get('anyOf') or []
    for branch in branches:
        if isinstance(branch, dict) and branch.get('type') == 'array':
            return int(branch.get('minItems', 0)) == 0
    return True

def schema_properties(schema, _visited=None):
    """Return an ordered union of properties declared directly, through $ref,
    or through composition. allOf is flattened for generated Go structs."""
    if _visited is None:
        _visited = set()
    props = OrderedDict()
    if not isinstance(schema, dict):
        return props
    ref = schema.get('$ref')
    if ref and ref not in _visited:
        _visited.add(ref)
        ref_schema = resolve_ref_schema(ref)
        if ref_schema:
            props.update(schema_properties(ref_schema, _visited))
    for key in ('allOf', 'anyOf', 'oneOf'):
        for branch in schema.get(key, []):
            props.update(schema_properties(branch, _visited))
    for json_name, prop in schema.get('properties', {}).items():
        props[json_name] = prop
    return props

def schema_required_names(schema, _visited=None):
    """Return fields required by the schema. For generation, allOf-required
    fields are required; anyOf/oneOf branch requirements are intentionally not
    merged because only one branch applies."""
    if _visited is None:
        _visited = set()
    required = set()
    if not isinstance(schema, dict):
        return required
    ref = schema.get('$ref')
    if ref and ref not in _visited:
        _visited.add(ref)
        ref_schema = resolve_ref_schema(ref)
        if ref_schema:
            required.update(schema_required_names(ref_schema, _visited))
    required.update(schema.get('required', []))
    for branch in schema.get('allOf', []):
        required.update(schema_required_names(branch, _visited))
    return required

def has_struct_fields(schema):
    """True when a schema can emit a Go struct. This includes top-level oneOf
    schemas whose branches declare object properties; Go represents those as a
    flattened struct with the union of variant fields, matching the hand-written
    oneOf pattern used elsewhere in this package."""
    return bool(schema_properties(schema))

def can_auto_generate_ref_schema(schema):
    """True when an otherwise unlisted $ref target is safe to emit as a struct.

    Auto-discovery is intentionally narrower than the hand-curated schema lists:
    it picks up plain object schemas and object allOf compositions, but leaves
    top-level anyOf/oneOf union payloads alone. Broad union payloads often need
    custom Go ergonomics or intentionally remain open extension points.
    """
    if not isinstance(schema, dict):
        return False
    if schema.get('type') == 'object' or 'properties' in schema or 'allOf' in schema:
        return has_struct_fields(schema)
    if 'oneOf' in schema and schema.get('discriminator'):
        branches = schema.get('oneOf', [])
        if branches and all(isinstance(branch, dict) for branch in branches):
            return has_struct_fields(schema)
    return False

def ref_to_go_name(ref):
    """Convert a $ref path to a Go type name.
    /schemas/core/product.json -> Product
    /schemas/enums/delivery-type.json -> DeliveryType (string)
    """
    # Strip /schemas/ prefix and .json suffix
    ref = ref.replace("/schemas/", "").replace(".json", "")
    parts = ref.split("/")
    name = parts[-1]  # e.g., "delivery-type" or "product"
    return pascal_case(name)

def pascal_case(s):
    """Convert kebab-case or snake_case to PascalCase. Fully capitalizes
    known acronyms (e.g. 'id' -> 'ID') and their trivial plurals (e.g.
    'ids' -> 'IDs'), which matters for Go idioms like FormatIDs, PropertyIDs."""
    parts = re.split(r'[-_]', s)
    result = []
    acronyms = {'id', 'url', 'uri', 'api', 'http', 'html', 'css', 'json', 'xml', 'uid', 'ip', 'rid', 'cpm', 'cpc', 'cpa', 'cta', 'mcp', 'ai', 'c2pa'}
    for p in parts:
        lp = p.lower()
        if lp in acronyms:
            result.append(p.upper())
        elif len(lp) > 1 and lp.endswith('s') and lp[:-1] in acronyms:
            result.append(p[:-1].upper() + 's')
        else:
            result.append(p[0].upper() + p[1:] if p else '')
    return ''.join(result)

def is_enum_ref(ref):
    """Check if a $ref points to an enum schema."""
    return '/enums/' in ref


_WILL_GENERATE_CACHE = None
_AUTO_REF_SCHEMA_CACHE = None

def _reset_will_generate_cache():
    """Clear the cache so the next `_will_generate_set()` call rebuilds from
    current CORE_SCHEMAS/TOOL_SCHEMAS/WEBHOOK_SCHEMAS. Called at the top of
    `generate()` to guarantee correctness when the module is used across
    multiple runs (e.g. imported by lint.py then invoked standalone)."""
    global _WILL_GENERATE_CACHE, _AUTO_REF_SCHEMA_CACHE
    _WILL_GENERATE_CACHE = None
    _AUTO_REF_SCHEMA_CACHE = None


def _schema_spec_path(spec):
    """Return the filesystem schema path portion from path.json[#/pointer]."""
    return spec.split('#', 1)[0]


def _generation_seed_schema_paths():
    """Schema files whose refs define the generator's reachable type graph."""
    paths = []
    seen = set()
    for rel in CORE_SCHEMAS + SUPPORT_SCHEMAS + TOOL_SCHEMAS + WEBHOOK_SCHEMAS:
        if rel not in seen:
            paths.append(rel)
            seen.add(rel)
    for spec in INLINE_SCHEMA_TYPES.values():
        rel = _schema_spec_path(spec)
        if rel not in seen:
            paths.append(rel)
            seen.add(rel)
    for specs in UNION_SCHEMA_TYPES.values():
        if isinstance(specs, str):
            specs = (specs,)
        for spec in specs:
            rel = _schema_spec_path(spec)
            if rel not in seen:
                paths.append(rel)
                seen.add(rel)
    return paths


def _schema_type_name(path):
    return REF_ALIASES.get(pascal_case(Path(path).stem), pascal_case(Path(path).stem))


def _explicit_generated_type_names():
    """Names generated without auto-ref discovery."""
    names = set(KNOWN_TYPES)
    for rel in CORE_SCHEMAS + SUPPORT_SCHEMAS:
        if schema_exists(rel):
            names.add(_schema_type_name(rel))
    for rel in TOOL_SCHEMAS + WEBHOOK_SCHEMAS:
        if schema_exists(rel):
            names.add(pascal_case(Path(rel).stem))
    names.update(INLINE_SCHEMA_TYPES.keys())
    names.update(supported_union_schemas(skip_names=KNOWN_TYPES).keys())
    return names


def schema_has_unreviewed_any(type_name, schema):
    """Return True when emitting this struct would add unreviewed dynamic any."""
    props = schema_properties(schema)
    required_set = schema_required_names(schema)
    for json_name, prop in props.items():
        if not isinstance(prop, dict):
            continue
        go_type, reason = field_go_type_info(type_name, json_name, prop, required_set)
        if not contains_dynamic_any(go_type):
            continue
        if any_allowance(type_name, json_name, go_type, reason) is None:
            return True
    return False


def auto_ref_schema_paths():
    """Return unlisted local object schemas reachable by $ref from generated roots.

    This lets new protocol object types become Go structs without requiring a
    hand edit to CORE_SCHEMAS for every additive schema file. The result is
    stable-sorted for deterministic generated output.
    """
    global _AUTO_REF_SCHEMA_CACHE
    if _AUTO_REF_SCHEMA_CACHE is not None:
        return _AUTO_REF_SCHEMA_CACHE

    configured = set(_generation_seed_schema_paths())
    discovered = set()
    queued = list(_generation_seed_schema_paths())
    visited = set()

    while queued:
        rel = queued.pop(0)
        if rel in visited or not schema_exists(rel):
            continue
        visited.add(rel)
        try:
            schema = load_schema(rel)
        except SCHEMA_RESOLUTION_ERRORS as e:
            print(f'// Warning: skipped schema {rel}: {e}', file=sys.stderr)
            continue

        for ref in iter_auto_discovery_refs(schema):
            ref_path = ref_to_schema_path(ref)
            if not ref_path or ref_path in visited:
                continue
            if not schema_exists(ref_path):
                continue
            try:
                ref_schema = load_schema(ref_path)
            except SCHEMA_RESOLUTION_ERRORS as e:
                print(f'// Warning: skipped schema {ref_path}: {e}', file=sys.stderr)
                continue

            if ref_path not in queued:
                queued.append(ref_path)
            if ref_path not in configured and can_auto_generate_ref_schema(ref_schema):
                discovered.add(ref_path)

    explicit_names = _explicit_generated_type_names()
    paths_by_name = {}
    for path in discovered:
        paths_by_name.setdefault(_schema_type_name(path), []).append(path)
    collisions = {
        name: sorted(paths)
        for name, paths in paths_by_name.items()
        if len(paths) > 1 or (name in explicit_names and name not in KNOWN_TYPES)
    }
    if collisions:
        details = '; '.join(
            f'{name}: {", ".join(paths)}'
            for name, paths in sorted(collisions.items())
        )
        raise ValueError(
            'auto-discovered schema type name collision; add REF_ALIASES or '
            f'promote one schema explicitly: {details}'
        )

    # Filter out schemas that would add new unreviewed `any` fallbacks. Use a
    # fixed point so a kept candidate cannot type-reference another candidate
    # that is later filtered out.
    remaining = set(discovered)
    global _WILL_GENERATE_CACHE
    previous_will_generate_cache = _WILL_GENERATE_CACHE
    try:
        while True:
            _WILL_GENERATE_CACHE = explicit_names | {
                _schema_type_name(path) for path in remaining
            }
            filtered = []
            for path in sorted(remaining):
                name = _schema_type_name(path)
                # Explicit hand-written names are valid discovery leaves, but
                # are not emitted again in the auto-ref section.
                if name in explicit_names:
                    continue
                try:
                    schema = load_schema(path)
                except SCHEMA_RESOLUTION_ERRORS as e:
                    print(f'// Warning: skipped schema {path}: {e}', file=sys.stderr)
                    continue
                if schema_has_unreviewed_any(name, schema):
                    continue
                filtered.append(path)
            filtered_set = set(filtered)
            if filtered_set == {
                path for path in remaining
                if _schema_type_name(path) not in explicit_names
            }:
                break
            remaining = filtered_set
        filtered = sorted(remaining)
        filtered = [
            path for path in filtered
            if _schema_type_name(path) not in explicit_names
        ]
    finally:
        _WILL_GENERATE_CACHE = previous_will_generate_cache

    _AUTO_REF_SCHEMA_CACHE = tuple(filtered)
    return _AUTO_REF_SCHEMA_CACHE


def _will_generate_set():
    """Names (after REF_ALIASES) that this generator run will emit.
    Used so `resolve_go_type` can typed-reference a schema-derived type even
    though it hasn't been emitted yet at the moment of the first reference.
    Excludes schemas that will not produce a struct. Core/support oneOf schemas
    with object branches are included because generation flattens their variant
    fields into one struct; top-level tool response oneOf schemas are included
    because generation emits a closed interface plus concrete variants."""
    global _WILL_GENERATE_CACHE
    if _WILL_GENERATE_CACHE is not None:
        return _WILL_GENERATE_CACHE
    names = set()
    for rel in CORE_SCHEMAS + SUPPORT_SCHEMAS + list(auto_ref_schema_paths()):
        if not schema_exists(rel):
            continue
        try:
            schema = load_schema(rel)
        except SCHEMA_RESOLUTION_ERRORS as e:
            print(f'// Warning: skipped schema {rel}: {e}', file=sys.stderr)
            continue
        if has_struct_fields(schema):
            stem = Path(rel).stem
            name = REF_ALIASES.get(pascal_case(stem), pascal_case(stem))
            names.add(name)
    for rel in TOOL_SCHEMAS + WEBHOOK_SCHEMAS:
        if not schema_exists(rel):
            continue
        try:
            schema = load_schema(rel)
        except SCHEMA_RESOLUTION_ERRORS as e:
            print(f'// Warning: skipped schema {rel}: {e}', file=sys.stderr)
            continue
        if schema.get('type') == 'object' and 'properties' in schema:
            stem = Path(rel).stem
            name = REF_ALIASES.get(pascal_case(stem), pascal_case(stem))
            names.add(name)
        elif 'oneOf' in schema and is_tool_response_schema(rel):
            stem = Path(rel).stem
            name = REF_ALIASES.get(pascal_case(stem), pascal_case(stem))
            names.add(name)
    for name, spec in INLINE_SCHEMA_TYPES.items():
        try:
            schema = load_schema_spec(spec)
        except SCHEMA_RESOLUTION_ERRORS as e:
            print(f'// Warning: skipped {name} ({spec}: {e})', file=sys.stderr)
            continue
        if schema_properties(schema):
            names.add(name)
    for name in supported_union_schemas(skip_names=KNOWN_TYPES):
        names.add(name)
    _WILL_GENERATE_CACHE = names
    return names

def resolve_allof_go_type_info(branches, required=False):
    """Resolve allOf when the composition has a lossless Go field type.

    The common schema pattern is `$ref` plus validation-only constraints (for
    example `not.required`). In that case the SDK field can use the referenced
    Go type without inventing a second wrapper type. Compositions that add
    inline properties still fall back to `any` unless they are explicitly named
    through INLINE_SCHEMA_TYPES/INLINE_TYPE_HINTS.
    """
    if len(branches) == 1 and isinstance(branches[0], dict):
        return resolve_go_type_info(branches[0], required)

    ref_types = []
    ref_property_names = set()
    structural_branches = []
    for branch in branches:
        if not isinstance(branch, dict):
            return 'any', 'unsupported_allOf'
        if '$ref' in branch:
            branch_extra = {
                key: value for key, value in branch.items()
                if key not in ('$ref', 'description', 'title')
            }
            if schema_properties(branch_extra):
                return 'any', 'unsupported_allOf'
            go_type, reason = resolve_go_type_info(branch, required)
            if reason:
                return 'any', f'allOf_ref:{reason}'
            ref_types.append(go_type)
            ref_schema = resolve_ref_schema(branch['$ref'])
            if ref_schema:
                ref_property_names.update(schema_properties(ref_schema).keys())
            continue
        if schema_properties(branch):
            structural_branches.append(branch)

    if len(ref_types) == 1 and not structural_branches:
        return ref_types[0], None

    if len(ref_types) == 1 and structural_branches:
        validation_only = True
        for branch in structural_branches:
            branch_props = set(schema_properties(branch).keys())
            if not branch_props:
                continue
            if not branch_props.issubset(ref_property_names):
                validation_only = False
                break
        if validation_only:
            return ref_types[0], None

    if not ref_types and len(structural_branches) == 1:
        return resolve_go_type_info(structural_branches[0], required)

    return 'any', 'unsupported_allOf'

def resolve_go_type_info(prop, required=False):
    """Resolve a JSON schema property to a Go type string and fallback reason.

    The reason is None when the type is fully represented. When the generated Go
    type contains `any`, the reason explains why the generator fell back.
    """
    if '$ref' in prop:
        ref = prop['$ref']
        if is_enum_ref(ref):
            return ref_to_go_name(ref), None
        ref_schema = resolve_ref_schema(ref)
        if isinstance(ref_schema, dict) and ref_schema.get('type') == 'string':
            return 'string', None
        name = ref_to_go_name(ref)
        # Error conflicts with the Error function in errors.go
        if name == 'Error':
            return 'AdcpError', 'adcp_error_alias'
        # Apply aliases for schema names that don't match Go type names
        name = REF_ALIASES.get(name, name)
        if name in ('string', 'int', 'float64', 'bool', 'any'):
            reason = 'ref_alias_any' if name == 'any' else None
            return name, reason
        # Resolve if the type is hand-written (KNOWN_TYPES) or will be
        # emitted from one of the registered schema lists in this run.
        if name in KNOWN_TYPES or name in _will_generate_set():
            return name, None
        return 'any', f'unknown_ref:{ref}'  # Avoid undefined type errors

    if 'allOf' in prop:
        return resolve_allof_go_type_info(prop.get('allOf', []), required)

    typ = prop.get('type', '')
    if isinstance(typ, list):
        non_null_types = [t for t in typ if t != 'null']
        if len(non_null_types) == 1:
            nested = dict(prop)
            nested['type'] = non_null_types[0]
            go_type, reason = resolve_go_type_info(nested, required)
            if not (
                go_type.startswith('*') or
                go_type.startswith('[]') or
                go_type.startswith('map[')
            ):
                go_type = f'*{go_type}'
            return go_type, reason
        return 'any', 'union'

    if typ == 'string':
        return 'string', None
    elif typ == 'integer':
        return 'int', None
    elif typ == 'number':
        return 'float64', None
    elif typ == 'boolean':
        return 'bool', None
    elif typ == 'array':
        items = prop.get('items', {})
        if isinstance(items, dict):
            item_type, reason = resolve_go_type_info(items)
            return f'[]{item_type}', f'array_item:{reason}' if reason else None
        return '[]any', 'array_missing_items'
    elif typ == 'object':
        # Check for additionalProperties (map type)
        addl = prop.get('additionalProperties')
        if isinstance(addl, dict) and addl:
            val_type, reason = resolve_go_type_info(addl)
            return f'map[string]{val_type}', f'map_value:{reason}' if reason else None
        # Check if it has properties (structured object)
        if 'properties' in prop:
            return 'any', 'inline_object'
        return 'map[string]any', 'freeform_object'
    elif 'oneOf' in prop or 'anyOf' in prop:
        return 'any', 'union'
    elif 'const' in prop:
        return 'string', None
    else:
        return 'any', 'unspecified_schema_type'


def resolve_go_type(prop, required=False):
    """Resolve a JSON schema property to a Go type string."""
    go_type, _ = resolve_go_type_info(prop, required)
    return go_type


def is_any_type(go_type):
    """True if a generated type string includes Go's dynamic `any`."""
    return (
        go_type == 'any' or
        go_type == '[]any' or
        go_type == 'map[string]any' or
        go_type.endswith(']any') or
        '[]any' in go_type
    )


def contains_dynamic_any(go_type):
    """True if a generated type uses any directly or through an alias."""
    return is_any_type(go_type) or 'AdcpError' in go_type


def should_pointer_optional_type(go_type):
    """True when an optional zero-value field needs a pointer to omit cleanly."""
    if go_type.startswith('*') or go_type.startswith('[]') or go_type.startswith('map['):
        return False
    if go_type in ('string', 'int', 'float64', 'bool', 'any', 'AdcpError'):
        return False
    return True


def field_go_type_info(type_name, json_name, prop, required_set):
    """Return the generated field type and fallback reason for a struct field."""
    hint_key = (type_name, json_name)
    has_hint = hint_key in INLINE_TYPE_HINTS
    direct_enum_ref = not has_hint and '$ref' in prop and is_enum_ref(prop['$ref'])
    if has_hint:
        hint_type = INLINE_TYPE_HINTS[hint_key]
        if prop.get('type', '') == 'array':
            go_type = f'[]{hint_type}'
        else:
            go_type = hint_type
        reason = 'inline_type_hint' if contains_dynamic_any(go_type) else None
    else:
        go_type, reason = resolve_go_type_info(prop, json_name in required_set)

    is_required = json_name in required_set
    if not is_required and go_type == 'bool':
        go_type = '*bool'
    elif not is_required and should_pointer_optional_type(go_type) and not direct_enum_ref:
        go_type = f'*{go_type}'
    return go_type, reason

def schema_to_struct(name, schema):
    """Convert a JSON schema to a Go struct definition string."""
    props = schema_properties(schema)
    required_set = schema_required_names(schema)

    fields = []
    for json_name, prop in props.items():
        if not isinstance(prop, dict):
            continue

        go_name = pascal_case(json_name)

        go_type, _ = field_go_type_info(name, json_name, prop, required_set)
        is_required = json_name in required_set

        omit = 'omitempty' if not is_required else ''
        tag = f'`json:"{json_name}'
        if omit:
            tag += f',{omit}'
        tag += '"`'

        desc = safe_comment(field_description(name, json_name, prop), 80)
        comment = f' // {desc}' if desc else ''

        deprecated = deprecated_comment(prop)
        if deprecated:
            fields.append(f'\t// {deprecated}')
        fields.append(f'\t{go_name} {go_type} {tag}{comment}')

    desc = safe_comment(
        INLINE_SCHEMA_DESCRIPTIONS.get(name, '') or schema.get('description', ''),
        100,
    )
    doc = f'// {name} — {desc}\n' if desc else ''
    return f'{doc}type {name} struct {{\n' + '\n'.join(fields) + '\n}\n'

def is_tool_response_schema(path):
    return Path(path).stem.endswith('-response')

def validate_top_level_tool_union(name, schema, schema_path):
    if not is_tool_response_schema(schema_path):
        raise ValueError(
            f'{name} in {schema_path} is a top-level tool oneOf request; '
            'add explicit request-union generation before emitting it'
        )
    return top_level_union_variants(name, schema, schema_path)

def top_level_union_variants(name, schema, schema_path=None):
    """Return generated variant names for a top-level oneOf schema."""
    variants = []
    seen = set()
    location = f' in {schema_path}' if schema_path else ''
    for idx, variant in enumerate(schema.get('oneOf', [])):
        vname = variant.get('title', '')
        if not vname:
            raise ValueError(
                f'{name}{location} oneOf branch {idx} must have a title '
                'so Go can generate a named variant'
            )
        if not has_struct_fields(variant):
            raise ValueError(
                f'{name}{location} oneOf branch {idx} ({vname}) must define '
                'object properties so Go can generate a variant struct'
            )
        if vname in seen:
            raise ValueError(
                f'{name}{location} oneOf branch {idx} repeats variant {vname}'
            )
        seen.add(vname)
        variants.append(vname)
    return variants

def union_interface_to_type(name, schema, schema_path=None):
    """Generate a closed Go interface for a top-level response union."""
    top_level_union_variants(name, schema, schema_path)
    marker = f'is{name}'
    return '\n'.join([
        f'// {name} is a discriminated union — use one of the generated variant structs.',
        f'type {name} interface {{',
        f'\t{marker}()',
        '}',
    ]) + '\n'

def union_marker_methods(name, schema, schema_path=None):
    """Generate marker methods for the variants of a top-level response union."""
    marker = f'is{name}'
    lines = []
    for vname in top_level_union_variants(name, schema, schema_path):
        lines.append(f'func ({vname}) {marker}() {{}}')
    return '\n'.join(lines) + '\n'

def scalar_or_array_union_to_type(name, schema):
    """Generate a Go helper for a `scalar or []scalar` JSON union."""
    elem_type = scalar_union_go_type(schema)
    if not elem_type:
        raise ValueError(f'{name} is not a supported scalar-or-array union')
    desc = safe_comment(schema.get('description', ''), 100)
    doc = f'// {name} — {desc}\n' if desc else ''
    doc += (
        f'// {name} preserves unknown values for forward compatibility.\n'
        '// It validates only scalar-or-array shape and array cardinality.\n'
    )
    accepts_empty = schema_accepts_empty_array(schema)
    reject_empty = '' if accepts_empty else f'''\tif len(v) == 0 {{
\t\treturn nil, fmt.Errorf("{name} must contain at least one value")
\t}}
'''
    reject_empty_unmarshal = '' if accepts_empty else f'''\tif len(many) == 0 {{
\t\treturn fmt.Errorf("{name} must contain at least one value")
\t}}
'''
    empty_constructor = '' if accepts_empty else f'''\tif len(values) == 0 {{
\t\treturn nil
\t}}
'''
    # append to a zero-length literal keeps the no-arg constructor non-nil.
    constructor_value = f'append({name}{{}}, values...)' if accepts_empty else f'{name}(values)'
    if accepts_empty:
        constructor_doc = (
            f'// New{name} returns a pointer to a {name} containing values.\n'
            f'// Use nil instead of New{name}() when an optional field should be omitted.\n'
            f'// New{name}() returns a non-nil empty slice pointer that marshals as [].\n'
        )
    else:
        constructor_doc = (
            f'// New{name} returns a pointer to a {name} containing values.\n'
            '// It returns nil when called with no values so optional fields omit instead\n'
            '// of triggering a MarshalJSON error on a schema-invalid empty array.\n'
        )
    return f'''{doc}type {name} []{elem_type}

{constructor_doc}\
func New{name}(values ...{elem_type}) *{name} {{
{empty_constructor}\tv := {constructor_value}
\treturn &v
}}

func (v {name}) MarshalJSON() ([]byte, error) {{
{reject_empty}\tif len(v) == 1 {{
\t\treturn json.Marshal(v[0])
\t}}
\treturn json.Marshal([]{elem_type}(v))
}}

func (v *{name}) UnmarshalJSON(data []byte) error {{
\tif string(data) == "null" {{
\t\treturn fmt.Errorf("{name} cannot be null")
\t}}
\tvar single {elem_type}
\tif err := json.Unmarshal(data, &single); err == nil {{
\t\t*v = {name}{{single}}
\t\treturn nil
\t}}
\tvar many []{elem_type}
\tif err := json.Unmarshal(data, &many); err != nil {{
\t\treturn err
\t}}
\t{reject_empty_unmarshal.strip()}
\t*v = {name}(many)
\treturn nil
}}
'''

def supported_union_schemas(skip_names=None):
    """Return configured union helper schemas that this generator can emit."""
    skip_names = set(skip_names or ())
    schemas = OrderedDict()
    for name, specs in UNION_SCHEMA_TYPES.items():
        if name in skip_names:
            continue
        if isinstance(specs, str):
            specs = (specs,)
        primary_schema = None
        try:
            loaded = [(spec, load_schema_spec(spec)) for spec in specs]
        except SCHEMA_RESOLUTION_ERRORS as e:
            raise ValueError(f'{name} has an invalid union schema pointer: {e}') from e
        elem_types = {scalar_union_go_type(schema) for _, schema in loaded}
        if len(elem_types) != 1 or None in elem_types:
            raise ValueError(f'{name} union schemas are not equivalent scalar-or-array unions')
        if len({schema_accepts_empty_array(schema) for _, schema in loaded}) != 1:
            raise ValueError(f'{name} union array constraints differ')
        for spec, schema in loaded:
            description = schema.get('description', '')
            # Prefer the shortest shared helper doc when one field description
            # includes request-specific defaults that do not apply everywhere.
            if primary_schema is None or len(description) < len(primary_schema.get('description', '')):
                primary_schema = schema
        schemas[name] = primary_schema
    return schemas

def enum_members(name, values):
    """Return `(const_name, value)` entries this generator can safely emit."""
    members = []
    seen_const_names = {}
    for v in values:
        if not isinstance(v, str):
            raise ValueError(f'{name} enum value {v!r} is not a string')
        if v == '':
            raise ValueError(f'{name} enum value must not be empty')
        # Replace dots and other invalid chars for Go identifiers
        safe_v = v.replace('.', '_').replace('-', '_').replace(' ', '_')
        const_name = name + pascal_case(safe_v)
        if not re.match(r'^[A-Za-z_][A-Za-z0-9_]*$', const_name):
            raise ValueError(f'{name} enum value {v!r} cannot be converted to a Go identifier')
        if const_name in seen_const_names:
            raise ValueError(
                f'{name} enum values {seen_const_names[const_name]!r} and {v!r} '
                f'both convert to {const_name}'
            )
        seen_const_names[const_name] = v
        members.append((const_name, v))
    return members

def enum_to_type(name, desc, values):
    """Generate a Go enum alias, constants, and opt-in validation helpers."""
    lines = []
    members = enum_members(name, values)

    # Enum overrides are total replacements for schema docs that truncate poorly.
    desc = ENUM_DESCRIPTION_OVERRIDES.get(name, desc)
    lines.append(f'// {name} — {safe_comment(desc, 80)}' if desc else f'// {name} enum values')
    lines.append(f'type {name} = string')
    lines.append('const (')
    for const_name, value in members:
        lines.append(f'\t{const_name} {name} = "{value}"')
    lines.append(')')
    lines.append('')

    constants = ', '.join(const_name for const_name, _ in members)
    lines.append(f'// Known{name}Values returns the current schema-defined values for {name}.')
    lines.append(f'func Known{name}Values() []{name} {{')
    lines.append(f'\treturn []{name}{{{constants}}}')
    lines.append('}')
    lines.append('')

    lines.append(f'// IsKnown{name} reports whether v is one of the current schema-defined {name} values.')
    lines.append('// It is an opt-in strict helper; JSON unmarshalling preserves unknown values.')
    lines.append(f'func IsKnown{name}(v {name}) bool {{')
    lines.append('\tswitch v {')
    if members:
        lines.append(f'\tcase {constants}:')
        lines.append('\t\treturn true')
    lines.append('\tdefault:')
    lines.append('\t\treturn false')
    lines.append('\t}')
    lines.append('}')
    lines.append('')

    lines.append(f'// Parse{name} returns s as {name} when s is one of the current schema-defined values.')
    lines.append('// It is an opt-in strict helper; JSON unmarshalling preserves unknown values.')
    lines.append(f'func Parse{name}(s string) ({name}, error) {{')
    lines.append(f'\tv := {name}(s)')
    lines.append(f'\tif IsKnown{name}(v) {{')
    lines.append('\t\treturn v, nil')
    lines.append('\t}')
    lines.append(f'\treturn "", fmt.Errorf("unknown {name} value")')
    lines.append('}')
    lines.append('')

    return '\n'.join(lines)

def inline_enum_origin(spec):
    schema = spec.get('schema')
    kind = spec.get('one_of_kind')
    prop = spec.get('property')
    return f'{schema} oneOf kind={kind} property={prop}'

def load_inline_enum_schema(spec):
    schema_spec = spec.get('schema')
    kind = spec.get('one_of_kind')
    prop = spec.get('property')
    if not schema_spec or not kind or not prop:
        raise ValueError(f'invalid inline enum config: {spec!r}')

    schema = load_schema_spec(schema_spec)
    matches = []
    for branch in schema.get('oneOf', []):
        props = branch.get('properties', {})
        kind_schema = props.get('kind', {})
        # The resolver intentionally requires exactly one const discriminator
        # match. If upstream splits a kind into sub-variants, add a more precise
        # selector instead of picking one branch implicitly.
        # Single-value enum discriminators are also not accepted here; they fail
        # with a no-match build error until the config shape explicitly supports
        # that selector form.
        if kind_schema.get('const') == kind:
            matches.append(props.get(prop, {}))

    origin = inline_enum_origin(spec)
    if not matches:
        raise ValueError(f'{origin} did not match a oneOf branch')
    if len(matches) > 1:
        raise ValueError(f'{origin} matched multiple oneOf branches')
    return matches[0]

def generate_enums(_seen=None):
    """Generate Go string constants for all enum schemas."""
    lines = []
    seen = dict(_seen or {})

    def append_enum(name, schema, origin, error_if_empty=False):
        values = schema.get('enum', [])
        if not values:
            if error_if_empty:
                raise ValueError(f'{origin} no longer defines enum values for {name}')
            return
        if name in seen:
            raise ValueError(f'duplicate enum type {name}: {seen[name]} and {origin}')
        seen[name] = origin
        desc = schema.get('description', '')
        lines.append(enum_to_type(name, desc, values))

    enum_dir = SCRIPT_DIR / ENUM_DIR
    if enum_dir.exists():
        for f in sorted(enum_dir.iterdir()):
            if not f.suffix == '.json':
                continue
            schema = load_schema(f)
            name = pascal_case(f.stem)
            append_enum(name, schema, str(f.relative_to(SCRIPT_DIR)))

    for name, spec in INLINE_ENUM_TYPES.items():
        try:
            schema = load_inline_enum_schema(spec)
        except ValueError as e:
            raise ValueError(f'INLINE_ENUM_TYPES[{name!r}]: {e}') from e
        origin = inline_enum_origin(spec)
        append_enum(name, schema, origin, error_if_empty=True)

    return '\n'.join(lines)


def generated_schema_entries():
    """Yield generated schema entries in the same ownership order as generate()."""
    generated = set(KNOWN_TYPES)

    for section, paths in (
        ('core', CORE_SCHEMAS),
        ('support', SUPPORT_SCHEMAS),
        ('auto_ref', auto_ref_schema_paths()),
    ):
        for path in paths:
            if not schema_exists(path):
                continue
            schema = load_schema(path)
            name = REF_ALIASES.get(pascal_case(Path(path).stem),
                                   pascal_case(Path(path).stem))
            if name in generated:
                continue
            generated.add(name)
            if has_struct_fields(schema):
                yield {
                    'section': section,
                    'name': name,
                    'schema': path,
                    'schema_obj': schema,
                    'kind': 'struct',
                }

    for name, spec in INLINE_SCHEMA_TYPES.items():
        if name in generated:
            continue
        generated.add(name)
        try:
            schema = load_schema_spec(spec)
        except SCHEMA_RESOLUTION_ERRORS:
            continue
        if has_struct_fields(schema):
            yield {
                'section': 'inline',
                'name': name,
                'schema': spec,
                'schema_obj': schema,
                'kind': 'struct',
            }

    for section, paths in (
        ('tool', TOOL_SCHEMAS),
        ('webhook', WEBHOOK_SCHEMAS),
    ):
        for path in paths:
            if not schema_exists(path):
                continue
            schema = load_schema(path)
            name = REF_ALIASES.get(pascal_case(Path(path).stem),
                                   pascal_case(Path(path).stem))
            if name in generated:
                continue
            generated.add(name)

            if section == 'tool' and 'oneOf' in schema:
                validate_top_level_tool_union(name, schema, path)
                yield {
                    'section': section,
                    'name': name,
                    'schema': path,
                    'schema_obj': schema,
                    'kind': 'union_interface',
                }
                for idx, variant in enumerate(schema['oneOf']):
                    vname = variant.get('title', '')
                    if vname and vname not in generated and has_struct_fields(variant):
                        generated.add(vname)
                        yield {
                            'section': section,
                            'name': vname,
                            'schema': f'{path}#/oneOf/{idx}',
                            'variant': vname,
                            'schema_obj': variant,
                            'kind': 'struct',
                        }
                continue

            if has_struct_fields(schema):
                yield {
                    'section': section,
                    'name': name,
                    'schema': path,
                    'schema_obj': schema,
                    'kind': 'struct',
                }


def any_allowance(type_name, json_name, go_type, reason):
    """Return an allowlist explanation for intentional `any`, or None."""
    if json_name in INTENTIONAL_ANY_FIELD_NAMES:
        return f'intentional {json_name} escape hatch'
    if (type_name, json_name) in INTENTIONAL_ANY_FIELDS:
        return INTENTIONAL_ANY_FIELDS[(type_name, json_name)]
    if (type_name, json_name) in SCHEMA_32_BETA_ANY_FIELDS:
        return 'known 3.2 beta generator compatibility gap'
    if 'AdcpError' in go_type:
        return 'AdCP error payload is intentionally open'
    return None


def any_coverage_report():
    """Return a structured report of generated `any` fallbacks."""
    records = []
    for entry in generated_schema_entries():
        if entry['kind'] == 'union_interface':
            continue

        schema = entry['schema_obj']
        props = schema_properties(schema)
        required_set = schema_required_names(schema)
        for json_name, prop in props.items():
            if not isinstance(prop, dict):
                continue
            go_type, reason = field_go_type_info(
                entry['name'], json_name, prop, required_set,
            )
            if not contains_dynamic_any(go_type):
                continue
            allowance = any_allowance(entry['name'], json_name, go_type, reason)
            records.append({
                'type': entry['name'],
                'section': entry['section'],
                'schema': entry['schema'],
                'variant': entry.get('variant'),
                'field': pascal_case(json_name),
                'json': json_name,
                'go_type': go_type,
                'reason': reason or 'unknown',
                'allowed': allowance is not None,
                'allowance': allowance,
            })

    by_reason = {}
    by_section = {}
    for record in records:
        by_reason[record['reason']] = by_reason.get(record['reason'], 0) + 1
        by_section[record['section']] = by_section.get(record['section'], 0) + 1

    return {
        'total_any': len(records),
        'allowed_any': sum(1 for r in records if r['allowed']),
        'unreviewed_any': sum(1 for r in records if not r['allowed']),
        'by_reason': dict(sorted(by_reason.items())),
        'by_section': dict(sorted(by_section.items())),
        'records': records,
    }


def print_any_coverage_summary(report):
    print(
        'Generated any coverage: '
        f'{report["total_any"]} total, '
        f'{report["allowed_any"]} allowed, '
        f'{report["unreviewed_any"]} unreviewed'
    )
    print()
    print('By reason:')
    for reason, count in report['by_reason'].items():
        print(f'  {reason}: {count}')
    print()
    print('Unreviewed generated any fields:')
    for record in report['records']:
        if record['allowed']:
            continue
        if record['field']:
            print(
                f'  {record["type"]}.{record["field"]} '
                f'({record["json"]}) -> {record["go_type"]} '
                f'[{record["reason"]}] '
                f'{record["schema"]}'
            )
        else:
            print(
                f'  {record["type"]} -> {record["go_type"]} '
                f'[{record["reason"]}] '
                f'{record["schema"]}'
            )

def generate():
    """Main generation function."""
    _reset_will_generate_cache()
    # Read pinned version
    version = "unknown"
    try:
        with open(SCRIPT_DIR / 'VERSION') as f:
            version = f.read().strip()
    except FileNotFoundError:
        pass

    print('// Code generated by generate.py from AdCP JSON schemas. DO NOT EDIT.')
    print(f'// AdCP schema version: {version}')
    print('// Source: https://github.com/adcontextprotocol/adcp/tree/main/static/schemas/source')
    print()
    print('package adcp')
    print()
    union_schemas = supported_union_schemas(skip_names=KNOWN_TYPES)
    enums = generate_enums()
    imports = []
    if union_schemas:
        imports.append('encoding/json')
    if union_schemas or enums:
        imports.append('fmt')
    if imports:
        print('import (')
        for pkg in imports:
            print(f'\t"{pkg}"')
        print(')')
        print()

    # Type aliases for $ref targets not in our core generation list
    print('// Type aliases for $ref targets from schemas not directly generated.')
    print('type AdcpError = map[string]any')
    print()

    if enums:
        print('// --- Enum types ---')
        print()
        print(enums)

    # Use KNOWN_TYPES as the skip set — single source of truth
    generated = set(KNOWN_TYPES)

    # Generate narrow union helper types.
    print('// --- Union helper types ---')
    print()
    for name, schema in union_schemas.items():
        if name in generated:
            continue
        generated.add(name)
        print(scalar_or_array_union_to_type(name, schema))

    # Generate core types
    print('// --- Core types ---')
    print()
    for path in CORE_SCHEMAS:
        if not schema_exists(path):
            print(f'// Skipped {path} (not found)', file=sys.stderr)
            continue
        schema = load_schema(path)
        name = REF_ALIASES.get(pascal_case(Path(path).stem),
                               pascal_case(Path(path).stem))
        if name in generated:
            continue
        generated.add(name)
        if has_struct_fields(schema):
            print(schema_to_struct(name, schema))

    # Generate support/helper schema types.
    print('// --- Support schema types ---')
    print()
    for path in SUPPORT_SCHEMAS:
        if not schema_exists(path):
            print(f'// Skipped {path} (not found)', file=sys.stderr)
            continue
        schema = load_schema(path)
        name = REF_ALIASES.get(pascal_case(Path(path).stem),
                               pascal_case(Path(path).stem))
        if name in generated:
            continue
        generated.add(name)
        if has_struct_fields(schema):
            print(schema_to_struct(name, schema))

    # Generate reachable object $ref schema types that are not otherwise
    # hand-curated. This is intentionally limited to object/allOf schemas so
    # broad anyOf/oneOf payload unions remain explicit decisions.
    auto_ref_paths = auto_ref_schema_paths()
    if auto_ref_paths:
        print('// --- Referenced schema types ---')
        print()
    for path in auto_ref_paths:
        if not schema_exists(path):
            print(f'// Skipped {path} (not found)', file=sys.stderr)
            continue
        schema = load_schema(path)
        name = REF_ALIASES.get(pascal_case(Path(path).stem),
                               pascal_case(Path(path).stem))
        if name in generated:
            continue
        generated.add(name)
        if has_struct_fields(schema):
            print(schema_to_struct(name, schema))

    # Generate named inline schema-pointer types.
    print('// --- Inline schema types ---')
    print()
    for name, spec in INLINE_SCHEMA_TYPES.items():
        if name in generated:
            continue
        generated.add(name)
        try:
            schema = load_schema_spec(spec)
        except SCHEMA_RESOLUTION_ERRORS as e:
            print(f'// Skipped {name} ({spec}: {e})', file=sys.stderr)
            continue
        if schema_properties(schema):
            print(schema_to_struct(name, schema))

    # Generate tool request/response types
    print('// --- Tool request/response types ---')
    print()
    for path in TOOL_SCHEMAS:
        if not schema_exists(path):
            print(f'// Skipped {path} (not found)', file=sys.stderr)
            continue
        schema = load_schema(path)

        # Derive name from path: media-buy/get-products-request.json -> GetProductsRequest
        stem = Path(path).stem  # get-products-request
        name = REF_ALIASES.get(pascal_case(stem), pascal_case(stem))
        if name in generated:
            continue
        generated.add(name)

        # Handle oneOf at top level (like preview-creative-response.json)
        if 'oneOf' in schema:
            validate_top_level_tool_union(name, schema, path)
            print(union_interface_to_type(name, schema, path))
            # Generate each variant
            for variant in schema['oneOf']:
                vname = variant.get('title', '')
                if vname and vname not in generated:
                    generated.add(vname)
                    if has_struct_fields(variant):
                        print(schema_to_struct(vname, variant))
            print(union_marker_methods(name, schema, path))
            continue

        if has_struct_fields(schema):
            print(schema_to_struct(name, schema))

    # Generate webhook payload types and their IdempotencyKeyPtr methods.
    # The method satisfies adcp/webhook.Payload so webhook.Marshal / Deliver
    # can fill a UUIDv4 key when the caller leaves IdempotencyKey empty.
    # Generating the method alongside the struct prevents drift — adding a
    # new webhook schema to WEBHOOK_SCHEMAS automatically extends the set of
    # types that implement Payload.
    print('// --- Webhook payload types ---')
    print()
    webhook_type_names = []
    for path in WEBHOOK_SCHEMAS:
        if not schema_exists(path):
            print(f'// Skipped {path} (not found)', file=sys.stderr)
            continue
        schema = load_schema(path)
        name = pascal_case(Path(path).stem)
        if name in generated:
            continue
        generated.add(name)
        if has_struct_fields(schema):
            # Webhook payloads must carry a required idempotency_key field
            # (adcontextprotocol/adcp#2417). Refuse to emit a method that
            # references a field the schema did not declare.
            if 'idempotency_key' not in schema_properties(schema):
                print(f'// {name}: WARNING: schema has no idempotency_key — IdempotencyKeyPtr method NOT generated', file=sys.stderr)
                print(schema_to_struct(name, schema))
                continue
            print(schema_to_struct(name, schema))
            webhook_type_names.append(name)

    if webhook_type_names:
        print('// --- Webhook Payload interface satisfaction ---')
        print('// IdempotencyKeyPtr returns a writable pointer to the payload\'s idempotency_key field')
        print('// so webhook.Marshal can fill a UUIDv4 key when the caller leaves it empty.')
        print('// Spec: adcontextprotocol/adcp#2417.')
        for name in webhook_type_names:
            print(f'func (p *{name}) IdempotencyKeyPtr() *string {{ return &p.IdempotencyKey }}')
        print()

def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument(
        '--coverage-json',
        action='store_true',
        help='emit JSON report of generated any fallbacks instead of Go code',
    )
    parser.add_argument(
        '--coverage-summary',
        action='store_true',
        help='emit human-readable report of generated any fallbacks instead of Go code',
    )
    parser.add_argument(
        '--coverage-max-unreviewed-any',
        type=int,
        metavar='N',
        help='fail if generated unreviewed any fallbacks exceed N',
    )
    args = parser.parse_args(argv)

    if (
        args.coverage_json or
        args.coverage_summary or
        args.coverage_max_unreviewed_any is not None
    ):
        _reset_will_generate_cache()
        supported_union_schemas(skip_names=KNOWN_TYPES)
        report = any_coverage_report()
        if args.coverage_json:
            print(json.dumps(report, indent=2))
        else:
            print_any_coverage_summary(report)
        if (
            args.coverage_max_unreviewed_any is not None and
            report['unreviewed_any'] > args.coverage_max_unreviewed_any
        ):
            print(
                'Generated unreviewed any fallbacks exceed baseline: '
                f'{report["unreviewed_any"]} > '
                f'{args.coverage_max_unreviewed_any}',
                file=sys.stderr,
            )
            return 1
        return 0

    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        generate()
    source = buf.getvalue()
    try:
        result = subprocess.run(
            ['gofmt'],
            input=source,
            text=True,
            capture_output=True,
            check=True,
        )
    except OSError:
        print(source, end='')
    except subprocess.CalledProcessError as exc:
        if exc.stderr:
            print(exc.stderr, file=sys.stderr, end='')
        print('gofmt failed while formatting generated Go', file=sys.stderr)
        return 1
    else:
        print(result.stdout, end='')
    return 0


if __name__ == '__main__':
    sys.exit(main())
