#!/usr/bin/env python3
"""Generate Go types from AdCP JSON schemas.

Usage:
    python3 generate.py > ../types_gen.go

Reads all JSON schemas from the current directory (downloaded from
adcontextprotocol/adcp/static/schemas/source/) and generates Go structs
with proper json tags matching the wire format.
"""

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
    "core/product.json",
    "core/package.json",
    "core/media-buy.json",
    "core/pricing-option.json",
    "core/format.json",
    "core/format-id.json",
    "core/creative-asset.json",
    "core/creative-manifest.json",
    "core/deployment.json",
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
    "core/event.json",
    "core/performance-feedback.json",
    "core/creative-brief.json",
    "core/cancellation-policy.json",
    "core/collection-list-ref.json",
    "core/creative-consumption.json",
    "core/industry-identifier.json",
    "core/measurement-terms.json",
    "core/measurement-window.json",
    "core/performance-standard.json",
    "core/vendor-pricing-option.json",
    "core/content-rating.json",
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
    ('PackageUpdate', 'keyword_targets_add'): 'KeywordTargetUpdate',
    ('PackageUpdate', 'keyword_targets_remove'): 'KeywordTargetRef',
    ('PackageUpdate', 'negative_keywords_add'): 'KeywordTargetRef',
    ('PackageUpdate', 'negative_keywords_remove'): 'KeywordTargetRef',
    ('MediaBuyDelivery', 'totals'): 'MediaBuyDeliveryTotals',
    ('MediaBuyDelivery', 'by_package'): 'PackageDelivery',
    ('GetMediaBuyDeliveryResponse', 'reporting_period'): 'ReportingPeriod',
    ('GetMediaBuyDeliveryResponse', 'aggregated_totals'): '*DeliveryAggregatedTotals',
    ('GetMediaBuyDeliveryResponse', 'media_buy_deliveries'): 'MediaBuyDelivery',
    # format.json: renders[] and assets[] are oneOf items. Map to hand-written
    # Render/AssetSlot so reference-agent code can keep using typed literals.
    ('CreativeFormat', 'renders'): 'Render',
    ('CreativeFormat', 'assets'): 'AssetSlot',
    ('GetMediaBuysResponse', 'media_buys'): 'MediaBuyData',
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
    acronyms = {'id', 'url', 'uri', 'api', 'http', 'html', 'css', 'json', 'xml', 'uid', 'ip', 'rid', 'cpm', 'cpc', 'cpa', 'mcp'}
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
    Excludes schemas that will not produce a struct (no `properties`, oneOf-
    only, etc.) because a ref to such a name resolves to `any`."""
    global _WILL_GENERATE_CACHE
    if _WILL_GENERATE_CACHE is not None:
        return _WILL_GENERATE_CACHE
    names = set()
    for rel in CORE_SCHEMAS + SUPPORT_SCHEMAS + TOOL_SCHEMAS + WEBHOOK_SCHEMAS:
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
    _WILL_GENERATE_CACHE = names
    return names

def resolve_go_type(prop, required=False):
    """Resolve a JSON schema property to a Go type string."""
    if '$ref' in prop:
        ref = prop['$ref']
        if is_enum_ref(ref):
            return 'string'  # Enums are strings in Go
        name = ref_to_go_name(ref)
        # Error conflicts with the Error function in errors.go
        if name == 'Error':
            return 'AdcpError'
        # Apply aliases for schema names that don't match Go type names
        name = REF_ALIASES.get(name, name)
        if name in ('string', 'int', 'float64', 'bool', 'any'):
            return name
        # Resolve if the type is hand-written (KNOWN_TYPES) or will be
        # emitted from one of the registered schema lists in this run.
        if name in KNOWN_TYPES or name in _will_generate_set():
            return name
        return 'any'  # Unknown $ref target — avoid undefined type errors

    if 'allOf' in prop:
        branches = prop.get('allOf', [])
        if len(branches) == 1 and isinstance(branches[0], dict):
            return resolve_go_type(branches[0], required)
        return 'any'

    typ = prop.get('type', '')

    if typ == 'string':
        return 'string'
    elif typ == 'integer':
        return 'int'
    elif typ == 'number':
        return 'float64'
    elif typ == 'boolean':
        return 'bool'
    elif typ == 'array':
        items = prop.get('items', {})
        if isinstance(items, dict):
            item_type = resolve_go_type(items)
            return f'[]{item_type}'
        return '[]any'
    elif typ == 'object':
        # Check for additionalProperties (map type)
        addl = prop.get('additionalProperties')
        if isinstance(addl, dict) and addl:
            val_type = resolve_go_type(addl)
            return f'map[string]{val_type}'
        # Check if it has properties (structured object)
        if 'properties' in prop:
            return 'any'  # Inline objects become any — we'd need named types for these
        return 'map[string]any'
    elif 'oneOf' in prop or 'anyOf' in prop:
        return 'any'  # Union types
    elif 'const' in prop:
        return 'string'
    else:
        return 'any'

def schema_to_struct(name, schema):
    """Convert a JSON schema to a Go struct definition string."""
    props = schema_properties(schema)
    required_set = schema_required_names(schema)

    fields = []
    for json_name, prop in props.items():
        if not isinstance(prop, dict):
            continue

        go_name = pascal_case(json_name)

        # Check inline array hints before default resolution
        hint_key = (name, json_name)
        if hint_key in INLINE_TYPE_HINTS:
            hint_type = INLINE_TYPE_HINTS[hint_key]
            prop_type = prop.get('type', '')
            if prop_type == 'array':
                go_type = f'[]{hint_type}'
            else:
                go_type = hint_type
        else:
            go_type = resolve_go_type(prop, json_name in required_set)

        is_required = json_name in required_set

        # Use pointer for optional booleans (need to distinguish absent from false)
        # and optional struct references (need to distinguish absent from zero value)
        if not is_required and go_type == 'bool':
            go_type = '*bool'
        elif not is_required and '$ref' in prop and go_type not in ('string', 'any', 'AdcpError') and not go_type.startswith('[]') and not go_type.startswith('map['):
            go_type = f'*{go_type}'

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
        lines.append(f'// {name} — {safe_comment(desc, 80)}' if desc else f'// {name} enum values')
        lines.append(f'type {name} = string')
        lines.append('const (')
        for v in values:
            if isinstance(v, str):
                # Replace dots and other invalid chars for Go identifiers
                safe_v = v.replace('.', '_').replace('-', '_').replace(' ', '_')
                const_name = name + pascal_case(safe_v)
                # Skip if still not a valid Go identifier
                if not re.match(r'^[A-Za-z_][A-Za-z0-9_]*$', const_name):
                    continue
                lines.append(f'\t{const_name} {name} = "{v}"')
        lines.append(')')
        lines.append('')

    return '\n'.join(lines)

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

    # Type aliases for $ref targets not in our core generation list
    print('// Type aliases for $ref targets from schemas not directly generated.')
    print('type AdcpError = map[string]any')
    print()

    # Generate enums
    enums = generate_enums()
    if enums:
        print('// --- Enum types ---')
        print()
        print(enums)

    # Use KNOWN_TYPES as the skip set — single source of truth
    generated = set(KNOWN_TYPES)

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
        if schema.get('type') == 'object' and 'properties' in schema:
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
        if schema_properties(schema):
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

if __name__ == '__main__':
    generate()
