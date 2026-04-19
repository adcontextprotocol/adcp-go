#!/usr/bin/env python3
"""Generate Go types from AdCP JSON schemas.

Usage:
    python3 generate.py > ../types_gen.go

Reads all JSON schemas from the current directory (downloaded from
adcontextprotocol/adcp/static/schemas/source/) and generates Go structs
with proper json tags matching the wire format.
"""

import json
import os
import re
import sys
from pathlib import Path
from collections import OrderedDict

# Types that exist in hand-written files or will be generated.
# $ref targets not in this set get replaced with `any`.
KNOWN_TYPES = {
    # From types.go (hand-written)
    'CapabilitiesData', 'ADCPVersion', 'BrandReference', 'AccountReference',
    'AccountResult', 'AccountSetup', 'GovernanceResult', 'GovernanceAccount',
    'GovernanceAgent', 'ProductsData', 'Product', 'FormatRef', 'PricingOption',
    'Targeting', 'GeoTarget', 'CreativeSpec', 'MediaBuyData', 'Package',
    'MediaBuyListItem', 'DeliveryData', 'ReportingPeriod', 'MediaBuyDelivery',
    'DeliveryTotals', 'PackageDelivery', 'CreativeFormatID', 'CreativeFormat',
    'Render', 'AssetSlot', 'CreativeResult', 'CreativeListItem', 'PreviewResult',
    'Preview', 'PreviewRender', 'BuildCreativeResult', 'Signal', 'SignalID',
    'SignalPricing', 'Deployment', 'ActivationKey', 'CatalogResult',
    'EventSourceResult', 'LogEventResult',
    # From inputs.go (hand-written types that need custom Go code)
    'EmptyInput', 'PackageInput',
    'AccountInput', 'GovernanceAccountInput',
    'CreativeInput', 'CatalogInput', 'EventSourceInput', 'DestinationInput',
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
    'CollectionListRef', 'CreativeConsumption', 'IndustryIdentifier',
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
    'ComplianceTestingCapabilities',
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

# Core types that tools reference via $ref
CORE_SCHEMAS = [
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
    "core/brand-ref.json",
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

# Map $ref schema names to Go type names when the schema filename doesn't match
# the Go type name (e.g., brand-ref.json -> BrandReference, not BrandRef).
REF_ALIASES = {
    'BrandRef': 'BrandReference',
    'AccountRef': 'AccountReference',
    'PackageRequest': 'PackageInput',
    'FormatID': 'FormatRef',
    'SignalDefinition': 'Signal',
    'SignalPricingOption': 'SignalPricing',
    'StartTiming': 'string',  # start_time is a string or "asap"
    'AccountInput': 'AccountInput',
    'GovernanceAccountInput': 'GovernanceAccountInput',
    'CreativeInput': 'CreativeInput',
    'DestinationInput': 'DestinationInput',
}

# Inline array item type hints: when a request schema has an array property whose
# items are inline objects (no $ref), map (struct_name, field) -> Go item type.
INLINE_TYPE_HINTS = {
    ('SyncAccountsRequest', 'accounts'): 'AccountInput',
    ('SyncGovernanceRequest', 'accounts'): 'GovernanceAccountInput',
    ('SyncCreativesRequest', 'creatives'): 'CreativeInput',
    ('ListCreativesRequest', 'filters'): 'CreativeFilters',
    ('SyncCatalogsRequest', 'catalogs'): 'CatalogInput',
    ('SyncEventSourcesRequest', 'event_sources'): 'EventSourceInput',
    ('LogEventRequest', 'events'): 'map[string]any',
    ('ActivateSignalRequest', 'destinations'): 'DestinationInput',
    ('GetSignalsRequest', 'filters'): 'SignalFilters',
}

# Enum schemas
ENUM_DIR = "enums"

def safe_comment(text, max_len=80):
    """Sanitize text for embedding in a Go // comment. Strips newlines to
    prevent code injection via schema descriptions."""
    return text.replace('\n', ' ').replace('\r', '')[:max_len] if text else ''

def load_schema(path):
    """Load a JSON schema file, preserving property order."""
    with open(path) as f:
        # Use object_pairs_hook to preserve key order
        return json.load(f, object_pairs_hook=OrderedDict)

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
    """Convert kebab-case or snake_case to PascalCase."""
    parts = re.split(r'[-_]', s)
    result = []
    acronyms = {'id', 'url', 'uri', 'api', 'http', 'html', 'css', 'json', 'xml', 'uid', 'ip', 'rid', 'cpm', 'cpc', 'cpa'}
    for p in parts:
        if p.lower() in acronyms:
            result.append(p.upper())
        else:
            result.append(p[0].upper() + p[1:] if p else '')
    return ''.join(result)

def is_enum_ref(ref):
    """Check if a $ref points to an enum schema."""
    return '/enums/' in ref

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
        # If the referenced type won't exist in the generated output, use any
        if name in KNOWN_TYPES:
            return name
        return 'any'  # Unknown $ref target — avoid undefined type errors

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
    props = schema.get('properties', {})
    required_set = set(schema.get('required', []))

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
    enum_dir = Path(ENUM_DIR)
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
    # Read pinned version
    version = "unknown"
    try:
        with open('VERSION') as f:
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
        if not os.path.exists(path):
            print(f'// Skipped {path} (not found)', file=sys.stderr)
            continue
        schema = load_schema(path)
        name = pascal_case(Path(path).stem)
        if name in generated:
            continue
        generated.add(name)
        if schema.get('type') == 'object' and 'properties' in schema:
            print(schema_to_struct(name, schema))

    # Generate tool request/response types
    print('// --- Tool request/response types ---')
    print()
    for path in TOOL_SCHEMAS:
        if not os.path.exists(path):
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

if __name__ == '__main__':
    generate()
