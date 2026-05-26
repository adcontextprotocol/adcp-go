#!/usr/bin/env python3
"""Generate Go types from AdCP JSON schemas.

Usage:
    python3 generate.py > ../types_gen.go

Reads all JSON schemas from the current directory (downloaded from
adcontextprotocol/adcp/static/schemas/source/) and generates Go structs
with proper json tags matching the wire format.
"""

import argparse
import json
import re
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
#   - schema is oneOf with union fields (generator produces `type X = any`)
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
    # oneOf schemas — generator produces `type X = any`; hand-writing flattens
    # the union into a single struct with all variant fields.
    'PricingOption', 'Deployment', 'PublisherPropertySelector',
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
    "core/data-provider-signal-selector.json",
    "core/placement.json",
    "core/delivery-forecast.json",
    "core/forecast-point.json",
    "core/forecast-range.json",
    "core/proposal.json",
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
    "core/signal-id.json",
    "core/signal-pricing-option.json",
    "core/signal-pricing.json",
    "core/delivery-metrics.json",
    "core/account.json",
    "core/account-ref.json",
    "core/targeting.json",
    "core/context.json",
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
    "core/ad-inventory-config.json",
    "core/installment-deadlines.json",
    "core/material-deadline.json",
    "core/duration.json",
]

# Schemas that are not standalone tool requests/responses, but are important
# SDK input shapes referenced by tool schemas.
SUPPORT_SCHEMAS = [
    "media-buy/package-request.json",
    "media-buy/package-update.json",
]

