#!/usr/bin/env python3
"""Compare hand-written Go types in adcp/ against their JSON schema counterparts.

For each entry in generate.py's KNOWN_TYPES that corresponds to a schema listed
in CORE_SCHEMAS / TOOL_SCHEMAS / WEBHOOK_SCHEMAS (or an inline item in a tool
response), parse the matching Go struct's json tags and diff them against the
schema's `properties` keys. Exits non-zero if any drift is detected.

Usage:
    python3 lint.py                 # human-readable report
    python3 lint.py --json          # machine-readable report
    python3 lint.py --strict        # exit non-zero on ANY drift
    python3 lint.py --allow-missing # treat missing-in-schema-dir as non-fatal

CI wiring:
    cd adcp/schemas && ./download.sh && python3 lint.py --strict
"""

import argparse
import json
import os
import re
import sys
from collections import OrderedDict
from pathlib import Path

# Reuse config from generate.py — single source of truth for the skip list
# and schema registries. sys.path manipulation keeps this file standalone
# without forcing generate.py to become an importable module.
SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))
import generate as gen  # noqa: E402

ADCP_DIR = SCRIPT_DIR.parent
GO_SOURCE_FILES = [
    ADCP_DIR / 'types.go',
    ADCP_DIR / 'inputs.go',
    ADCP_DIR / 'errors.go',
    ADCP_DIR / 'responses.go',
    ADCP_DIR / 'governance_types.go',
    ADCP_DIR / 'testcontroller.go',
    ADCP_DIR / 'seller.go',
]

# Types we deliberately do not diff against a named schema file:
#   - oneOf union flatteners (Go can't express oneOf natively)
#   - error/helper types that carry no schema
#   - testcontroller types with no schema
#   - response-builder "types" that are actually functions
#   - inline item shapes whose schema is nested under a tool response
EXEMPT = {
    # oneOf union flatteners
    'BrandReference', 'AccountReference', 'ActivationKey', 'SignalID',
    # errors + helpers
    'Error', 'ErrorOptions',
    # testcontroller (no schema)
    'TestControllerStore', 'StateTransition', 'SimulateDeliveryParams',
    'ReportedSpend', 'SimulateBudgetParams', 'SimulationResult',
    'TestControllerError',
    # inputs (agent-specific helpers, schemas are inline in request schemas)
    'EmptyInput', 'PackageInput', 'AccountInput', 'GovernanceAccountInput',
    'CreativeInput', 'CatalogInput', 'EventSourceInput', 'DestinationInput',
    'CreativeFilters', 'SignalFilters',
    # response builders (funcs, not structs — KNOWN_TYPES collision guard)
    'SyncCreativesResponse', 'SyncAccountsResponse', 'GovernanceResponse',
    'ListCreativesResponse', 'PreviewCreativeResponse', 'BuildCreativeResponse',
    'CreativeFormatsResponse', 'SignalsResponse', 'ActivateSignalResponse',
    'SyncCatalogsResponse', 'SyncEventSourcesResponse', 'LogEventResponse',
    'PerformanceFeedbackResponse',
    # plan types — inline nested in sync-plans-request.json, generator cannot descend
    'Plan', 'PlanBudget', 'PlanBudgetAllocation', 'PlanFlight',
    'PlanChannels', 'PlanChannelMixTarget', 'PlanDelegation',
    'PlanDelegationBudget', 'PlanPortfolio', 'PlanPortfolioBudgetCap',
    'HumanOverride',
    # sync-response inline items (schemas live inside tool response schemas)
    'AccountResult', 'AccountSetup', 'CreativeResult', 'CatalogResult',
    'EventSourceResult', 'LogEventResult', 'GovernanceResult',
    'GovernanceAccount', 'GovernanceAgent', 'CreativeListItem',
    'MediaBuyListItem', 'PackageDelivery', 'DeliveryTotals', 'DeliveryData',
    'MediaBuyDelivery', 'ReportingPeriod', 'PreviewResult', 'Preview',
    'PreviewRender', 'BuildCreativeResult', 'ProductsData',
    # capability blocks — generator doesn't own these yet
    'CapabilitiesData', 'ADCPVersion', 'IdempotencyCaps', 'AccountCapabilities',
    'MediaBuyCapabilities', 'MediaBuyExecution', 'TrustedMatchCaps',
    'CreativeSpecsCaps', 'TargetingCaps', 'GeoMetrosCaps', 'GeoPostalAreasCaps',
    'GeoProximityCaps', 'AgeRestrictionCaps', 'KeywordMatchCaps',
    'AudienceTargetingCaps', 'MatchingLatencyRange', 'ConversionTrackingCaps',
    'AttributionWindowOption', 'AttributionWindow', 'ContentStandardsCaps',
    'PortfolioCaps', 'SignalsCapabilities', 'GovernanceCapabilities',
    'GovernanceFeature', 'FeatureRange', 'SICapabilities', 'SIEndpoint',
    'SITransport', 'BrandCapabilities', 'CreativeCapabilities',
    'RequestSigningCapabilities', 'WebhookSigningCapabilities',
    'ComplianceTestingCapabilities', 'IdentityCapabilities',
    'IdentityKeyOrigins', 'IdentityCompromiseNotification',
    # collection response wrappers — responses with embedded payload
    'CreateCollectionListResponse', 'GetCollectionListResponse',
    'UpdateCollectionListResponse', 'DeleteCollectionListResponse',
    'ListCollectionListsResponse',
    # collection list structural types (hand-shaped, separate review)
    'CollectionList', 'CollectionListFilters', 'BaseCollectionSource',
    'DistributionID', 'ResolvedCollection', 'CollectionPagination',
    # governance Plan's DataSubjectContestation is also inline
    'DataSubjectContestation',
    # nested shapes embedded inside other schemas — no top-level schema file
    'BillingMeasurement',  # nested in measurement-terms.json
    'MakegoodPolicy',      # nested in measurement-terms.json
    'CancellationFee',     # nested in cancellation-policy.json
    # Render / AssetSlot intentionally deferred to the format.json rework task
    'Render', 'AssetSlot',
    # PublisherPropertySelector is a hand-flattened oneOf; publisher-property-
    # selector.json is a oneOf of three variants and has no top-level properties.
    'PublisherPropertySelector',
}

