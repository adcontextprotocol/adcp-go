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
    cd adcp/v3/schemas && ./download.sh && python3 lint.py --strict
"""

import argparse
import json
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
SCHEMA_RESOLUTION_ERRORS = gen.SCHEMA_RESOLUTION_ERRORS
GO_SOURCE_FILES = [
    ADCP_DIR / 'types.go',
    ADCP_DIR / 'inputs.go',
    ADCP_DIR / 'errors.go',
    ADCP_DIR / 'responses.go',
    ADCP_DIR / 'governance_types.go',
    ADCP_DIR / 'testcontroller.go',
    ADCP_DIR / 'seller.go',
]

# Wire-visible JSON properties implemented by custom MarshalJSON/UnmarshalJSON
# rather than direct struct tags.
CUSTOM_WIRE_FIELDS = {
    'CheckGovernanceCondition': {'required_value'},
}

# Hand-written types that are the payload of a response whose schema also lists
# the protocol envelope (core/protocol-envelope.json). The transport adds the
# envelope's optional properties (task_id, replayed, ...), so lint compares
# these types with the node minus those. The envelope's required properties
# (status) are not subtracted: nothing adds them, so a type that lacks one drops it.
ENVELOPE_SCHEMA = 'core/protocol-envelope.json'
ENVELOPE_PAYLOAD_TYPES = {'DeliveryData', 'ProductsData'}

# Hand-written types lint does not diff against a schema. Everything else in
# KNOWN_TYPES that is a struct in a scanned Go file is paired: through
# generate.py's HAND_WRITTEN_SCHEMA_SPECS or HAND_WRITTEN_INLINE_SCHEMA_SPECS,
# or by a schema file whose name PascalCases to the type. A oneOf flattener is
# paired with its union schema, and lint compares the union of the variants'
# properties. A name that is no struct lint scans (a response-builder func, a
# generated type, an alias) has nothing to exempt and does not belong here;
# validate_exempt_config fails the run on one, and on a type that is also paired.
#
# Two lists, by why the type is not compared:
#   EXEMPT_NO_SCHEMA  no schema node can describe the type, ever. A reason, no
#                     issue.
#   EXEMPT_UNPAIRED   a node exists, or should, and pairing is blocked today.
#                     A reason and the issue that unblocks it, as `#123` or
#                     `owner/repo#123`, never an umbrella issue; the entry goes
#                     when that issue closes.
EXEMPT_NO_SCHEMA = {
    'EmptyInput': 'argument type of tools whose request declares no properties',
    'ErrorOptions': 'options of the Error builder, not a wire shape',
    'TestControllerStore': 'a table of handler funcs, not a wire shape',
    'TestControllerError': 'a Go error type, not a wire shape',
    'SimulateDeliveryParams': (
        'comply_test_controller `params` is one object for every scenario: its '
        'schema declares the properties of all of them and allows more, so a '
        'scenario\'s own shape has no schema node'
    ),
    'SimulateBudgetParams': 'see SimulateDeliveryParams',
}

# An issue that tracks a whole burn-down (adcp-go#564) cannot own an entry: it
# stays open while the entries exist, which is how an entry becomes permanent.
# Every EXEMPT_UNPAIRED and KNOWN_SCHEMA_GAPS entry names the narrower issue
# that closes it.
UMBRELLA_ISSUES = {'#564'}

# Each owner is an issue reference; the umbrella issue is rejected as one.
GENERATED_TYPES = '#613'
OPTIONAL_POINTERS = '#614'
RESPONSE_ENVELOPE_STATUS = '#612'
UNPAIRED_TYPES = '#615'
WIRE_BUGS = '#611'

EXEMPT_UNPAIRED = {
    'HumanOverride': (
        'Plan.human_override has no schema node: the plan item is closed '
        '(additionalProperties: false) and declares no such property. Removing '
        'the field is breaking; Plan lists it in KNOWN_SCHEMA_GAPS',
        WIRE_BUGS,
    ),
    'AssetSlot': (
        'format.json assets items are a oneOf of typed asset shapes that '
        'AssetSlot flattens; deferred to the format.json rework',
        UNPAIRED_TYPES,
    ),
    'PackageStatus': (
        'embeds Package and marshals by hand, so its wire fields are not '
        'struct tags, which lint reads',
        UNPAIRED_TYPES,
    ),
    **{
        name: (
            'exported but nothing in the SDK takes or returns it; it is a '
            'subset of its schema node, and removing it is breaking',
            UNPAIRED_TYPES,
        )
        for name in (
            'BuildCreativeResult', 'CreativeListItem', 'LogEventResult',
            'MediaBuyListItem', 'PreviewResult',
        )
    },
}

EXEMPT = set(EXEMPT_NO_SCHEMA) | set(EXEMPT_UNPAIRED)

# Field-level drift a paired hand-written type carries today. Pairing a type in
# generate.py's HAND_WRITTEN_SCHEMA_SPECS makes lint compare it with its schema;
# the drift it already has is listed here, field by field, so --strict passes
# while every gap is still printed in each run.
#
#   Go type name -> finding kind -> {json field: issue that owns the gap}
#
# Kinds are the lists lint reports for a paired type: missing_in_go,
# extra_in_go and required_with_omitempty (schema drift), and
# optional_bool_pointer and optional_numeric_pointer (hand-written pointer
# checks). Each entry names its issue as `#123` or `owner/repo#123`, never an
# umbrella issue (UMBRELLA_ISSUES). An entry that no longer matches a finding
# fails lint, so the list only shrinks: the PR that adds a field deletes its
# entry. Entries are keyed by type, so for
# a type with several HAND_WRITTEN_INLINE_SCHEMA_SPECS pointers one entry
# covers that field on every pointer.
#
# The table is a baseline of one schema bundle, and bundle upgrades move it:
# fields appear, are renamed or leave. KNOWN_SCHEMA_GAPS_BUNDLE records which
# one; lint fails when schemas/VERSION differs, so the upgrade's PR re-reads each
# entry against the new bundle and records the new version here.
KNOWN_SCHEMA_GAPS_BUNDLE = '3.2.2'


def _owned(ref, *fields):
    """One issue owning several fields: {field: ref} for a gap-table row."""
    return {field: ref for field in fields}


KNOWN_SCHEMA_GAPS = {
    'AccountInput': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'account', 'billing_entity', 'currency', 'destination_billing_entity',
            'notification_configs', 'operator_identity', 'operator_unit',
            'preferred_reporting_protocol', 'reporting_delivery_configs', 'revision',
            'timezone',
        ),
        # Top-level account_id is not a sync_accounts property; settings-update
        # entries carry it inside `account`.
        'extra_in_go': _owned(GENERATED_TYPES, 'account_id'),
        # lint asks for *bool on every optional boolean so an explicit false
        # survives omitempty; the schema gives sandbox no default.
        'optional_bool_pointer': _owned(OPTIONAL_POINTERS, 'sandbox'),
    },
    'AccountReference': {
        # Natural-key fields of an account: operator_unit, currency, timezone.
        'missing_in_go': _owned(
            WIRE_BUGS,
            'currency', 'operator_unit', 'timezone',
        ),
        # lint asks for *bool on every optional boolean so an explicit false
        # survives omitempty; changing the field type is breaking.
        'optional_bool_pointer': _owned(OPTIONAL_POINTERS, 'sandbox'),
    },
    'AccountResult': {
        # The Go item carries 9 of the schema item's 27 properties.
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'account', 'authorization', 'billing_entity', 'credit_limit', 'currency',
            'destination_billing_entity', 'errors', 'identity_change',
            'identity_change_preview', 'name', 'notification_configs', 'operator_unit',
            'rate_card', 'reporting_delivery_configs', 'revision', 'sandbox',
            'timezone', 'warnings',
        ),
    },
    'BillingMeasurement': {
        # An optional schema field the Go struct lacks.
        'missing_in_go': _owned(GENERATED_TYPES, 'finalization_deadline_hours'),
    },
    'BrandReference': {
        # countries is part of the advertiser identity key.
        'missing_in_go': _owned(WIRE_BUGS, 'brand_kit_override', 'countries'),
    },
    'CancellationFee': {
        # lint asks for a pointer on an optional number so an explicit zero
        # survives omitempty; changing the field type is breaking.
        'optional_numeric_pointer': _owned(OPTIONAL_POINTERS, 'amount', 'rate'),
    },
    'CatalogInput': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'category', 'content_id_type', 'conversion_events', 'feed_field_mappings',
            'feed_format', 'gtins', 'ids', 'name', 'query', 'tags', 'type',
            'update_frequency', 'url',
        ),
    },
    'CatalogResult': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'catalog_generation', 'changes', 'errors', 'item_issues', 'items_pending',
            'items_rejected', 'last_synced_at', 'next_fetch_at', 'platform_id',
            'warnings',
        ),
        # lint asks for a pointer on an optional number so an explicit zero
        # survives omitempty; changing the field type is breaking.
        'optional_numeric_pointer': _owned(
            OPTIONAL_POINTERS,
            'item_count', 'items_approved',
        ),
    },
    'CollectionList': {
        'missing_in_go': _owned(WIRE_BUGS, 'account'),
        # principal is not a property of collection-list.json.
        'extra_in_go': _owned(WIRE_BUGS, 'principal'),
    },
    'CreativeFilters': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'accounts', 'asset_types', 'assigned_to_packages', 'concept_ids',
            'created_after', 'created_before', 'ext', 'format_kinds', 'has_served',
            'has_variables', 'indicator_types', 'media_buy_ids', 'name_contains',
            'statuses', 'tags', 'tags_any', 'unassigned', 'updated_after',
            'updated_before',
        ),
    },
    'CreativeInput': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'component_assets', 'format_kind', 'format_option_ref',
            'industry_identifiers', 'inputs', 'placement_ids', 'placement_refs',
            'provenance', 'representation_selection', 'rights', 'status', 'tags',
            'weight',
        ),
        # The schema allows an empty name and an empty assets object, which
        # omitempty drops, leaving the item without a required property.
        'required_with_omitempty': _owned(GENERATED_TYPES, 'assets', 'name'),
    },
    'CreativeResult': {
        # adcp-go#365 owns the CreativeResult gap, rejection_reason included.
        'missing_in_go': _owned(
            '#365',
            'account', 'assigned_to', 'assignment_errors', 'changes', 'expires_at',
            'localization', 'macro_resolution_results', 'platform_id', 'preview_url',
            'revision_id', 'warnings',
        ),
        'extra_in_go': _owned('#365', 'rejection_reason'),
    },
    'DeliveryData': {
        # Compared without the envelope's optional properties
        # (ENVELOPE_PAYLOAD_TYPES); its required status is compared, and no
        # response builder sets it.
        'missing_in_go': {
            **_owned(RESPONSE_ENVELOPE_STATUS, 'status'),
            **_owned(
                GENERATED_TYPES,
                'adcp_major_version', 'adcp_version', 'aggregated_totals',
                'attribution_window', 'errors', 'ext', 'next_expected_at',
                'notification_type', 'pagination', 'partial_data',
                'reporting_revision', 'reporting_revision_binding', 'reporting_rows',
                'sandbox', 'sequence_number', 'unavailable_count',
            ),
        },
    },
    'EventSourceInput': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'action_source', 'allowed_domains', 'event_types', 'name', 'surface',
            'value_currencies',
        ),
    },
    'EventSourceResult': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'action_source', 'errors', 'event_types', 'ext', 'health', 'managed_by',
            'name', 'seller_id', 'surface', 'value_currencies',
        ),
    },
    'GovernanceAccount': {
        # An account-ref natural key; Go carries the brand + operator variant
        # only.
        'missing_in_go': _owned(
            WIRE_BUGS,
            'account_id', 'currency', 'operator_unit', 'sandbox', 'timezone',
        ),
    },
    'GovernanceAccountInput': {
        # The accounts[] item is closed (additionalProperties: false), so a
        # request that sets either field fails schema validation.
        'extra_in_go': _owned(WIRE_BUGS, 'brand', 'operator'),
        # omitempty drops only a nil account, which the schema rejects anyway
        # (required).
        'required_with_omitempty': _owned(WIRE_BUGS, 'account'),
    },
    'GovernanceAgent': {
        # categories is not a property of the response item. The request item,
        # which requires `authentication`, is not compared: the two differ and
        # this type serves both.
        'extra_in_go': _owned(WIRE_BUGS, 'categories'),
    },
    'GovernanceResult': {
        'missing_in_go': _owned(GENERATED_TYPES, 'errors'),
    },
    'MediaBuyData': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'accepted_proposal', 'accepted_proposal_id',
            'accepted_proposal_terms_digest', 'bidding', 'budget_allocation',
            'budget_cap_timezone', 'daily_budget_cap', 'frequency_cap',
            'indicator_types_evaluated', 'indicators', 'indicators_as_of',
            'indicators_evaluated_scope', 'name', 'pacing', 'rejection_reason',
            'status_as_of', 'webhook_activity',
        ),
        # omitempty drops an empty confirmed_at, which the schema requires.
        'required_with_omitempty': _owned(WIRE_BUGS, 'confirmed_at'),
    },
    'OptimizationGoal': {
        # Unknown top-level keys round-trip through Extra, so a replacement update
        # does not drop them.
        'missing_in_go': _owned(GENERATED_TYPES, 'metric_id', 'standard', 'vendor'),
    },
    'PackageCreativeApproval': {
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'approval_scopes', 'indicator_types_evaluated', 'indicators',
            'indicators_as_of', 'indicators_evaluated_scope',
        ),
    },
    'Plan': {
        # The plan item is closed (additionalProperties: false) and declares no
        # human_override. Removing the field is breaking.
        'extra_in_go': _owned(WIRE_BUGS, 'human_override'),
        # lint asks for *bool on every optional boolean so an explicit false
        # survives omitempty; changing the field type is breaking.
        'optional_bool_pointer': _owned(OPTIONAL_POINTERS, 'human_review_required'),
    },
    'PlanBudget': {
        # An optional schema field the Go struct lacks.
        'missing_in_go': _owned(GENERATED_TYPES, 'accounting_mode'),
    },
    'PreviewRender': {
        'missing_in_go': _owned(
            WIRE_BUGS,
            'embedding', 'preview_html', 'renderer',
        ),
        # html is not a property of preview-render.json (preview_html is).
        'extra_in_go': _owned(WIRE_BUGS, 'html'),
    },
    'ProductsData': {
        # Compared without the envelope's optional properties
        # (ENVELOPE_PAYLOAD_TYPES).
        'missing_in_go': _owned(
            GENERATED_TYPES,
            'adcp_major_version', 'adcp_version', 'catalog_applied', 'errors', 'ext',
            'extensions', 'filter_diagnostics', 'incomplete', 'pagination',
            'property_list_applied', 'proposals', 'reason', 'suggestions',
            'targeting_resolution',
        ),
        # lint asks for *bool on every optional boolean so an explicit false
        # survives omitempty; changing the field type is breaking.
        'optional_bool_pointer': _owned(OPTIONAL_POINTERS, 'sandbox'),
    },
    'PublisherPropertySelector': {
        'missing_in_go': _owned('#120', 'publisher_domains'),
    },
    'ReportingPeriod': {
        # An optional schema field the Go struct lacks.
        'missing_in_go': _owned(GENERATED_TYPES, 'timezone'),
    },
    'ReportingReceipt': {
        'missing_in_go': _owned(GENERATED_TYPES, 'observed_control_totals'),
        # omitempty drops a nil observed_row_count, which the schema requires.
        'required_with_omitempty': _owned(GENERATED_TYPES, 'observed_row_count'),
    },
    'ResolvedCollection': {
        'missing_in_go': _owned(GENERATED_TYPES, 'collection_id', 'publisher_domain'),
    },
    'SignalFilters': {
        'missing_in_go': _owned(GENERATED_TYPES, 'data_providers', 'ext', 'max_percent'),
    },
    'SimulationResult': {
        'missing_in_go': _owned(GENERATED_TYPES, 'context', 'ext', 'message'),
    },
    'StateTransition': {
        'missing_in_go': _owned(GENERATED_TYPES, 'context', 'ext', 'message'),
    },
}

KNOWN_SCHEMA_GAP_KINDS = (
    'missing_in_go',
    'extra_in_go',
    'required_with_omitempty',
    'optional_bool_pointer',
    'optional_numeric_pointer',
)
DRIFT_GAP_KINDS = ('missing_in_go', 'extra_in_go', 'required_with_omitempty')
ISSUE_REF_RE = re.compile(r'(?:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?#[1-9][0-9]*')


def _owner_problem(ref):
    """Why `ref` cannot own an entry, or None."""
    if not isinstance(ref, str) or not ISSUE_REF_RE.fullmatch(ref):
        return 'needs the issue that owns it, as #123 or owner/repo#123'
    if ref in UMBRELLA_ISSUES:
        return (
            f'{ref} is an umbrella issue and cannot own an entry; name the '
            'issue that closes it'
        )
    return None

# Map hand-written Go type name → schema path (relative to schemas/).
# For types whose Go name doesn't match schema filename-derived PascalCase,
# this table declares the pairing explicitly. A shape with no standalone schema
# file is paired by JSON pointer (`file.json#/pointer`) to the node that its
# holding field maps to; types with no schema node at all belong in
# EXEMPT_NO_SCHEMA, and types whose pairing is blocked in EXEMPT_UNPAIRED.
EXPLICIT_SCHEMA = gen.HAND_WRITTEN_SCHEMA_SPECS

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
CUSTOM_JSON_METHOD_RE = re.compile(
    r'^func\s*\(\s*(?:\w+\s+)?\*?(\w+)\s*\)\s*'
    r'(MarshalJSON|UnmarshalJSON)\s*\(',
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
                if fm.group(3) == '-':
                    continue
                fields.append((
                    fm.group(1),
                    fm.group(2).strip(),
                    fm.group(3),
                    _has_omitempty(fm.group(4)),
                ))
            structs[name] = fields
    return structs


def parse_custom_json_methods():
    """Return {type_name: {MarshalJSON, UnmarshalJSON}} for scanned Go files."""
    methods = {}
    for path in GO_SOURCE_FILES:
        if not path.exists():
            continue
        src = path.read_text()
        for m in CUSTOM_JSON_METHOD_RE.finditer(src):
            methods.setdefault(m.group(1), set()).add(m.group(2))
    return methods


def validate_custom_wire_fields(go_structs, custom_json_methods):
    """Require every custom wire-field allowance to have custom JSON methods.

    CUSTOM_WIRE_FIELDS exists for fields that are deliberately wire-visible
    through MarshalJSON/UnmarshalJSON while staying hidden from direct struct
    tags. If those methods disappear, the allowance would otherwise keep
    suppressing schema drift.
    """
    reports = []
    required_methods = {'MarshalJSON', 'UnmarshalJSON'}
    for type_name, fields in sorted(CUSTOM_WIRE_FIELDS.items()):
        report = {
            'type': type_name,
            'fields': sorted(fields),
        }
        if type_name not in go_structs:
            reports.append({
                **report,
                'error': 'custom wire fields declared for unknown Go type',
            })
            continue
        missing = sorted(required_methods - custom_json_methods.get(type_name, set()))
        if missing:
            reports.append({
                **report,
                'missing_methods': missing,
                'error': 'custom wire fields require custom JSON methods',
            })
    return reports


def load_schema(path):
    with open(path) as f:
        return json.load(f, object_pairs_hook=OrderedDict)


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
    schema = load_schema(SCRIPT_DIR / path_part)
    if pointer:
        return json_pointer_get(schema, pointer)
    return schema


def _resolve_ref(ref):
    """Load a schema referenced by $ref. Only supports local refs of the form
    /schemas/{version}/{path}.json — the only form actually used in-bundle.
    Contained entirely within SCRIPT_DIR to defeat any `../` escape a crafted
    ref could attempt."""
    if not isinstance(ref, str):
        return None
    m = re.match(
        r'^(?:https://adcontextprotocol\.org)?/schemas/[^/]+/(.+\.json)(#.*)?$',
        ref,
    )
    if not m:
        return None
    rel = m.group(1)
    fragment = m.group(2) or ''
    path = (SCRIPT_DIR / rel).resolve()
    root = SCRIPT_DIR.resolve()
    if root != path and root not in path.parents:
        return None
    if not path.exists():
        return None
    try:
        schema = load_schema(path)
        if fragment:
            return json_pointer_get(schema, fragment[1:])
        return schema
    except SCHEMA_RESOLUTION_ERRORS:
        return None


def schema_property_set(schema, _visited=None):
    """Return the set of property names the schema declares. For oneOf/anyOf/
    allOf schemas, returns the UNION of variant properties — matches how Go
    code flattens unions into a single struct whose fields cover every variant.
    `_visited` tracks $refs already expanded to prevent cycles."""
    if _visited is None:
        _visited = set()
    props = set()
    ref = schema.get('$ref')
    if ref and ref not in _visited:
        _visited.add(ref)
        ref_schema = _resolve_ref(ref)
        if ref_schema:
            props.update(schema_property_set(ref_schema, _visited))
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
    for branch in schema.get('allOf', []):
        if isinstance(branch, dict):
            ref = branch.get('$ref')
            if ref:
                ref_schema = _resolve_ref(ref)
                if ref_schema:
                    req.update(schema_required_set(ref_schema))
            req.update(branch.get('required', []))
    return req


def validate_inline_schema_specs():
    """Smoke-test generated inline schema pointers so pointer drift fails in CI."""
    reports = []
    for type_name, schema_spec in gen.INLINE_SCHEMA_TYPES.items():
        path_part = schema_spec.split('#', 1)[0]
        schema_path = SCRIPT_DIR / path_part
        if not schema_path.exists():
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': f'schema not found: {schema_path}',
            })
            continue
        try:
            schema = load_schema_spec(schema_spec)
        except SCHEMA_RESOLUTION_ERRORS as e:
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': f'could not resolve schema pointer: {e}',
            })
            continue
        if not schema_property_set(schema):
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': 'schema pointer resolved but declares no properties',
            })
    return reports


def schema_is_closed_inline(schema):
    if not isinstance(schema, dict):
        return False
    if schema.get('additionalProperties') is False:
        return True
    ref = schema.get('$ref')
    if ref:
        # A bare $ref (optionally with sibling keys like title/description,
        # which draft-07 ignores) means the schema was extracted into its own
        # file. Follow it so extraction doesn't look like the type went open.
        resolved = _resolve_ref(ref)
        if isinstance(resolved, dict):
            return schema_is_closed_inline(resolved)
    branches = schema.get('oneOf') or []
    if branches:
        return all(schema_is_closed_inline(branch) for branch in branches)
    return False


def validate_inline_additional_properties_policies():
    """Verify inline helper closure policies match their schemas.

    Generated inline structs preserve only authored fields. A helper registered
    as closed must stay backed by `additionalProperties: false`; helpers backed
    by open schemas must be explicitly listed as open so the data-dropping risk
    is visible during review.
    """
    reports = []
    inline_types = set(gen.INLINE_SCHEMA_TYPES)
    closed_types = set(gen.CLOSED_INLINE_SCHEMA_TYPES)
    open_types = set(gen.OPEN_INLINE_SCHEMA_TYPES)
    overlap = closed_types & open_types
    for type_name in sorted(overlap):
        reports.append({
            'type': type_name,
            'schema': gen.INLINE_SCHEMA_TYPES.get(type_name, '?'),
            'error': 'inline type is listed as both closed and open',
            'remediation': (
                'keep the type only in CLOSED_INLINE_SCHEMA_TYPES if the schema '
                'is closed, or only in OPEN_INLINE_SCHEMA_TYPES if unknown '
                'properties are intentionally accepted by the schema'
            ),
        })

    known_policy = closed_types | open_types
    for type_name in sorted(inline_types - known_policy):
        reports.append({
            'type': type_name,
            'schema': gen.INLINE_SCHEMA_TYPES[type_name],
            'error': 'inline type is missing an additionalProperties policy',
            'remediation': (
                'add the type to CLOSED_INLINE_SCHEMA_TYPES when the schema has '
                'additionalProperties:false; otherwise add it to '
                'OPEN_INLINE_SCHEMA_TYPES and confirm unknown-field dropping is '
                'acceptable for this helper'
            ),
        })
    for type_name in sorted(known_policy - inline_types):
        reports.append({
            'type': type_name,
            'schema': '?',
            'error': 'inline additionalProperties policy references unknown type',
            'remediation': (
                'remove the stale policy entry or restore the matching '
                'INLINE_SCHEMA_TYPES registration'
            ),
        })

    for type_name in sorted(inline_types & (closed_types | open_types)):
        schema_spec = gen.INLINE_SCHEMA_TYPES[type_name]
        path_part = schema_spec.split('#', 1)[0]
        schema_path = SCRIPT_DIR / path_part
        if not schema_path.exists():
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': f'schema not found: {schema_path}',
                'remediation': 'download schemas before running strict lint',
            })
            continue
        try:
            schema = load_schema_spec(schema_spec)
        except SCHEMA_RESOLUTION_ERRORS as e:
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': f'could not resolve schema pointer: {e}',
                'remediation': 'update or remove the stale inline schema pointer',
            })
            continue
        is_closed = type_name in closed_types
        schema_is_closed = schema_is_closed_inline(schema)
        if is_closed and not schema_is_closed:
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': 'closed inline type schema is not closed to additional properties',
                'remediation': (
                    'if the schema intentionally became open, move the type to '
                    'OPEN_INLINE_SCHEMA_TYPES and review unknown-field dropping; '
                    'otherwise fix the schema or stop generating this helper as '
                    'a plain struct'
                ),
            })
        if not is_closed and schema_is_closed:
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'error': 'open inline type schema is closed to additional properties',
                'remediation': (
                    'move the type to CLOSED_INLINE_SCHEMA_TYPES so future '
                    'schema relaxation is caught, or confirm why this closed '
                    'schema still needs an open policy'
                ),
            })
    return reports


def validate_shared_inline_overrides():
    """Verify shared inline helper types still match each reused schema shape."""
    reports = []
    for type_name, schema_specs in gen.SHARED_INLINE_OVERRIDES.items():
        loaded = []
        error_count = len(reports)
        for schema_spec in schema_specs:
            path_part = schema_spec.split('#', 1)[0]
            schema_path = SCRIPT_DIR / path_part
            if not schema_path.exists():
                reports.append({
                    'type': type_name,
                    'schema': schema_spec,
                    'error': f'schema not found: {schema_path}',
                })
                continue
            try:
                schema = load_schema_spec(schema_spec)
            except SCHEMA_RESOLUTION_ERRORS as e:
                reports.append({
                    'type': type_name,
                    'schema': schema_spec,
                    'error': f'could not resolve schema pointer: {e}',
                })
                continue
            loaded.append((schema_spec, schema_property_set(schema)))
        if len(reports) != error_count or len(loaded) != len(schema_specs):
            continue

        anchor_spec, anchor_props = loaded[0]
        for schema_spec, props in loaded[1:]:
            if props == anchor_props:
                continue
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'anchor_schema': anchor_spec,
                'missing': sorted(anchor_props - props),
                'extra': sorted(props - anchor_props),
                'error': 'shared inline override schema properties differ',
            })
    return reports


def validate_cross_type_inline_hints():
    """Verify inline hints that reuse another type stay schema-equivalent."""
    reports = []
    entries = {
        entry['name']: entry
        for entry in gen.generated_schema_entries()
        if entry['kind'] == 'struct'
    }
    for hint_key, target_type in gen.CROSS_TYPE_INLINE_HINTS.items():
        source_type, json_name = hint_key
        actual_hint = gen.INLINE_TYPE_HINTS.get(hint_key)
        base_actual_hint = (
            actual_hint[1:]
            if actual_hint and actual_hint.startswith('*')
            else actual_hint
        )
        if base_actual_hint != target_type:
            reports.append({
                'type': source_type,
                'json': json_name,
                'target_type': target_type,
                'actual_hint': actual_hint,
                'error': 'cross-type inline hint does not match INLINE_TYPE_HINTS',
            })
            continue

        source_entry = entries.get(source_type)
        target_entry = entries.get(target_type)
        if source_entry is None:
            reports.append({
                'type': source_type,
                'json': json_name,
                'target_type': target_type,
                'error': 'cross-type inline hint source type not generated',
            })
            continue
        if target_entry is None:
            reports.append({
                'type': source_type,
                'json': json_name,
                'target_type': target_type,
                'schema': source_entry['schema'],
                'error': 'cross-type inline hint target type not generated',
            })
            continue

        source_props = gen.schema_properties(source_entry['schema_obj'])
        prop = source_props.get(json_name)
        if not isinstance(prop, dict):
            reports.append({
                'type': source_type,
                'json': json_name,
                'target_type': target_type,
                'schema': source_entry['schema'],
                'target_schema': target_entry['schema'],
                'error': 'cross-type inline hint source field not found',
            })
            continue
        if prop.get('type') == 'array' and isinstance(prop.get('items'), dict):
            prop = prop['items']

        source_shape = schema_property_set(prop)
        target_shape = schema_property_set(target_entry['schema_obj'])
        source_required = schema_required_set(prop)
        target_required = schema_required_set(target_entry['schema_obj'])
        if source_shape == target_shape and source_required == target_required:
            continue
        reports.append({
            'type': source_type,
            'json': json_name,
            'target_type': target_type,
            'schema': source_entry['schema'],
            'target_schema': target_entry['schema'],
            'missing': sorted(target_shape - source_shape),
            'extra': sorted(source_shape - target_shape),
            'required_missing': sorted(target_required - source_required),
            'required_extra': sorted(source_required - target_required),
            'error': 'cross-type inline hint schema shape differs',
        })
    return reports


def validate_hand_written_inline_schema_specs(go_structs):
    """Diff hand-written inline structs against their owning schema pointers."""
    reports = []
    for type_name, schema_specs in gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS.items():
        go_fields = go_structs.get(type_name)
        if go_fields is None:
            reports.append({
                'type': type_name,
                'error': 'hand-written inline type not found in scanned Go sources',
            })
            continue
        for schema_spec in schema_specs:
            drift = diff_type(type_name, go_fields, schema_spec)
            if drift:
                reports.append(drift)
    return reports


def validate_hand_written_inline_schema_divergence():
    """Report schema-side drift among pointers sharing one hand-written type."""
    reports = []
    for type_name, schema_specs in gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS.items():
        loaded = []
        error_count = len(reports)
        for schema_spec in schema_specs:
            path_part = schema_spec.split('#', 1)[0]
            schema_path = SCRIPT_DIR / path_part
            if not schema_path.exists():
                reports.append({
                    'type': type_name,
                    'schema': schema_spec,
                    'error': f'schema not found: {schema_path}',
                })
                continue
            try:
                schema = load_schema_spec(schema_spec)
            except SCHEMA_RESOLUTION_ERRORS as e:
                reports.append({
                    'type': type_name,
                    'schema': schema_spec,
                    'error': f'could not resolve schema pointer: {e}',
                })
                continue
            loaded.append((
                schema_spec,
                schema_property_set(schema),
                schema_required_set(schema),
            ))
        if len(reports) != error_count or len(loaded) != len(schema_specs):
            continue
        if len(loaded) < 2:
            continue

        anchor_spec, anchor_props, anchor_required = loaded[0]
        for schema_spec, props, required in loaded[1:]:
            if props == anchor_props and required == anchor_required:
                continue
            reports.append({
                'type': type_name,
                'schema': schema_spec,
                'anchor_schema': anchor_spec,
                'missing': sorted(anchor_props - props),
                'extra': sorted(props - anchor_props),
                'required_missing': sorted(anchor_required - required),
                'required_extra': sorted(required - anchor_required),
                'error': 'hand-written inline schema shape differs',
            })
    return reports


def validate_required_properties_declared():
    """Verify every schema emitted as a Go struct declares what it requires.

    The generator builds struct fields from `gen.schema_properties`. A name in
    `gen.schema_required_names` that it does not declare is silently dropped, so
    Go can neither read it nor emit a valid payload. Such a schema needs a
    hand-written type (KNOWN_TYPES) or a fix upstream.
    """
    reports = []
    for entry in gen.generated_schema_entries():
        if entry.get('kind') != 'struct':
            continue
        schema = entry['schema_obj']
        undeclared = sorted(
            set(gen.schema_required_names(schema)) - set(gen.schema_properties(schema))
        )
        if undeclared:
            reports.append({
                'type': entry['name'],
                'schema': entry.get('schema', '?'),
                'error': 'required properties are not declared, so the generated struct drops them',
                'undeclared': undeclared,
            })
    return reports


def validate_object_refs_not_scalar_aliases():
    """Verify no generated field binds a `$ref` to an object schema to a string
    or number alias (adcp-go#564, check 3).

    `enums/canonical-media-buy-action.json` and
    `core/canonical-media-buy-action.json` both PascalCase to
    `CanonicalMediaBuyAction`, a name that KNOWN_TYPES trusted and the enum
    owned, so an array of the objects was generated as `[]string`.
    """
    reports = []
    for entry in gen.generated_schema_entries():
        if entry.get('kind') != 'struct':
            continue
        for violation in gen.object_ref_scalar_alias_violations(
            entry['name'], entry['schema_obj'],
        ):
            reports.append({
                'type': violation['type'],
                'schema': entry.get('schema', '?'),
                'json': violation['json'],
                'ref': violation['ref'],
                'go_type': violation['go_type'],
                'error': gen.format_object_ref_scalar_alias_violation(violation),
            })
    return reports


def validate_union_schema_specs():
    """Smoke-test generated union schema pointers and shared-helper equivalence."""
    reports = []
    for type_name, schema_specs in gen.UNION_SCHEMA_TYPES.items():
        if isinstance(schema_specs, str):
            schema_specs = (schema_specs,)
        loaded = []
        error_count = len(reports)
        for schema_spec in schema_specs:
            path_part = schema_spec.split('#', 1)[0]
            schema_path = SCRIPT_DIR / path_part
            if not schema_path.exists():
                reports.append({
                    'type': type_name,
                    'schema': schema_spec,
                    'error': f'schema not found: {schema_path}',
                })
                continue
            try:
                loaded.append((schema_spec, load_schema_spec(schema_spec)))
            except SCHEMA_RESOLUTION_ERRORS as e:
                reports.append({
                    'type': type_name,
                    'schema': schema_spec,
                    'error': f'could not resolve schema pointer: {e}',
                })
        if len(reports) != error_count or len(loaded) != len(schema_specs):
            continue
        elem_types = {gen.scalar_union_go_type(schema) for _, schema in loaded}
        if len(elem_types) != 1 or None in elem_types:
            reports.append({
                'type': type_name,
                'schema': ', '.join(schema_specs),
                'error': 'schemas are not equivalent scalar-or-array unions',
            })
            continue
        empty_constraints = {gen.schema_accepts_empty_array(schema) for _, schema in loaded}
        if len(empty_constraints) != 1:
            reports.append({
                'type': type_name,
                'schema': ', '.join(schema_specs),
                'error': 'array minItems constraints differ',
            })
    return reports


OPTIONAL_NUMERIC_OMISSION_HINTS = (
    'default:',
    'defaults to',
    'default is',
    'if omitted',
    'omit for',
    'omit to',
    'when omitted',
    'if present',
    'when present',
    'when provided',
    'if provided',
    'required when',
    'populated when',
    'when specified',
    'optional override',
)

OPTIONAL_NUMERIC_SCALAR_OK = {
    # Prefer changing optional numeric fields to *int/*float64 when omission and
    # explicit zero have different wire semantics, especially request fields
    # with defaults or schema descriptions that say "if omitted" / "when
    # provided". Add a waiver only after confirming zero is invalid or
    # semantically identical to omission, and include the protocol rationale in
    # the reason string.
    # (type_name, json_name): reason
}


def numeric_zero_invalid(prop):
    """Return True when schema constraints reject an explicit numeric zero."""
    minimum = prop.get('minimum')
    if minimum is not None:
        try:
            if float(minimum) > 0:
                return True
        except (TypeError, ValueError):
            pass

    exclusive_minimum = prop.get('exclusiveMinimum')
    if exclusive_minimum is True:
        try:
            return float(prop.get('minimum', 0)) >= 0
        except (TypeError, ValueError):
            return True
    if (
        exclusive_minimum is not False and
        isinstance(exclusive_minimum, (int, float))
    ):
        if float(exclusive_minimum) >= 0:
            return True

    enum = prop.get('enum')
    if enum is not None and 0 not in enum and 0.0 not in enum:
        return True

    return False


def numeric_omission_is_semantically_distinct(prop):
    """Heuristic for optional numerics where omission means more than zero."""
    if 'default' in prop:
        try:
            return float(prop['default']) != 0
        except (TypeError, ValueError):
            return True

    description = (prop.get('description') or '').lower()
    return any(hint in description for hint in OPTIONAL_NUMERIC_OMISSION_HINTS)


def optional_numeric_pointer_candidate(json_name, prop, required_set):
    if json_name in required_set:
        return False
    if prop.get('type') not in ('integer', 'number'):
        return False
    return (
        not numeric_zero_invalid(prop) and
        numeric_omission_is_semantically_distinct(prop)
    )


def optional_bool_pointer_candidate(json_name, prop, required_set):
    """Return True when an optional boolean must preserve explicit false."""
    if json_name in required_set:
        return False
    return prop.get('type') == 'boolean'


def _branch_mentions(branch, json_name):
    forbidden = (branch.get('not') or {}).get('required') or []
    return (
        json_name in (branch.get('properties') or {})
        or json_name in (branch.get('required') or [])
        or json_name in forbidden
    )


def _branch_pins_true(branch, json_name):
    prop = (branch.get('properties') or {}).get(json_name)
    return isinstance(prop, dict) and prop.get('const') is True


def _branch_forbids(branch, json_name):
    forbidden = (branch.get('not') or {}).get('required') or []
    prop = (branch.get('properties') or {}).get(json_name)
    return json_name in forbidden or prop is False


def bool_false_is_omission(json_name, prop, schema):
    """True when the schema leaves an explicit false no meaning of its own.

    The property is `const: true`, or the schema pins it to true in a
    oneOf/anyOf/allOf branch and every branch that mentions it either pins it
    true or forbids it. False is then invalid wherever it can be written, and
    omission is the only way to say it. A boolean with a default, or one a
    branch leaves free, is not waived: false and omission can differ, and
    generated structs make the same field a *bool.
    """
    if prop.get('const') is True:
        return True
    branches = [
        branch
        for key in ('oneOf', 'anyOf', 'allOf')
        for branch in schema.get(key, [])
        if isinstance(branch, dict) and _branch_mentions(branch, json_name)
    ]
    return (
        bool(branches)
        and all(
            _branch_pins_true(b, json_name) or _branch_forbids(b, json_name)
            for b in branches
        )
        and any(_branch_pins_true(b, json_name) for b in branches)
    )


def optional_bool_pointer_reports(go_structs):
    """Find optional boolean fields that need pointers to preserve false."""
    reports = []

    for entry in gen.generated_schema_entries():
        if entry['kind'] != 'struct':
            continue
        props = gen.schema_properties(entry['schema_obj'])
        required_set = gen.schema_required_names(entry['schema_obj'])
        for json_name, prop in props.items():
            if not isinstance(prop, dict):
                continue
            if not optional_bool_pointer_candidate(json_name, prop, required_set):
                continue
            go_type, _ = gen.field_go_type_info(
                entry['name'], json_name, prop, required_set,
            )
            # Generated optional booleans should already be pointers; this branch
            # catches generator regressions before they reach types_gen.go.
            if go_type == 'bool':
                reports.append({
                    'owner': 'generated',
                    'type': entry['name'],
                    'schema': entry['schema'],
                    'json': json_name,
                    'go_type': go_type,
                    'description': prop.get('description', ''),
                })

    for type_name, go_fields in sorted(go_structs.items()):
        schema_spec = resolve_schema_spec(type_name)
        if schema_spec is None:
            continue
        try:
            schema = load_schema_spec(schema_spec)
        except SCHEMA_RESOLUTION_ERRORS:
            continue
        props = gen.schema_properties(schema)
        required_set = gen.schema_required_names(schema)
        fields_by_json = {tag: go_type for _, go_type, tag, _ in go_fields}
        for json_name, prop in props.items():
            if not isinstance(prop, dict):
                continue
            if not optional_bool_pointer_candidate(json_name, prop, required_set):
                continue
            if bool_false_is_omission(json_name, prop, schema):
                continue
            go_type = fields_by_json.get(json_name)
            if go_type == 'bool':
                reports.append({
                    'owner': 'hand-written',
                    'type': type_name,
                    'schema': schema_spec,
                    'json': json_name,
                    'go_type': go_type,
                    'description': prop.get('description', ''),
                })

    return reports


def optional_numeric_pointer_reports(go_structs):
    """Find optional numeric fields that need pointers to preserve omission."""
    reports = []

    for entry in gen.generated_schema_entries():
        if entry['kind'] != 'struct':
            continue
        props = gen.schema_properties(entry['schema_obj'])
        required_set = gen.schema_required_names(entry['schema_obj'])
        for json_name, prop in props.items():
            if not isinstance(prop, dict):
                continue
            if not optional_numeric_pointer_candidate(json_name, prop, required_set):
                continue
            go_type, _ = gen.field_go_type_info(
                entry['name'], json_name, prop, required_set,
            )
            if go_type in ('int', 'float64') and (
                entry['name'], json_name,
            ) not in OPTIONAL_NUMERIC_SCALAR_OK:
                reports.append({
                    'owner': 'generated',
                    'type': entry['name'],
                    'schema': entry['schema'],
                    'json': json_name,
                    'go_type': go_type,
                    'description': prop.get('description', ''),
                })

    for type_name, go_fields in sorted(go_structs.items()):
        schema_spec = resolve_schema_spec(type_name)
        if schema_spec is None:
            continue
        try:
            schema = load_schema_spec(schema_spec)
        except SCHEMA_RESOLUTION_ERRORS:
            continue
        props = gen.schema_properties(schema)
        required_set = gen.schema_required_names(schema)
        fields_by_json = {tag: go_type for _, go_type, tag, _ in go_fields}
        for json_name, prop in props.items():
            if not isinstance(prop, dict):
                continue
            if not optional_numeric_pointer_candidate(json_name, prop, required_set):
                continue
            go_type = fields_by_json.get(json_name)
            if go_type in ('int', 'float64') and (
                type_name, json_name,
            ) not in OPTIONAL_NUMERIC_SCALAR_OK:
                reports.append({
                    'owner': 'hand-written',
                    'type': type_name,
                    'schema': schema_spec,
                    'json': json_name,
                    'go_type': go_type,
                    'description': prop.get('description', ''),
                })

    return reports


def resolve_schema_spec(type_name):
    """Return schema spec for `type_name`, or None."""
    if type_name in EXPLICIT_SCHEMA:
        return EXPLICIT_SCHEMA[type_name]
    # Otherwise, search the generate.py registries for a schema whose
    # filename-derived PascalCase matches.
    candidates = (
        gen.CORE_SCHEMAS + gen.SUPPORT_SCHEMAS +
        gen.TOOL_SCHEMAS + gen.WEBHOOK_SCHEMAS
    )
    for rel in candidates:
        stem = Path(rel).stem
        if gen.pascal_case(stem) == type_name:
            return rel
    return None


def go_zero_is_schema_invalid(prop):
    """True when the schema rejects the Go zero value of this property's type.

    A required field tagged omitempty is dropped from the wire when it holds
    its zero value. When the schema rejects that value anyway (an integer with
    `minimum: 1`, a string with `minLength: 1` or an enum without "", an array
    with `minItems: 1`), the drop loses nothing a valid payload carries.
    """
    if not isinstance(prop, dict):
        return False
    merged = dict(prop)
    ref = merged.pop('$ref', None)
    if ref:
        resolved = _resolve_ref(ref)
        if isinstance(resolved, dict):
            merged = {**resolved, **merged}
    kind = merged.get('type')
    kinds = [k for k in (kind if isinstance(kind, list) else [kind]) if k != 'null']
    if len(kinds) != 1:
        return False
    if kinds[0] in ('integer', 'number'):
        return numeric_zero_invalid(merged)
    if kinds[0] == 'string':
        return (
            merged.get('minLength', 0) >= 1
            or ('enum' in merged and '' not in merged['enum'])
            or ('const' in merged and merged['const'] != '')
        )
    if kinds[0] == 'array':
        return merged.get('minItems', 0) >= 1
    if kinds[0] == 'object':
        return merged.get('minProperties', 0) >= 1
    return False


_ENVELOPE_PROPERTIES = []


def protocol_envelope_properties():
    """The optional properties core/protocol-envelope.json adds to a response."""
    if not _ENVELOPE_PROPERTIES:
        schema = load_schema(SCRIPT_DIR / ENVELOPE_SCHEMA)
        _ENVELOPE_PROPERTIES.append(
            set(schema.get('properties', {})) - set(schema.get('required', []))
        )
    return _ENVELOPE_PROPERTIES[0]


def diff_type(type_name, go_fields, schema_spec):
    """Compare a hand-written Go struct against its JSON schema. Returns a dict
    describing the drift, or None if clean."""
    path_part = schema_spec.split('#', 1)[0]
    schema_path = SCRIPT_DIR / path_part
    if not schema_path.exists():
        return {'type': type_name, 'error': f'schema not found: {schema_path}'}
    try:
        schema = load_schema_spec(schema_spec)
    except SCHEMA_RESOLUTION_ERRORS as e:
        return {
            'type': type_name,
            'schema': schema_spec,
            'error': f'could not resolve schema pointer: {e}',
        }
    if schema_is_oneof_only(schema):
        return None  # can't diff a pure oneOf with tag-level comparison
    schema_props = schema_property_set(schema)
    schema_required = schema_required_set(schema)
    if not schema_props:
        return None
    go_tags = {tag for _, _, tag, _ in go_fields}
    go_tags.update(CUSTOM_WIRE_FIELDS.get(type_name, set()))
    if type_name in ENVELOPE_PAYLOAD_TYPES:
        # The transport adds the envelope's optional properties; the rest is compared.
        envelope = protocol_envelope_properties()
        schema_props = schema_props - envelope
        schema_required = schema_required - envelope
        go_tags = go_tags - envelope
        go_fields = [f for f in go_fields if f[2] not in envelope]
    schema_prop_defs = gen.schema_properties(schema)
    # A required field marked `omitempty` in Go is silently dropped from the
    # wire when the zero value is present — a distinct failure mode from
    # missing/extra fields, worth flagging separately so the fix is obvious.
    required_with_omitempty = sorted({
        tag for _, _, tag, omitempty in go_fields
        if omitempty and tag in schema_required
        and not go_zero_is_schema_invalid(schema_prop_defs.get(tag))
    })
    missing = sorted(schema_props - go_tags)
    extra = sorted(go_tags - schema_props)
    if not missing and not extra and not required_with_omitempty:
        return None
    return {
        'type': type_name,
        'schema': schema_spec,
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
    types.go. A oneOf flattener is paired with its union schema, so a field
    that belongs to one variant is not extra.
  - `required+omitempty`: the schema marks this field required but Go has
    `omitempty` in its tag. Drop `,omitempty` — Go will silently drop required
    fields from the wire when the zero value is present.
  - Pairing a type whose fields cannot be fixed in the same PR: list each
    finding in KNOWN_SCHEMA_GAPS with the issue that owns it, rather than
    leaving the whole type out of the comparison. EXEMPT_NO_SCHEMA and
    EXEMPT_UNPAIRED are for types lint cannot compare at all; each entry has
    a reason, and EXEMPT_UNPAIRED the issue that unblocks it.
See adcp/v3/schemas/generate.py's KNOWN_TYPES comment for criteria on hand-written
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


def validate_known_schema_gaps_config():
    """Require every KNOWN_SCHEMA_GAPS entry to have a known kind and an owner."""
    reports = []
    for type_name, kinds in sorted(KNOWN_SCHEMA_GAPS.items()):
        for kind, fields in sorted(kinds.items()):
            if kind not in KNOWN_SCHEMA_GAP_KINDS:
                reports.append({
                    'type': type_name,
                    'kind': kind,
                    'error': (
                        'unknown known-gap kind; use one of '
                        + ', '.join(KNOWN_SCHEMA_GAP_KINDS)
                    ),
                })
                continue
            for json_name, ref in sorted(fields.items()):
                problem = _owner_problem(ref)
                if problem:
                    reports.append({
                        'type': type_name,
                        'kind': kind,
                        'json': json_name,
                        'ref': ref,
                        'error': f'known gap {problem}',
                    })
    return reports


def _known_gap_ref(type_name, kind, json_name):
    ref = KNOWN_SCHEMA_GAPS.get(type_name, {}).get(kind, {}).get(json_name)
    if _owner_problem(ref) is None:
        return ref
    return None


def split_known_drift(drift, known, matched):
    """Move KNOWN_SCHEMA_GAPS fields out of a diff_type report.

    Returns the report with only the unlisted findings left, or None when none
    are left. Each suppressed field is appended to `known` and recorded in
    `matched` as (type, kind, json) so stale entries can be found afterwards.
    """
    if drift is None:
        return None
    if drift.get('error'):
        return drift
    type_name = drift['type']
    remaining = dict(drift)
    for kind in DRIFT_GAP_KINDS:
        kept = []
        for json_name in drift.get(kind, []):
            ref = _known_gap_ref(type_name, kind, json_name)
            if ref is None:
                kept.append(json_name)
                continue
            matched.add((type_name, kind, json_name))
            known.append({
                'type': type_name,
                'schema': drift.get('schema', '?'),
                'kind': kind,
                'json': json_name,
                'ref': ref,
            })
        remaining[kind] = kept
    if any(remaining.get(kind) for kind in DRIFT_GAP_KINDS):
        return remaining
    return None


def split_known_pointer_reports(reports, kind, known, matched):
    """Move KNOWN_SCHEMA_GAPS fields out of hand-written pointer reports."""
    kept = []
    for r in reports:
        ref = None
        if r.get('owner') == 'hand-written':
            ref = _known_gap_ref(r['type'], kind, r['json'])
        if ref is None:
            kept.append(r)
            continue
        matched.add((r['type'], kind, r['json']))
        known.append({
            'type': r['type'],
            'schema': r.get('schema', '?'),
            'kind': kind,
            'json': r['json'],
            'ref': ref,
        })
    return kept


def stale_known_schema_gaps(matched):
    """Report KNOWN_SCHEMA_GAPS entries that matched no finding in this run."""
    reports = []
    matched_types = {type_name for type_name, _, _ in matched}
    for type_name, kinds in sorted(KNOWN_SCHEMA_GAPS.items()):
        if type_name not in matched_types:
            reports.append({
                'type': type_name,
                'kind': 'all',
                'error': (
                    'stale known gaps: lint reports none of this type\'s '
                    'entries (it is not paired, it is in EXEMPT, its schema '
                    'pointer did not resolve, or every gap closed); pair the '
                    'type or delete its entries'
                ),
            })
            continue
        for kind, fields in sorted(kinds.items()):
            if kind not in KNOWN_SCHEMA_GAP_KINDS:
                continue
            for json_name, ref in sorted(fields.items()):
                if (type_name, kind, json_name) in matched:
                    continue
                if _owner_problem(ref):
                    continue  # validate_known_schema_gaps_config reports it
                reports.append({
                    'type': type_name,
                    'kind': kind,
                    'json': json_name,
                    'ref': ref,
                    'error': (
                        'stale known gap: lint no longer reports this finding '
                        '(the field was fixed or left the schema, the type is '
                        'not paired, or its schema pointer did not resolve); '
                        'delete the entry or fix the pairing'
                    ),
                })
    return reports


def validate_known_gaps_bundle():
    """Require KNOWN_SCHEMA_GAPS to be baselined on the bundle in schemas/VERSION."""
    try:
        version = (SCRIPT_DIR / 'VERSION').read_text().strip()
    except OSError as e:
        return [{
            'type': 'KNOWN_SCHEMA_GAPS',
            'kind': 'bundle',
            'error': f'cannot read schemas/VERSION to check the baseline: {e}',
        }]
    if version == KNOWN_SCHEMA_GAPS_BUNDLE:
        return []
    return [{
        'type': 'KNOWN_SCHEMA_GAPS',
        'kind': 'bundle',
        'error': (
            f'KNOWN_SCHEMA_GAPS is baselined on bundle {KNOWN_SCHEMA_GAPS_BUNDLE} '
            f'but schemas/VERSION is {version}: re-read every entry against the '
            'new bundle, then set KNOWN_SCHEMA_GAPS_BUNDLE'
        ),
    }]


def validate_exempt_config(go_structs):
    """Hold EXEMPT_NO_SCHEMA and EXEMPT_UNPAIRED to their rules.

    Every entry names a struct lint scans, has a reason, and is not also paired.
    An EXEMPT_UNPAIRED entry names the issue that unblocks it, and an
    EXEMPT_NO_SCHEMA entry has no schema file by its name to pair.
    """
    reports = []
    paired = set(EXPLICIT_SCHEMA) | set(gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS)

    def report(type_name, list_name, error):
        reports.append({'type': type_name, 'list': list_name, 'error': error})

    for type_name in sorted(set(EXEMPT_NO_SCHEMA) & set(EXEMPT_UNPAIRED)):
        report(type_name, 'both', 'listed in EXEMPT_NO_SCHEMA and EXEMPT_UNPAIRED')
    entries = (
        [(t, 'EXEMPT_NO_SCHEMA', r, None) for t, r in EXEMPT_NO_SCHEMA.items()]
        + [(t, 'EXEMPT_UNPAIRED', r[0], r[1]) for t, r in EXEMPT_UNPAIRED.items()]
    )
    for type_name, list_name, reason, ref in sorted(entries):
        if type_name not in go_structs:
            report(
                type_name, list_name,
                'not a struct in the scanned Go files (a func, alias or '
                'generated type has nothing to exempt); delete the entry',
            )
            continue
        if type_name in paired:
            report(
                type_name, list_name,
                'paired in HAND_WRITTEN_SCHEMA_SPECS or '
                'HAND_WRITTEN_INLINE_SCHEMA_SPECS, so it is compared; delete '
                'the entry',
            )
        if not isinstance(reason, str) or not reason.strip():
            report(type_name, list_name, 'needs a reason')
        if list_name == 'EXEMPT_NO_SCHEMA' and resolve_schema_spec(type_name):
            report(
                type_name, list_name,
                'a schema file is named after this type; pair it instead of '
                'exempting it',
            )
        if list_name == 'EXEMPT_UNPAIRED' and _owner_problem(ref):
            report(type_name, list_name, f'issue: {_owner_problem(ref)}')
    return reports


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
    custom_json_methods = parse_custom_json_methods()
    _assert_exempt_subset_known(go_structs)
    exempt_errors = validate_exempt_config(go_structs)
    reports = []
    no_schema = []
    inline_schema_errors = validate_inline_schema_specs()
    inline_additional_properties_errors = validate_inline_additional_properties_policies()
    shared_inline_errors = validate_shared_inline_overrides()
    cross_type_inline_errors = validate_cross_type_inline_hints()
    union_schema_errors = validate_union_schema_specs()
    required_undeclared_errors = validate_required_properties_declared()
    object_ref_alias_errors = validate_object_refs_not_scalar_aliases()
    custom_wire_field_errors = validate_custom_wire_fields(
        go_structs,
        custom_json_methods,
    )
    known_gaps = []
    matched_gaps = set()
    optional_bool_reports = split_known_pointer_reports(
        optional_bool_pointer_reports(go_structs),
        'optional_bool_pointer', known_gaps, matched_gaps,
    )
    optional_numeric_reports = split_known_pointer_reports(
        optional_numeric_pointer_reports(go_structs),
        'optional_numeric_pointer', known_gaps, matched_gaps,
    )
    hand_written_inline_schema_divergence = validate_hand_written_inline_schema_divergence()
    hand_written_inline_reports = [
        r for r in (
            split_known_drift(drift, known_gaps, matched_gaps)
            for drift in validate_hand_written_inline_schema_specs(go_structs)
        )
        if r
    ]
    reports.extend(hand_written_inline_reports)

    for type_name in sorted(gen.KNOWN_TYPES):
        if type_name in EXEMPT:
            continue
        if type_name in gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS:
            continue  # paired by pointer and diffed above
        if type_name not in go_structs:
            # Type listed in KNOWN_TYPES but not found in hand-written sources —
            # either a stale entry or defined in a file we don't scan.
            continue
        schema_spec = resolve_schema_spec(type_name)
        if schema_spec is None:
            # No schema correspondent — these are candidates for deletion.
            no_schema.append(type_name)
            continue
        drift = split_known_drift(
            diff_type(type_name, go_structs[type_name], schema_spec),
            known_gaps, matched_gaps,
        )
        if drift:
            reports.append(drift)

    known_gap_errors = (
        validate_known_gaps_bundle()
        + validate_known_schema_gaps_config()
        + stale_known_schema_gaps(matched_gaps)
    )
    known_gaps.sort(key=lambda g: (g['type'], g['schema'], g['kind'], g['json']))

    if args.json:
        out = {
            'drift': reports,
            'hand_written_inline_drift': hand_written_inline_reports,
            'hand_written_inline_schema_divergence': hand_written_inline_schema_divergence,
            'no_schema_correspondent': no_schema,
            'inline_schema_errors': inline_schema_errors,
            'inline_additional_properties_errors': inline_additional_properties_errors,
            'shared_inline_errors': shared_inline_errors,
            'cross_type_inline_errors': cross_type_inline_errors,
            'union_schema_errors': union_schema_errors,
            'required_undeclared_errors': required_undeclared_errors,
            'object_ref_alias_errors': object_ref_alias_errors,
            'custom_wire_field_errors': custom_wire_field_errors,
            'optional_bool_pointer': optional_bool_reports,
            'optional_numeric_pointer': optional_numeric_reports,
            'known_schema_gaps': known_gaps,
            'known_schema_gap_errors': known_gap_errors,
            'exempt_errors': exempt_errors,
        }
        print(json.dumps(out, indent=2))
    else:
        if required_undeclared_errors:
            print(
                'Required-but-undeclared property errors in '
                f'{len(required_undeclared_errors)} type(s):'
            )
            for r in required_undeclared_errors:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                print(f'    error:      {r["error"]}')
                print(f'    undeclared: {", ".join(r["undeclared"])}')
            print()
        if object_ref_alias_errors:
            print(
                'Object $ref bound to a scalar Go type in '
                f'{len(object_ref_alias_errors)} field(s):'
            )
            for r in object_ref_alias_errors:
                print()
                print(f'  {r["type"]}.{r["json"]}  ({r.get("schema", "?")})')
                print(f'    ref:     {r["ref"]}')
                print(f'    Go type: {r["go_type"]}')
                print(
                    '    fix:     add a REF_ALIASES entry giving the object its own '
                    'Go name, and drop the name from KNOWN_TYPES'
                )
            print()
        if inline_schema_errors:
            print(f'Inline schema pointer errors in {len(inline_schema_errors)} type(s):')
            for r in inline_schema_errors:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                print(f'    error: {r["error"]}')
            print()
        if inline_additional_properties_errors:
            print(
                'Inline additionalProperties policy errors in '
                f'{len(inline_additional_properties_errors)} type(s):'
            )
            for r in inline_additional_properties_errors:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                print(f'    error: {r["error"]}')
                print(f'    fix:   {r["remediation"]}')
            print()
        if shared_inline_errors:
            print(f'Shared inline override errors in {len(shared_inline_errors)} type(s):')
            for r in shared_inline_errors:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                print(f'    anchor: {r.get("anchor_schema", "?")}')
                print(f'    error:  {r["error"]}')
                if r.get('missing'):
                    print(f'    missing: {", ".join(r["missing"])}')
                if r.get('extra'):
                    print(f'    extra:   {", ".join(r["extra"])}')
            print()
        if cross_type_inline_errors:
            print(
                'Cross-type inline hint errors in '
                f'{len(cross_type_inline_errors)} type(s):'
            )
            for r in cross_type_inline_errors:
                print()
                print(
                    f'  {r["type"]}.{r.get("json", "?")}  '
                    f'({r.get("schema", "?")})'
                )
                print(f'    target: {r.get("target_type", "?")}')
                if r.get('target_schema'):
                    print(f'    target schema: {r["target_schema"]}')
                print(f'    error:  {r["error"]}')
                if r.get('actual_hint') is not None:
                    print(f'    actual hint: {r["actual_hint"]}')
                if r.get('missing'):
                    print(f'    missing: {", ".join(r["missing"])}')
                if r.get('extra'):
                    print(f'    extra:   {", ".join(r["extra"])}')
                if r.get('required_missing'):
                    print(
                        '    required missing: '
                        f'{", ".join(r["required_missing"])}'
                    )
                if r.get('required_extra'):
                    print(
                        '    required extra:   '
                        f'{", ".join(r["required_extra"])}'
                    )
            print()
        if hand_written_inline_schema_divergence:
            print(
                'Hand-written inline schema divergence in '
                f'{len(hand_written_inline_schema_divergence)} type(s):'
            )
            for r in hand_written_inline_schema_divergence:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                print(f'    anchor: {r.get("anchor_schema", "?")}')
                print(f'    error:  {r["error"]}')
                if r.get('missing'):
                    print(f'    missing: {", ".join(r["missing"])}')
                if r.get('extra'):
                    print(f'    extra:   {", ".join(r["extra"])}')
                if r.get('required_missing'):
                    print(
                        '    required missing: '
                        f'{", ".join(r["required_missing"])}'
                    )
                if r.get('required_extra'):
                    print(
                        '    required extra:   '
                        f'{", ".join(r["required_extra"])}'
                    )
            print()
        if union_schema_errors:
            print(f'Union schema pointer errors in {len(union_schema_errors)} type(s):')
            for r in union_schema_errors:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                print(f'    error: {r["error"]}')
            print()
        if custom_wire_field_errors:
            print(
                'Custom wire-field errors in '
                f'{len(custom_wire_field_errors)} type(s):'
            )
            for r in custom_wire_field_errors:
                print()
                print(f'  {r["type"]}')
                print(f'    fields: {", ".join(r["fields"])}')
                print(f'    error:  {r["error"]}')
                if r.get('missing_methods'):
                    print(f'    missing methods: {", ".join(r["missing_methods"])}')
            print()
        if optional_bool_reports:
            print(
                'Optional boolean pointer issues in '
                f'{len(optional_bool_reports)} field(s):'
            )
            for r in optional_bool_reports:
                print()
                print(f'  {r["type"]}.{r["json"]}  ({r["schema"]})')
                print(f'    owner:   {r["owner"]}')
                print(f'    Go type: {r["go_type"]}')
                print('    fix:     use *bool so explicit false survives omitempty')
            print()
        if optional_numeric_reports:
            print(
                'Optional numeric pointer issues in '
                f'{len(optional_numeric_reports)} field(s):'
            )
            for r in optional_numeric_reports:
                print()
                print(f'  {r["type"]}.{r["json"]}  ({r["schema"]})')
                print(f'    owner:   {r["owner"]}')
                print(f'    Go type: {r["go_type"]}')
                print('    fix:     use *int/*float64 or add an OPTIONAL_NUMERIC_SCALAR_OK waiver')
            print()
        if exempt_errors:
            print(f'EXEMPT errors ({len(exempt_errors)}):')
            for r in exempt_errors:
                print()
                print(f'  {r["type"]}  ({r["list"]})')
                print(f'    error: {r["error"]}')
            print()
        if known_gap_errors:
            print(f'Known schema gap errors ({len(known_gap_errors)}):')
            for r in known_gap_errors:
                print()
                where = r['type'] + (f'.{r["json"]}' if r.get('json') else '')
                print(f'  {where}  ({r["kind"]}, {r.get("ref", "no issue")})')
                print(f'    error: {r["error"]}')
            print()
        if reports:
            print(f'Schema drift detected in {len(reports)} type(s):')
            for r in reports:
                print()
                print(f'  {r["type"]}  ({r.get("schema", "?")})')
                if r.get('error'):
                    print(f'    error: {r["error"]}')
                if r.get('missing_in_go'):
                    print(f'    missing in Go:       {", ".join(r["missing_in_go"])}')
                if r.get('extra_in_go'):
                    print(f'    extra in Go:         {", ".join(r["extra_in_go"])}')
                if r.get('required_with_omitempty'):
                    print(f'    required+omitempty:  {", ".join(r["required_with_omitempty"])}')
            print(DRIFT_REMEDIATION)
        else:
            print('No schema drift.')
        if known_gaps:
            print()
            print(
                f'Known schema gaps ({len(known_gaps)} field(s), each owned by '
                'an issue; listed in KNOWN_SCHEMA_GAPS, not failing):'
            )
            by_type = OrderedDict()
            for g in known_gaps:
                by_type.setdefault((g['type'], g['schema']), []).append(g)
            for (type_name, schema_spec), gaps in by_type.items():
                print()
                print(f'  {type_name}  ({schema_spec})')
                for kind in KNOWN_SCHEMA_GAP_KINDS:
                    fields = [
                        f'{g["json"]} ({g["ref"]})'
                        for g in gaps if g['kind'] == kind
                    ]
                    if fields:
                        print(f'    {kind}: {", ".join(fields)}')
        if no_schema:
            print()
            print('Types in KNOWN_TYPES with no schema correspondent '
                  '(pair them, or list them in EXEMPT_NO_SCHEMA or EXEMPT_UNPAIRED):')
            for t in no_schema:
                print(f'  - {t}')

    has_problems = (
        bool(inline_schema_errors)
        or bool(inline_additional_properties_errors)
        or bool(shared_inline_errors)
        or bool(cross_type_inline_errors)
        or bool(hand_written_inline_schema_divergence)
        or bool(union_schema_errors)
        or bool(required_undeclared_errors)
        or bool(object_ref_alias_errors)
        or bool(custom_wire_field_errors)
        or bool(optional_bool_reports)
        or bool(reports)
        or bool(optional_numeric_reports)
        or bool(known_gap_errors)
        or bool(exempt_errors)
        or (args.strict and bool(no_schema))
    )
    return 1 if (args.strict and has_problems) else 0


if __name__ == '__main__':
    sys.exit(main())