# Named types generated from inline JSON Schema pointers. This is the first step
# toward making the Go SDK generator own composed/nested shapes instead of
# relying on hand-written approximations.
INLINE_SCHEMA_TYPES = OrderedDict([
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
        "GeoPostalAreaTarget",
        "core/targeting.json#/properties/geo_postal_areas/items",
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
        "CreativeAgentRef",
        "media-buy/list-creative-formats-response.json#/properties/creative_agents/items",
    ),
    (
        "BuildCreativePreviewInput",
        "media-buy/build-creative-request.json#/properties/preview_inputs/items",
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
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_catalog_item/items",
    ),
    (
        "PackageCreativeDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_creative/items",
    ),
    (
        "PackageKeywordDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_keyword/items",
    ),
    (
        "PackageGeoDelivery",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_geo/items",
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
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/by_placement/items",
    ),
    (
        "PackageDailyBreakdown",
        "media-buy/get-media-buy-delivery-response.json"
        "#/properties/media_buy_deliveries/items/properties/by_package/items"
        "/allOf/1/properties/daily_breakdown/items",
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
    ('SyncPlansRequest', 'plans'): 'Plan',
    ('GetProductsResponse', 'proposals'): 'Proposal',
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
    ('Account', 'credit_limit'): '*AccountCreditLimit',
    ('Account', 'governance_agents'): 'AccountGovernanceAgent',
    ('Account', 'reporting_bucket'): '*ReportingBucket',
    ('Targeting', 'geo_metros'): 'GeoMetroTarget',
    ('Targeting', 'geo_metros_exclude'): 'GeoMetroTarget',
    ('Targeting', 'geo_postal_areas'): 'GeoPostalAreaTarget',
    ('Targeting', 'geo_postal_areas_exclude'): 'GeoPostalAreaTarget',
    ('Targeting', 'age_restriction'): '*AgeRestriction',
    ('Targeting', 'keyword_targets'): 'KeywordTarget',
    ('Targeting', 'negative_keywords'): 'NegativeKeywordTarget',
    ('BusinessEntity', 'address'): '*BusinessAddress',
    ('BusinessEntity', 'contacts'): 'BusinessContact',
    ('BusinessEntity', 'bank'): '*BankAccount',
    ('PushNotificationConfig', 'authentication'): '*LegacyWebhookAuthentication',
    ('ReportingWebhook', 'authentication'): 'LegacyWebhookAuthentication',
    ('Package', 'cancellation'): '*PackageCancellation',
    ('CreativeFormat', 'accessibility'): '*CreativeFormatAccessibility',
    ('Signal', 'range'): '*SignalRange',
    ('DeliveryTotals', 'quartile_data'): '*DeliveryQuartileData',
    ('MediaBuyDeliveryTotals', 'quartile_data'): '*DeliveryQuartileData',
    ('PackageDelivery', 'quartile_data'): '*DeliveryQuartileData',
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
    ('ListCreativeFormatsResponse', 'creative_agents'): 'CreativeAgentRef',
    ('BuildCreativeRequest', 'preview_inputs'): 'BuildCreativePreviewInput',
    ('GetMediaBuysRequest', 'status_filter'): '*MediaBuyStatusFilter',
    ('GetMediaBuyDeliveryRequest', 'status_filter'): '*MediaBuyStatusFilter',
    ('GetCollectionListRequest', 'pagination'): '*CollectionRequestPagination',
    ('CollectionListChangedWebhook', 'change_summary'): '*CollectionChangeSummary',
    ('PropertyListChangedWebhook', 'change_summary'): '*PropertyChangeSummary',
    ('RightsConstraint', 'rights_agent'): 'RightsAgentRef',
    ('Product', 'product_card'): '*ProductCard',
    ('Product', 'product_card_detailed'): '*ProductCardDetailed',
    ('CreativeFormat', 'format_card'): '*CreativeFormatCard',
    ('CreativeFormat', 'format_card_detailed'): '*CreativeFormatCardDetailed',
    ('CreativeAsset', 'provenance'): '*Provenance',
    ('CreativeManifest', 'provenance'): '*Provenance',
    ('Provenance', 'ai_tool'): '*ProvenanceAITool',
    ('Provenance', 'declared_by'): '*ProvenanceDeclaredBy',
    ('Provenance', 'c2pa'): '*ProvenanceC2PA',
    ('Provenance', 'disclosure'): '*ProvenanceDisclosure',
    ('Provenance', 'verification'): 'ProvenanceVerification',
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
    ('MediaBuyDelivery', 'daily_breakdown'): 'MediaBuyDailyBreakdown',
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
    # format.json: renders[] and assets[] are oneOf items. Map to hand-written
    # Render/AssetSlot so reference-agent code can keep using typed literals.
    ('CreativeFormat', 'renders'): 'Render',
    ('CreativeFormat', 'assets'): 'AssetSlot',
    ('GetMediaBuysResponse', 'media_buys'): 'MediaBuyData',
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
):
    INLINE_TYPE_HINTS.update({
        (_delivery_metric_type, 'by_event_type'): 'DeliveryEventTypeMetrics',
        (_delivery_metric_type, 'quartile_data'): '*DeliveryQuartileData',
        (_delivery_metric_type, 'dooh_metrics'): '*DeliveryDOOHMetrics',
        (_delivery_metric_type, 'viewability'): '*DeliveryViewability',
        (_delivery_metric_type, 'by_action_source'): 'DeliveryActionSourceMetrics',
    })

# Initial allowlist for generated `any` fallbacks that are intentional protocol
# escape hatches rather than generator gaps. The coverage report still includes
# them, but marks them as allowed so CI can later fail only on unreviewed `any`.
INTENTIONAL_ANY_FIELD_NAMES = {
    'context',
    'ext',
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
    ('SimulationSuccess', 'simulated'): 'test-controller simulation payload is scenario-specific',
    ('SimulationSuccess', 'cumulative'): 'test-controller cumulative state is scenario-specific',
    ('CheckGovernanceRequest', 'payload'): 'governance can evaluate different protocol payloads',
}

# Enum schemas
ENUM_DIR = "enums"

def safe_comment(text, max_len=80):
    """Sanitize text for embedding in a Go // comment. Strips newlines to
    prevent code injection via schema descriptions."""
    return text.replace('\n', ' ').replace('\r', '')[:max_len].rstrip() if text else ''

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
    m = re.match(r'^/schemas/[^/]+/(.+\.json)(#.*)?$', ref)
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
    acronyms = {'id', 'url', 'uri', 'api', 'http', 'html', 'css', 'json', 'xml', 'uid', 'ip', 'rid', 'cpm', 'cpc', 'cpa', 'mcp', 'ai', 'c2pa'}
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

def _reset_will_generate_cache():
    """Clear the cache so the next `_will_generate_set()` call rebuilds from
    current CORE_SCHEMAS/TOOL_SCHEMAS/WEBHOOK_SCHEMAS. Called at the top of
    `generate()` to guarantee correctness when the module is used across
    multiple runs (e.g. imported by lint.py then invoked standalone)."""
    global _WILL_GENERATE_CACHE
    _WILL_GENERATE_CACHE = None


def _will_generate_set():
    """Names (after REF_ALIASES) that this generator run will emit as structs.
    Used so `resolve_go_type` can typed-reference a schema-derived type even
    though it hasn't been emitted yet at the moment of the first reference.
    Excludes schemas that will not produce a struct. Core/support oneOf schemas
    with object branches are included because generation flattens their variant
    fields into one struct; top-level tool oneOf schemas still emit `type X =
    any` and are not treated as typed refs."""
    global _WILL_GENERATE_CACHE
    if _WILL_GENERATE_CACHE is not None:
        return _WILL_GENERATE_CACHE
    names = set()
    for rel in CORE_SCHEMAS + SUPPORT_SCHEMAS:
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

def resolve_go_type_info(prop, required=False):
    """Resolve a JSON schema property to a Go type string and fallback reason.

    The reason is None when the type is fully represented. When the generated Go
    type contains `any`, the reason explains why the generator fell back.
    """
    if '$ref' in prop:
        ref = prop['$ref']
        if is_enum_ref(ref):
            return 'string', None  # Enums are strings in Go
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
        branches = prop.get('allOf', [])
        if len(branches) == 1 and isinstance(branches[0], dict):
            return resolve_go_type_info(branches[0], required)
        return 'any', 'unsupported_allOf'

    typ = prop.get('type', '')

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
    if hint_key in INLINE_TYPE_HINTS:
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
    elif not is_required and should_pointer_optional_type(go_type):
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

        desc = safe_comment(prop.get('description', ''), 80)
        comment = f' // {desc}' if desc else ''

        fields.append(f'\t{go_name} {go_type} {tag}{comment}')

    desc = safe_comment(schema.get('description', ''), 100)
    doc = f'// {name} — {desc}\n' if desc else ''
    return f'{doc}type {name} struct {{\n' + '\n'.join(fields) + '\n}\n'

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

def generate_enums():
    """Generate Go string constants for all enum schemas."""
    lines = []
    enum_dir = SCRIPT_DIR / ENUM_DIR
    if not enum_dir.exists():
        return ''

    for f in sorted(enum_dir.iterdir()):
        if not f.suffix == '.json':
            continue
        schema = load_schema(f)
        name = pascal_case(f.stem)
        values = schema.get('enum', [])
        if not values:
            continue

        desc = schema.get('description', '')
        lines.append(enum_to_type(name, desc, values))

    return '\n'.join(lines)


def generated_schema_entries():
    """Yield generated schema entries in the same ownership order as generate()."""
    generated = set(KNOWN_TYPES)

    for section, paths in (
        ('core', CORE_SCHEMAS),
        ('support', SUPPORT_SCHEMAS),
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
            name = pascal_case(Path(path).stem)
            if name in generated:
                continue
            generated.add(name)

            if section == 'tool' and 'oneOf' in schema:
                yield {
                    'section': section,
                    'name': name,
                    'schema': path,
                    'schema_obj': schema,
                    'kind': 'alias_any',
                }
                for idx, variant in enumerate(schema['oneOf']):
                    vname = variant.get('title', '')
                    if vname and vname not in generated and 'properties' in variant:
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

            if schema.get('type') == 'object' and 'properties' in schema:
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
    if 'AdcpError' in go_type:
        return 'AdCP error payload is intentionally open'
    return None


def any_coverage_report():
    """Return a structured report of generated `any` fallbacks."""
    records = []
    for entry in generated_schema_entries():
        if entry['kind'] == 'alias_any':
            records.append({
                'type': entry['name'],
                'section': entry['section'],
                'schema': entry['schema'],
                'field': None,
                'json': None,
                'go_type': 'any',
                'reason': 'top_level_oneOf_alias',
                'allowed': False,
                'allowance': None,
            })
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
        name = pascal_case(stem)
        if name in generated:
            continue
        generated.add(name)

        # Handle oneOf at top level (like preview-creative-response.json)
        if 'oneOf' in schema:
            print(f'// {name} is a discriminated union — use the appropriate variant type.')
            print(f'type {name} = any')
            print()
            # Generate each variant
            for variant in schema['oneOf']:
                vname = variant.get('title', '')
                if vname and vname not in generated:
                    generated.add(vname)
                    if 'properties' in variant:
                        print(schema_to_struct(vname, variant))
            continue

        if schema.get('type') == 'object' and 'properties' in schema:
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
        if schema.get('type') == 'object' and 'properties' in schema:
            # Webhook payloads must carry a required idempotency_key field
            # (adcontextprotocol/adcp#2417). Refuse to emit a method that
            # references a field the schema did not declare.
            if 'idempotency_key' not in schema.get('properties', {}):
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

    generate()
    return 0


if __name__ == '__main__':
    sys.exit(main())