# Map hand-written Go type name → schema path (relative to schemas/).
# For types whose Go name doesn't match schema filename-derived PascalCase,
# this table declares the pairing explicitly. Types with no standalone schema
# file (nested shapes, dead code) belong in EXEMPT, not here — None entries
# were removed because they never reach path resolution.
EXPLICIT_SCHEMA = {
    'Product': 'core/product.json',
    'Package': 'core/package.json',
    'MediaBuyData': 'core/media-buy.json',
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

# STRUCT_RE assumes gofmt layout (closing `}` at column 0) and top-level
# struct declarations only. Anonymous struct fields that close at column 0
# would truncate the match — not currently produced by gofmt, so this holds
# for the adcp package.
STRUCT_RE = re.compile(
    r'^type\s+(\w+)\s+struct\s*\{(.*?)^\}',
    re.MULTILINE | re.DOTALL,
)
# Captures (go_name, go_type, json_name, omitempty_modifier). The omitempty
# group is truthy when the tag ends in `,omitempty` (or any variant such as
# `,string,omitempty`) — driven by lookahead to avoid over-matching.
FIELD_LINE_RE = re.compile(
    r'^\s*(\w+)\s+([^\s`]+(?:\s+[^\s`]+)*?)\s+'
    r'`json:"([^",]+)((?:,[^"]*)?)"`',
    re.MULTILINE,
)


def _has_omitempty(tag_modifier):
    """True if a captured tag modifier ('' or ',omitempty' or ',string,omitempty')
    contains the omitempty option."""
    return ',omitempty' in (tag_modifier or '')


def parse_go_structs():
    """Return {type_name: [(go_field_name, go_type, json_tag, omitempty), ...]}
    for every top-level hand-written struct in adcp/. Embedded fields (no tag)
    are skipped — they'd need tag-chasing into the embedded type, and nothing
    in adcp/ currently uses embedding. If that changes, add embedded-field
    handling here."""
    structs = {}
    for path in GO_SOURCE_FILES:
        if not path.exists():
            continue
        src = path.read_text()
        for m in STRUCT_RE.finditer(src):
            name = m.group(1)
            body = m.group(2)
            fields = []
            for fm in FIELD_LINE_RE.finditer(body):
                fields.append((
                    fm.group(1),
                    fm.group(2).strip(),
                    fm.group(3),
                    _has_omitempty(fm.group(4)),
                ))
            structs[name] = fields
    return structs


def load_schema(path):
    with open(path) as f:
        return json.load(f, object_pairs_hook=OrderedDict)


def _resolve_ref(ref):
    """Load a schema referenced by $ref. Only supports local refs of the form
    /schemas/{version}/{path}.json — the only form actually used in-bundle.
    Contained entirely within SCRIPT_DIR to defeat any `../` escape a crafted
    ref could attempt."""
    if not isinstance(ref, str):
        return None
    m = re.match(r'^/schemas/[^/]+/(.+\.json)$', ref)
    if not m:
        return None
    path = (SCRIPT_DIR / m.group(1)).resolve()
    root = SCRIPT_DIR.resolve()
    if root != path and root not in path.parents:
        return None
    if not path.exists():
        return None
    try:
        return load_schema(path)
    except (OSError, json.JSONDecodeError):
        return None


def schema_property_set(schema, _visited=None):
    """Return the set of property names the schema declares. For oneOf/anyOf/
    allOf schemas, returns the UNION of variant properties — matches how Go
    code flattens unions into a single struct whose fields cover every variant.
    `_visited` tracks $refs already expanded to prevent cycles."""
    if _visited is None:
        _visited = set()
    props = set()
    if 'properties' in schema:
        props.update(schema['properties'].keys())
    for key in ('allOf', 'anyOf', 'oneOf'):
        for branch in schema.get(key, []):
            if not isinstance(branch, dict):
                continue
            if 'properties' in branch:
                props.update(branch['properties'].keys())
            ref = branch.get('$ref')
            if ref and ref not in _visited:
                _visited.add(ref)
                ref_schema = _resolve_ref(ref)
                if ref_schema:
                    props.update(schema_property_set(ref_schema, _visited))
    return props


def schema_is_oneof_only(schema):
    """True if the schema is a pure oneOf with no direct `properties` and no
    other composition, and none of the oneOf branches declare properties.
    Such schemas cannot be diffed at the property-name level."""
    if 'properties' in schema or 'allOf' in schema or 'anyOf' in schema:
        return False
    if 'oneOf' not in schema:
        return False
    # A pure oneOf with inline-object variants is still diffable: the variant
    # properties collectively form the union-flattened field set. Only skip if
    # no variant declares any properties.
    for branch in schema['oneOf']:
        if isinstance(branch, dict) and ('properties' in branch or '$ref' in branch):
            return False
    return True


def schema_required_set(schema):
    req = set(schema.get('required', []))
    for key in ('allOf', 'anyOf'):
        for branch in schema.get(key, []):
            if isinstance(branch, dict):
                req.update(branch.get('required', []))
    return req


def resolve_schema_path(type_name):
    """Return absolute path to the JSON schema for `type_name`, or None."""
    if type_name in EXPLICIT_SCHEMA:
        return SCRIPT_DIR / EXPLICIT_SCHEMA[type_name]
    # Otherwise, search the generate.py registries for a schema whose
    # filename-derived PascalCase matches.
    candidates = gen.CORE_SCHEMAS + gen.TOOL_SCHEMAS + gen.WEBHOOK_SCHEMAS
    for rel in candidates:
        stem = Path(rel).stem
        if gen.pascal_case(stem) == type_name:
            return SCRIPT_DIR / rel
    return None


def diff_type(type_name, go_fields, schema_path):
    """Compare a hand-written Go struct against its JSON schema. Returns a dict
    describing the drift, or None if clean."""
    if not schema_path.exists():
        return {'type': type_name, 'error': f'schema not found: {schema_path}'}
    schema = load_schema(schema_path)
    if schema_is_oneof_only(schema):
        return None  # can't diff a pure oneOf with tag-level comparison
    schema_props = schema_property_set(schema)
    schema_required = schema_required_set(schema)
    if not schema_props:
        return None
    go_tags = {tag for _, _, tag, _ in go_fields}
    # A required field marked `omitempty` in Go is silently dropped from the
    # wire when the zero value is present — a distinct failure mode from
    # missing/extra fields, worth flagging separately so the fix is obvious.
    required_with_omitempty = sorted({
        tag for _, _, tag, omitempty in go_fields
        if omitempty and tag in schema_required
    })
    missing = sorted(schema_props - go_tags)
    extra = sorted(go_tags - schema_props)
    if not missing and not extra and not required_with_omitempty:
        return None
    return {
        'type': type_name,
        'schema': str(schema_path.relative_to(SCRIPT_DIR)),
        'missing_in_go': missing,
        'extra_in_go': extra,
        'required_with_omitempty': sorted(set(required_with_omitempty)),
    }


DRIFT_REMEDIATION = """
How to fix drift:
  - `missing in Go`: a field exists in the schema but not in the hand-written
    struct. Either add the field to types.go, or — if the whole struct is now
    schema-shaped — delete the hand-written version and remove the type name
    from KNOWN_TYPES in generate.py so the generator owns it.
  - `extra in Go`: a field exists in Go but not in the schema. Remove it from
    types.go, OR if the schema uses oneOf and the field belongs to a variant,
    add the type to EXEMPT in lint.py (oneOf-flattener case).
  - `required+omitempty`: the schema marks this field required but Go has
    `omitempty` in its tag. Drop `,omitempty` — Go will silently drop required
    fields from the wire when the zero value is present.
See adcp/schemas/generate.py's KNOWN_TYPES comment for criteria on hand-written
vs generator-owned types."""


def _assert_exempt_subset_known(go_structs):
    """Guard against config drift between lint.py's EXEMPT and generate.py's
    KNOWN_TYPES. Every EXEMPT entry that actually exists in Go source must also
    be in KNOWN_TYPES; otherwise the generator will emit a duplicate or the
    linter will silently skip a type that drifted. Emits warnings on stderr;
    does not fail the run."""
    missing = sorted(
        t for t in EXEMPT
        if t in go_structs and t not in gen.KNOWN_TYPES
    )
    if missing:
        print(
            'warning: lint.py EXEMPT contains types not in generate.py '
            f'KNOWN_TYPES: {", ".join(missing)}. Add them to KNOWN_TYPES so '
            'the generator does not try to emit duplicates.',
            file=sys.stderr,
        )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--json', action='store_true', help='emit JSON report')
    parser.add_argument('--strict', action='store_true',
                        help='exit non-zero on any drift or missing schema')
    parser.add_argument(
        '--allow-missing-schemas', action='store_true',
        help='do not fail when the schemas/ folder has not been downloaded',
    )
    args = parser.parse_args()

    # Confirm schemas are present; otherwise instruct the caller.
    if not (SCRIPT_DIR / 'core' / 'product.json').exists():
        msg = ('schemas not downloaded — run ./download.sh first '
               f'(in {SCRIPT_DIR}) [skipping lint]')
        if args.allow_missing_schemas:
            print(msg, file=sys.stderr)
            return 0
        print(msg, file=sys.stderr)
        return 2

    go_structs = parse_go_structs()
    _assert_exempt_subset_known(go_structs)
    reports = []
    no_schema = []

    for type_name in sorted(gen.KNOWN_TYPES):
        if type_name in EXEMPT:
            continue
        if type_name not in go_structs:
            # Type listed in KNOWN_TYPES but not found in hand-written sources —
            # either a stale entry or defined in a file we don't scan.
            continue
        schema_path = resolve_schema_path(type_name)
        if schema_path is None:
            # No schema correspondent — these are candidates for deletion.
            no_schema.append(type_name)
            continue
        drift = diff_type(type_name, go_structs[type_name], schema_path)
        if drift:
            reports.append(drift)

    if args.json:
        out = {
            'drift': reports,
            'no_schema_correspondent': no_schema,
        }
        print(json.dumps(out, indent=2))
    else:
        if reports:
            print(f'Schema drift detected in {len(reports)} type(s):')
            for r in reports:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                if r.get('missing_in_go'):
                    print(f'    missing in Go:       {", ".join(r["missing_in_go"])}')
                if r.get('extra_in_go'):
                    print(f'    extra in Go:         {", ".join(r["extra_in_go"])}')
                if r.get('required_with_omitempty'):
                    print(f'    required+omitempty:  {", ".join(r["required_with_omitempty"])}')
            print(DRIFT_REMEDIATION)
        else:
            print('No schema drift.')
        if no_schema:
            print()
            print('Types in KNOWN_TYPES with no schema correspondent '
                  '(candidates for deletion or EXEMPT):')
            for t in no_schema:
                print(f'  - {t}')

    has_problems = bool(reports) or (args.strict and bool(no_schema))
    return 1 if (args.strict and has_problems) else 0


if __name__ == '__main__':
    sys.exit(main())
