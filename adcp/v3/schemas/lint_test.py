import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import lint


ACCOUNT_SETUP_SCHEMA_SPECS = lint.gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS["AccountSetup"]
ACCOUNT_SETUP_SCHEMA_PATHS_EXIST = all(
    (lint.SCRIPT_DIR / spec.split("#", 1)[0]).exists()
    for spec in ACCOUNT_SETUP_SCHEMA_SPECS
)
VALIDATE_INPUT_WARNING_SCHEMA_SPEC = "creative/validate-input-result.json#/properties/warnings/items"
VALIDATE_INPUT_RESULT_SCHEMA_EXISTS = (lint.SCRIPT_DIR / "creative/validate-input-result.json").exists()
CHECK_GOVERNANCE_CONDITION_SCHEMA_SPECS = (
    lint.gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS["CheckGovernanceCondition"]
)
CHECK_GOVERNANCE_CONDITION_SCHEMA_PATHS_EXIST = all(
    (lint.SCRIPT_DIR / spec.split("#", 1)[0]).exists()
    for spec in CHECK_GOVERNANCE_CONDITION_SCHEMA_SPECS
)


class SharedInlineOverrideLintTest(unittest.TestCase):
    def test_shared_inline_override_reports_property_drift(self):
        original_overrides = lint.gen.SHARED_INLINE_OVERRIDES
        original_load_schema_spec = lint.load_schema_spec
        try:
            lint.gen.SHARED_INLINE_OVERRIDES = {
                "SharedShape": [
                    "core/product.json#/properties/a",
                    "core/product.json#/properties/b",
                ],
            }

            def load_schema_spec(spec):
                if spec.endswith("/a"):
                    return {"properties": {"enabled": {}}}
                return {"properties": {"enabled": {}, "limit": {}}}

            lint.load_schema_spec = load_schema_spec

            reports = lint.validate_shared_inline_overrides()
        finally:
            lint.gen.SHARED_INLINE_OVERRIDES = original_overrides
            lint.load_schema_spec = original_load_schema_spec

        self.assertEqual(1, len(reports))
        self.assertEqual("SharedShape", reports[0]["type"])
        self.assertEqual(["limit"], reports[0]["extra"])
        self.assertEqual([], reports[0]["missing"])


class CrossTypeInlineHintLintTest(unittest.TestCase):
    def test_performance_feedback_measurement_period_hint_is_registered(self):
        hint_key = ("PerformanceFeedback", "measurement_period")

        self.assertEqual("DatetimeRange", lint.gen.CROSS_TYPE_INLINE_HINTS[hint_key])
        self.assertEqual("DatetimeRange", lint.gen.INLINE_TYPE_HINTS[hint_key])

    def test_cross_type_inline_hint_reports_shape_drift(self):
        entries = [
            {
                "kind": "struct",
                "name": "SourceShape",
                "schema": "source.json",
                "schema_obj": {
                    "properties": {
                        "period": {
                            "type": "object",
                            "properties": {
                                "start": {},
                                "timezone": {},
                            },
                            "required": ["start", "timezone"],
                        },
                    },
                },
            },
            {
                "kind": "struct",
                "name": "TargetShape",
                "schema": "target.json",
                "schema_obj": {
                    "properties": {
                        "start": {},
                        "end": {},
                    },
                    "required": ["start", "end"],
                },
            },
        ]

        with (
            mock.patch.object(
                lint.gen,
                "CROSS_TYPE_INLINE_HINTS",
                {("SourceShape", "period"): "TargetShape"},
            ),
            mock.patch.object(
                lint.gen,
                "INLINE_TYPE_HINTS",
                {("SourceShape", "period"): "TargetShape"},
            ),
            mock.patch.object(
                lint.gen,
                "generated_schema_entries",
                return_value=iter(entries),
            ),
        ):
            reports = lint.validate_cross_type_inline_hints()

        self.assertEqual(1, len(reports))
        self.assertEqual("SourceShape", reports[0]["type"])
        self.assertEqual("period", reports[0]["json"])
        self.assertEqual("TargetShape", reports[0]["target_type"])
        self.assertEqual(["end"], reports[0]["missing"])
        self.assertEqual(["timezone"], reports[0]["extra"])
        self.assertEqual(["end"], reports[0]["required_missing"])
        self.assertEqual(["timezone"], reports[0]["required_extra"])


class HandWrittenInlineSchemaLintTest(unittest.TestCase):
    def test_account_setup_inline_schema_registration_is_pinned_without_schemas(self):
        self.assertEqual(
            [
                "core/account.json#/properties/setup",
                (
                    "account/sync-accounts-response.json"
                    "#/oneOf/0/properties/accounts/items/properties/setup"
                ),
                (
                    "bundled/creative/list-creatives-response.json"
                    "#/properties/creatives/items/properties/account/properties/setup"
                ),
                (
                    "bundled/creative/sync-creatives-response.json"
                    "#/oneOf/0/properties/creatives/items/properties/account/properties/setup"
                ),
                (
                    "bundled/media-buy/create-media-buy-response.json"
                    "#/oneOf/0/properties/account/properties/setup"
                ),
                (
                    "bundled/media-buy/get-media-buys-response.json"
                    "#/properties/media_buys/items/properties/account/properties/setup"
                ),
            ],
            ACCOUNT_SETUP_SCHEMA_SPECS,
        )
        self.assertEqual(6, len(ACCOUNT_SETUP_SCHEMA_SPECS))
        for schema_spec in ACCOUNT_SETUP_SCHEMA_SPECS:
            path_part, pointer = schema_spec.split("#", 1)
            self.assertTrue(path_part.endswith(".json"))
            self.assertTrue(pointer.startswith("/"))

    def test_schema_divergence_reports_property_and_required_drift(self):
        specs = {
            "InlineShape": [
                "shape.json#/properties/first",
                "shape.json#/properties/second",
            ],
        }

        def load_schema_spec(spec):
            if spec.endswith("/first"):
                return {
                    "properties": {
                        "message": {"type": "string"},
                        "url": {"type": "string"},
                    },
                    "required": ["message"],
                }
            return {
                "properties": {
                    "message": {"type": "string"},
                    "expires_at": {"type": "string"},
                },
                "required": ["message", "expires_at"],
            }

        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint.gen, "HAND_WRITTEN_INLINE_SCHEMA_SPECS", specs),
            mock.patch.object(lint, "load_schema_spec", side_effect=load_schema_spec),
        ):
            reports = lint.validate_hand_written_inline_schema_divergence()

        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual("shape.json#/properties/second", reports[0]["schema"])
        self.assertEqual("shape.json#/properties/first", reports[0]["anchor_schema"])
        self.assertEqual(["url"], reports[0]["missing"])
        self.assertEqual(["expires_at"], reports[0]["extra"])
        self.assertEqual([], reports[0]["required_missing"])
        self.assertEqual(["expires_at"], reports[0]["required_extra"])

    @unittest.skipUnless(ACCOUNT_SETUP_SCHEMA_PATHS_EXIST, "account setup schemas are not present")
    def test_current_account_setup_shape_is_drift_checked(self):
        reports = lint.validate_hand_written_inline_schema_specs(lint.parse_go_structs())

        self.assertEqual([], [r for r in reports if r["type"] == "AccountSetup"])

    @unittest.skipUnless(
        CHECK_GOVERNANCE_CONDITION_SCHEMA_PATHS_EXIST,
        "check governance condition schema is not present",
    )
    def test_current_check_governance_condition_shape_is_drift_checked(self):
        reports = lint.validate_hand_written_inline_schema_specs(lint.parse_go_structs())

        self.assertEqual([], [r for r in reports if r["type"] == "CheckGovernanceCondition"])

    def test_hand_written_inline_schema_reports_required_omitempty(self):
        schema_spec = "missing/schema.json#/properties/setup"
        specs = {
            "InlineShape": [schema_spec],
        }
        schema = {
            "properties": {
                "message": {"type": "string"},
            },
            "required": ["message"],
        }

        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint.gen, "HAND_WRITTEN_INLINE_SCHEMA_SPECS", specs),
            mock.patch.object(lint, "load_schema_spec", return_value=schema) as load_schema_spec,
        ):
            reports = lint.validate_hand_written_inline_schema_specs({
                "InlineShape": [("Message", "string", "message", True)],
            })

        load_schema_spec.assert_called_once_with(schema_spec)
        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual(["message"], reports[0]["required_with_omitempty"])

    def test_custom_wire_fields_count_as_present_for_drift(self):
        schema_spec = "missing/schema.json#/properties/condition"
        specs = {
            "InlineShape": [schema_spec],
        }
        schema = {
            "properties": {
                "field": {"type": "string"},
                "required_value": {},
                "reason": {"type": "string"},
            },
            "required": ["field", "reason"],
        }

        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint.gen, "HAND_WRITTEN_INLINE_SCHEMA_SPECS", specs),
            mock.patch.object(lint, "CUSTOM_WIRE_FIELDS", {"InlineShape": {"required_value"}}),
            mock.patch.object(lint, "load_schema_spec", return_value=schema),
        ):
            reports = lint.validate_hand_written_inline_schema_specs({
                "InlineShape": [
                    ("Field", "string", "field", False),
                    ("Reason", "string", "reason", False),
                ],
            })

        self.assertEqual([], reports)

    def test_json_output_exposes_hand_written_inline_drift_separately(self):
        inline_report = {
            "type": "InlineShape",
            "schema": "shape.json#/properties/inline",
            "missing_in_go": ["message"],
            "extra_in_go": [],
            "required_with_omitempty": [],
        }
        schema_divergence_report = {
            "type": "InlineShape",
            "schema": "shape.json#/properties/second",
            "anchor_schema": "shape.json#/properties/first",
            "missing": ["url"],
            "extra": [],
            "required_missing": [],
            "required_extra": [],
            "error": "hand-written inline schema shape differs",
        }

        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint, "parse_go_structs", return_value={}),
            mock.patch.object(lint, "parse_custom_json_methods", return_value={}),
            mock.patch.object(lint, "_assert_exempt_subset_known", return_value=None),
            mock.patch.object(lint, "validate_inline_schema_specs", return_value=[]),
            mock.patch.object(lint, "validate_inline_additional_properties_policies", return_value=[]),
            mock.patch.object(lint, "validate_shared_inline_overrides", return_value=[]),
            mock.patch.object(lint, "validate_cross_type_inline_hints", return_value=[]),
            mock.patch.object(lint, "validate_union_schema_specs", return_value=[]),
            mock.patch.object(lint, "validate_custom_wire_fields", return_value=[]),
            mock.patch.object(lint, "optional_bool_pointer_reports", return_value=[]),
            mock.patch.object(lint, "optional_numeric_pointer_reports", return_value=[]),
            mock.patch.object(
                lint,
                "validate_hand_written_inline_schema_divergence",
                return_value=[schema_divergence_report],
            ),
            mock.patch.object(
                lint,
                "validate_hand_written_inline_schema_specs",
                return_value=[inline_report],
            ),
            mock.patch.object(lint.gen, "KNOWN_TYPES", set()),
            mock.patch.object(sys, "argv", ["lint.py", "--json"]),
        ):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = lint.main()

        self.assertEqual(0, rc)
        payload = json.loads(out.getvalue())
        self.assertEqual([inline_report], payload["hand_written_inline_drift"])
        self.assertEqual(
            [schema_divergence_report],
            payload["hand_written_inline_schema_divergence"],
        )
        self.assertIn(inline_report, payload["drift"])
        self.assertEqual([], payload["optional_bool_pointer"])


class InlineAdditionalPropertiesPolicyLintTest(unittest.TestCase):
    def test_current_inline_additional_properties_policies_are_valid(self):
        reports = lint.validate_inline_additional_properties_policies()

        self.assertEqual([], reports)

    def test_inline_type_missing_policy_is_reported(self):
        with (
            mock.patch.object(lint.gen, "INLINE_SCHEMA_TYPES", {"InlineShape": "shape.json"}),
            mock.patch.object(lint.gen, "CLOSED_INLINE_SCHEMA_TYPES", frozenset()),
            mock.patch.object(lint.gen, "OPEN_INLINE_SCHEMA_TYPES", frozenset()),
        ):
            reports = lint.validate_inline_additional_properties_policies()

        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual(
            "inline type is missing an additionalProperties policy",
            reports[0]["error"],
        )
        self.assertIn("CLOSED_INLINE_SCHEMA_TYPES", reports[0]["remediation"])

    def test_closed_inline_type_fails_when_schema_becomes_open(self):
        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint.gen, "INLINE_SCHEMA_TYPES", {
                "InlineShape": "shape.json#/properties/inline",
            }),
            mock.patch.object(lint.gen, "CLOSED_INLINE_SCHEMA_TYPES", frozenset({
                "InlineShape",
            })),
            mock.patch.object(lint.gen, "OPEN_INLINE_SCHEMA_TYPES", frozenset()),
            mock.patch.object(lint, "load_schema_spec", return_value={
                "type": "object",
                "additionalProperties": True,
            }),
        ):
            reports = lint.validate_inline_additional_properties_policies()

        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual("shape.json#/properties/inline", reports[0]["schema"])
        self.assertEqual(
            "closed inline type schema is not closed to additional properties",
            reports[0]["error"],
        )
        self.assertIn("OPEN_INLINE_SCHEMA_TYPES", reports[0]["remediation"])

    def test_closed_inline_type_allows_closed_oneof_branches(self):
        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint.gen, "INLINE_SCHEMA_TYPES", {
                "InlineShape": "shape.json#/properties/inline",
            }),
            mock.patch.object(lint.gen, "CLOSED_INLINE_SCHEMA_TYPES", frozenset({
                "InlineShape",
            })),
            mock.patch.object(lint.gen, "OPEN_INLINE_SCHEMA_TYPES", frozenset()),
            mock.patch.object(lint, "load_schema_spec", return_value={
                "oneOf": [
                    {
                        "type": "object",
                        "properties": {"scope": {"const": "request"}},
                        "additionalProperties": False,
                    },
                    {
                        "type": "object",
                        "properties": {"scope": {"const": "product"}},
                        "additionalProperties": False,
                    },
                ],
            }),
        ):
            reports = lint.validate_inline_additional_properties_policies()

        self.assertEqual([], reports)

    def test_open_inline_type_fails_when_schema_becomes_closed(self):
        with (
            mock.patch.object(lint.Path, "exists", return_value=True),
            mock.patch.object(lint.gen, "INLINE_SCHEMA_TYPES", {
                "InlineShape": "shape.json#/properties/inline",
            }),
            mock.patch.object(lint.gen, "CLOSED_INLINE_SCHEMA_TYPES", frozenset()),
            mock.patch.object(lint.gen, "OPEN_INLINE_SCHEMA_TYPES", frozenset({
                "InlineShape",
            })),
            mock.patch.object(lint, "load_schema_spec", return_value={
                "type": "object",
                "additionalProperties": False,
            }),
        ):
            reports = lint.validate_inline_additional_properties_policies()

        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual(
            "open inline type schema is closed to additional properties",
            reports[0]["error"],
        )
        self.assertIn("CLOSED_INLINE_SCHEMA_TYPES", reports[0]["remediation"])


class CustomWireFieldLintTest(unittest.TestCase):
    def test_current_custom_wire_fields_are_backed_by_methods(self):
        reports = lint.validate_custom_wire_fields(
            lint.parse_go_structs(),
            lint.parse_custom_json_methods(),
        )

        self.assertEqual([], reports)

    def test_parse_custom_json_methods_finds_value_and_pointer_receivers(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "custom.go"
            source.write_text(
                """
package adcp

func (s InlineShape) MarshalJSON() ([]byte, error) {
	return nil, nil
}

func (s *InlineShape) UnmarshalJSON(data []byte) error {
	return nil
}
"""
            )
            with mock.patch.object(lint, "GO_SOURCE_FILES", [source]):
                methods = lint.parse_custom_json_methods()

        self.assertEqual({"MarshalJSON", "UnmarshalJSON"}, methods["InlineShape"])

    def test_custom_wire_fields_require_known_go_type(self):
        with mock.patch.object(lint, "CUSTOM_WIRE_FIELDS", {
            "InlineShape": {"required_value"},
        }):
            reports = lint.validate_custom_wire_fields({}, {
                "InlineShape": {"MarshalJSON", "UnmarshalJSON"},
            })

        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual(
            "custom wire fields declared for unknown Go type",
            reports[0]["error"],
        )

    def test_custom_wire_fields_require_marshal_and_unmarshal(self):
        with mock.patch.object(lint, "CUSTOM_WIRE_FIELDS", {
            "InlineShape": {"required_value"},
        }):
            reports = lint.validate_custom_wire_fields(
                {"InlineShape": []},
                {"InlineShape": {"MarshalJSON"}},
            )

        self.assertEqual(1, len(reports))
        self.assertEqual("InlineShape", reports[0]["type"])
        self.assertEqual(["required_value"], reports[0]["fields"])
        self.assertEqual(["UnmarshalJSON"], reports[0]["missing_methods"])
        self.assertEqual(
            "custom wire fields require custom JSON methods",
            reports[0]["error"],
        )


class OptionalNumericPointerLintTest(unittest.TestCase):
    def test_zero_invalid_honors_numeric_bounds(self):
        self.assertFalse(lint.numeric_zero_invalid({"type": "number", "minimum": 0}))
        self.assertTrue(lint.numeric_zero_invalid({"type": "number", "minimum": 1}))
        self.assertTrue(lint.numeric_zero_invalid({"type": "number", "exclusiveMinimum": 0}))
        self.assertTrue(lint.numeric_zero_invalid({
            "type": "number",
            "minimum": 0,
            "exclusiveMinimum": True,
        }))

    def test_zero_invalid_ignores_boolean_false_exclusive_minimum(self):
        self.assertFalse(lint.numeric_zero_invalid({
            "type": "number",
            "minimum": 0,
            "exclusiveMinimum": False,
        }))

    def test_omission_hint_uses_defaults_and_descriptions(self):
        self.assertTrue(lint.numeric_omission_is_semantically_distinct({
            "type": "number",
            "default": 1,
        }))
        self.assertFalse(lint.numeric_omission_is_semantically_distinct({
            "type": "number",
            "default": 0,
        }))
        self.assertTrue(lint.numeric_omission_is_semantically_distinct({
            "type": "number",
            "description": "Omit for no maximum.",
        }))
        self.assertTrue(lint.numeric_omission_is_semantically_distinct({
            "type": "number",
            "description": "If omitted, platform determines rotation.",
        }))
        self.assertTrue(lint.numeric_omission_is_semantically_distinct({
            "type": "number",
            "description": "When omitted, inherit the package bid.",
        }))
        self.assertFalse(lint.numeric_omission_is_semantically_distinct({
            "type": "number",
            "description": "Unique reach when reach_unit is omitted.",
        }))

    def test_optional_numeric_pointer_candidate(self):
        prop = {
            "type": "number",
            "minimum": 0,
            "description": "Minimum value. Omit for no minimum.",
        }
        self.assertTrue(lint.optional_numeric_pointer_candidate("min_value", prop, set()))
        self.assertFalse(lint.optional_numeric_pointer_candidate("min_value", prop, {"min_value"}))
        self.assertFalse(lint.optional_numeric_pointer_candidate(
            "view_duration_seconds",
            {"type": "number", "exclusiveMinimum": 0, "description": "Minimum view duration."},
            set(),
        ))

    def test_optional_bool_pointer_candidate(self):
        self.assertTrue(lint.optional_bool_pointer_candidate(
            "emits",
            {"type": "boolean", "default": False},
            set(),
        ))
        self.assertFalse(lint.optional_bool_pointer_candidate(
            "supported",
            {"type": "boolean"},
            {"supported"},
        ))
        self.assertFalse(lint.optional_bool_pointer_candidate(
            "count",
            {"type": "integer"},
            set(),
        ))

    def test_optional_bool_lint_catches_hand_written_bool_omitempty(self):
        schema = {
            "type": "object",
            "properties": {
                "emits": {"type": "boolean", "default": False},
                "supported": {"type": "boolean"},
            },
            "required": ["supported"],
        }

        with (
            mock.patch.object(lint.gen, "generated_schema_entries", return_value=[]),
            mock.patch.object(lint, "resolve_schema_spec", return_value="shape.json"),
            mock.patch.object(lint, "load_schema_spec", return_value=schema),
        ):
            reports = lint.optional_bool_pointer_reports({
                "Shape": [
                    ("Emits", "bool", "emits", True),
                    ("Supported", "bool", "supported", False),
                ],
            })

        self.assertEqual([("Shape", "emits")], [
            (r["type"], r["json"]) for r in reports
        ])

    def test_optional_bool_lint_catches_generated_bool_omitempty(self):
        schema = {
            "type": "object",
            "properties": {
                "emits": {"type": "boolean", "default": False},
                "supported": {"type": "boolean"},
            },
            "required": ["supported"],
        }
        entry = {
            "kind": "struct",
            "name": "GeneratedShape",
            "schema": "generated-shape.json",
            "schema_obj": schema,
        }

        def field_go_type_info(type_name, json_name, prop, required_set):
            return ("bool", None)

        with (
            mock.patch.object(lint.gen, "generated_schema_entries", return_value=[entry]),
            mock.patch.object(lint.gen, "field_go_type_info", side_effect=field_go_type_info),
            mock.patch.object(lint, "resolve_schema_spec", return_value=None),
        ):
            reports = lint.optional_bool_pointer_reports({})

        self.assertEqual([("GeneratedShape", "emits")], [
            (r["type"], r["json"]) for r in reports
        ])

    def test_optional_bool_lint_current_schema_is_clean(self):
        reports = lint.optional_bool_pointer_reports(lint.parse_go_structs())
        # Only fields listed in KNOWN_SCHEMA_GAPS may remain.
        reports = lint.split_known_pointer_reports(
            reports, "optional_bool_pointer", [], set(),
        )
        self.assertEqual([], reports)

    def test_optional_numeric_lint_catches_current_schema_candidates(self):
        reports = lint.optional_numeric_pointer_reports(lint.parse_go_structs())
        report_keys = {(r["type"], r["json"]) for r in reports}

        self.assertNotIn(("CreativeAsset", "weight"), report_keys)
        self.assertNotIn(("KeywordTarget", "bid_price"), report_keys)

        original_hints = lint.gen.INLINE_TYPE_HINTS
        try:
            lint.gen.INLINE_TYPE_HINTS = {
                key: value for key, value in original_hints.items()
                if key not in {
                    ("CreativeAsset", "weight"),
                    ("KeywordTarget", "bid_price"),
                }
            }
            lint.gen._reset_will_generate_cache()

            reports = lint.optional_numeric_pointer_reports(lint.parse_go_structs())
            report_keys = {(r["type"], r["json"]) for r in reports}

            self.assertIn(("CreativeAsset", "weight"), report_keys)
            self.assertIn(("KeywordTarget", "bid_price"), report_keys)
        finally:
            lint.gen.INLINE_TYPE_HINTS = original_hints
            lint.gen._reset_will_generate_cache()

    def test_optional_numeric_lint_honors_explicit_waivers(self):
        original_hints = lint.gen.INLINE_TYPE_HINTS
        original_waivers = lint.OPTIONAL_NUMERIC_SCALAR_OK
        try:
            lint.gen.INLINE_TYPE_HINTS = {
                key: value for key, value in original_hints.items()
                if key != ("CreativeAsset", "weight")
            }
            lint.OPTIONAL_NUMERIC_SCALAR_OK = {
                **original_waivers,
                ("CreativeAsset", "weight"): "test waiver",
            }
            lint.gen._reset_will_generate_cache()

            reports = lint.optional_numeric_pointer_reports(lint.parse_go_structs())
            report_keys = {(r["type"], r["json"]) for r in reports}

            self.assertNotIn(("CreativeAsset", "weight"), report_keys)
        finally:
            lint.gen.INLINE_TYPE_HINTS = original_hints
            lint.OPTIONAL_NUMERIC_SCALAR_OK = original_waivers
            lint.gen._reset_will_generate_cache()


class RequiredPropertiesDeclaredLintTest(unittest.TestCase):
    @unittest.skipUnless(VALIDATE_INPUT_RESULT_SCHEMA_EXISTS, "validate_input schemas are not present")
    def test_current_generated_structs_declare_their_required_properties(self):
        self.assertEqual([], lint.validate_required_properties_declared())

    @unittest.skipUnless(VALIDATE_INPUT_RESULT_SCHEMA_EXISTS, "validate_input schemas are not present")
    def test_validate_input_warning_still_requires_an_undeclared_field(self):
        schema = lint.load_schema_spec(VALIDATE_INPUT_WARNING_SCHEMA_SPEC)
        undeclared = set(lint.gen.schema_required_names(schema)) - set(lint.gen.schema_properties(schema))
        message = (
            "the validate_input warning shape changed upstream: once it declares field, move "
            "ValidateInputWarning back from types.go and KNOWN_TYPES to INLINE_SCHEMA_TYPES"
        )
        self.assertEqual({"field"}, undeclared, message)
        self.assertEqual({"rule", "expected", "predicted"}, set(lint.gen.schema_properties(schema)), message)

    def test_required_but_undeclared_property_is_reported(self):
        schema = {
            "type": "object",
            "required": ["rule", "field"],
            "properties": {"rule": {"type": "string"}},
        }
        entries = [{"name": "WarningItem", "schema": "x.json#/items", "schema_obj": schema, "kind": "struct"}]
        with mock.patch.object(lint.gen, "generated_schema_entries", return_value=entries):
            reports = lint.validate_required_properties_declared()
        self.assertEqual(1, len(reports))
        self.assertEqual("WarningItem", reports[0]["type"])
        self.assertEqual(["field"], reports[0]["undeclared"])

    def test_required_declared_in_an_allof_branch_is_accepted(self):
        schema = {
            "type": "object",
            "required": ["rule", "field"],
            "allOf": [{"properties": {"field": {"type": "string"}}}],
            "properties": {"rule": {"type": "string"}},
        }
        entries = [{"name": "Composed", "schema": "y.json", "schema_obj": schema, "kind": "struct"}]
        with mock.patch.object(lint.gen, "generated_schema_entries", return_value=entries):
            self.assertEqual([], lint.validate_required_properties_declared())


CREATIVE_INPUT_SCHEMA_SPEC = lint.gen.HAND_WRITTEN_SCHEMA_SPECS["CreativeInput"]
CREATIVE_INPUT_SCHEMA_EXISTS = (
    lint.SCRIPT_DIR / CREATIVE_INPUT_SCHEMA_SPEC.split("#", 1)[0]
).exists()


class KnownSchemaGapsLintTest(unittest.TestCase):
    SCHEMA = {
        "type": "object",
        "required": ["id", "name"],
        "properties": {
            "id": {"type": "string"},
            "name": {"type": "string"},
            "tags": {"type": "array"},
            "status": {"type": "string"},
        },
    }
    GO_FIELDS = [
        ("ID", "string", "id", False),
        ("Name", "string", "name", True),
        ("Legacy", "string", "legacy", True),
    ]

    def run_strict_lint(self, known_gaps, inline_drift=None):
        """Run lint.main() --strict --json on one Shape type.

        Shape is paired through HAND_WRITTEN_SCHEMA_SPECS, or, when
        `inline_drift` is given, only through the inline registry whose
        diff reports are `inline_drift`.
        """
        empty = [
            "validate_inline_schema_specs",
            "validate_inline_additional_properties_policies",
            "validate_shared_inline_overrides",
            "validate_cross_type_inline_hints",
            "validate_union_schema_specs",
            "validate_required_properties_declared",
            "validate_custom_wire_fields",
            "optional_bool_pointer_reports",
            "optional_numeric_pointer_reports",
            "validate_hand_written_inline_schema_divergence",
            "validate_exempt_config",
            "validate_known_gaps_bundle",
        ]
        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.object(
                lint, "validate_hand_written_inline_schema_specs",
                return_value=inline_drift or [],
            ))
            for name in empty:
                stack.enter_context(mock.patch.object(lint, name, return_value=[]))
            for target, attr, value in (
                (lint.Path, "exists", True),
                (lint, "parse_go_structs", {"Shape": self.GO_FIELDS}),
                (lint, "parse_custom_json_methods", {}),
                (lint, "_assert_exempt_subset_known", None),
                (lint, "load_schema_spec", self.SCHEMA),
            ):
                stack.enter_context(mock.patch.object(target, attr, return_value=value))
            explicit = {} if inline_drift else {"Shape": "shape.json"}
            stack.enter_context(mock.patch.object(lint, "EXPLICIT_SCHEMA", explicit))
            stack.enter_context(mock.patch.object(lint, "EXEMPT", {"Shape"} if inline_drift else set()))
            stack.enter_context(mock.patch.object(lint, "KNOWN_SCHEMA_GAPS", known_gaps))
            stack.enter_context(mock.patch.object(lint.gen, "KNOWN_TYPES", {"Shape"}))
            stack.enter_context(mock.patch.object(sys, "argv", ["lint.py", "--strict", "--json"]))
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = lint.main()
        return rc, json.loads(out.getvalue())

    ALL_GAPS = {
        "Shape": {
            "missing_in_go": {"tags": "#901", "status": "#901"},
            "extra_in_go": {"legacy": "#545"},
            "required_with_omitempty": {"name": "adcontextprotocol/adcp#8212"},
        },
    }

    def test_pairing_reports_each_missing_field(self):
        rc, payload = self.run_strict_lint({})

        self.assertEqual(1, rc)
        self.assertEqual(1, len(payload["drift"]))
        drift = payload["drift"][0]
        self.assertEqual("Shape", drift["type"])
        self.assertEqual(["status", "tags"], drift["missing_in_go"])
        self.assertEqual(["legacy"], drift["extra_in_go"])
        self.assertEqual(["name"], drift["required_with_omitempty"])

    def test_listed_gaps_pass_strict_and_stay_reported(self):
        rc, payload = self.run_strict_lint(self.ALL_GAPS)

        self.assertEqual(0, rc)
        self.assertEqual([], payload["drift"])
        self.assertEqual([], payload["known_schema_gap_errors"])
        self.assertEqual(
            {
                ("missing_in_go", "status", "#901"),
                ("missing_in_go", "tags", "#901"),
                ("extra_in_go", "legacy", "#545"),
                ("required_with_omitempty", "name", "adcontextprotocol/adcp#8212"),
            },
            {(g["kind"], g["json"], g["ref"]) for g in payload["known_schema_gaps"]},
        )

    def test_known_gap_suppresses_only_its_own_field(self):
        gaps = {"Shape": {"missing_in_go": {"tags": "#901"}}}
        rc, payload = self.run_strict_lint(gaps)

        self.assertEqual(1, rc)
        drift = payload["drift"][0]
        self.assertEqual(["status"], drift["missing_in_go"])
        self.assertEqual(["legacy"], drift["extra_in_go"])
        self.assertEqual(["name"], drift["required_with_omitempty"])

    def test_known_gap_applies_to_hand_written_inline_drift(self):
        inline_drift = {
            "type": "Shape",
            "schema": "parent.json#/properties/shape",
            "missing_in_go": ["tags", "status"],
            "extra_in_go": [],
            "required_with_omitempty": [],
        }
        gaps = {"Shape": {"missing_in_go": {"tags": "#901"}}}
        rc, payload = self.run_strict_lint(gaps, inline_drift=[inline_drift])

        self.assertEqual(1, rc)
        self.assertEqual(["status"], payload["hand_written_inline_drift"][0]["missing_in_go"])
        self.assertEqual(
            [("Shape", "missing_in_go", "tags")],
            [(g["type"], g["kind"], g["json"]) for g in payload["known_schema_gaps"]],
        )
        self.assertEqual([], payload["known_schema_gap_errors"])

    def test_known_gap_is_scoped_to_its_kind_and_type(self):
        gaps = {
            "Shape": {"missing_in_go": {"legacy": "#901"}},
            "Other": {"missing_in_go": {"tags": "#901"}},
        }
        known = []
        matched = set()
        drift = {
            "type": "Shape",
            "schema": "shape.json",
            "missing_in_go": ["tags"],
            "extra_in_go": ["legacy"],
            "required_with_omitempty": [],
        }
        with mock.patch.object(lint, "KNOWN_SCHEMA_GAPS", gaps):
            remaining = lint.split_known_drift(drift, known, matched)

        self.assertEqual(["tags"], remaining["missing_in_go"])
        self.assertEqual(["legacy"], remaining["extra_in_go"])
        self.assertEqual([], known)
        self.assertEqual(set(), matched)

    def test_stale_entry_fails(self):
        gaps = {
            "Shape": {
                **self.ALL_GAPS["Shape"],
                "missing_in_go": {**self.ALL_GAPS["Shape"]["missing_in_go"], "id": "#901"},
            },
        }
        rc, payload = self.run_strict_lint(gaps)

        self.assertEqual(1, rc)
        self.assertEqual([], payload["drift"])
        errors = payload["known_schema_gap_errors"]
        self.assertEqual(
            [("Shape", "missing_in_go", "id")],
            [(e["type"], e["kind"], e.get("json")) for e in errors],
        )
        self.assertIn("stale", errors[0]["error"])

    def test_entries_for_an_unpaired_type_fail(self):
        gaps = {**self.ALL_GAPS, "Unpaired": {"missing_in_go": {"x": "#901"}}}
        rc, payload = self.run_strict_lint(gaps)

        self.assertEqual(1, rc)
        self.assertEqual(
            [("Unpaired", "all")],
            [(e["type"], e["kind"]) for e in payload["known_schema_gap_errors"]],
        )

    def test_entry_without_issue_reference_fails(self):
        for ref in ("", "TODO", "564", "#", "#0", "#901\n", " #901", "https://example.com/1", None):
            with self.subTest(ref=ref):
                gaps = {
                    "Shape": {
                        **self.ALL_GAPS["Shape"],
                        "missing_in_go": {"tags": "#901", "status": ref},
                    },
                }
                rc, payload = self.run_strict_lint(gaps)

                self.assertEqual(1, rc)
                # The finding stays reported: an entry without an owner
                # suppresses nothing.
                self.assertEqual(["status"], payload["drift"][0]["missing_in_go"])
                self.assertEqual(
                    [("Shape", "status")],
                    [(e["type"], e.get("json")) for e in payload["known_schema_gap_errors"]],
                )

    def test_an_umbrella_owner_fails_and_suppresses_nothing(self):
        gaps = {
            "Shape": {
                **self.ALL_GAPS["Shape"],
                "missing_in_go": {"tags": "#564", "status": "#901"},
            },
        }
        rc, payload = self.run_strict_lint(gaps)

        self.assertEqual(1, rc)
        self.assertEqual(["tags"], payload["drift"][0]["missing_in_go"])
        errors = payload["known_schema_gap_errors"]
        self.assertEqual([("Shape", "tags")], [(e["type"], e.get("json")) for e in errors])
        self.assertIn("umbrella", errors[0]["error"])

    def test_a_cross_repository_issue_is_not_an_umbrella(self):
        gaps = {"Shape": {**self.ALL_GAPS["Shape"], "missing_in_go": {"tags": "other/repo#564", "status": "#901"}}}
        rc, payload = self.run_strict_lint(gaps)

        self.assertEqual(0, rc)
        self.assertEqual([], payload["known_schema_gap_errors"])

    def test_unknown_kind_fails(self):
        gaps = {"Shape": {**self.ALL_GAPS["Shape"], "missing": {"tags": "#901"}}}
        rc, payload = self.run_strict_lint(gaps)

        self.assertEqual(1, rc)
        self.assertEqual(
            [("Shape", "missing")],
            [(e["type"], e["kind"]) for e in payload["known_schema_gap_errors"]],
        )

    def test_pointer_report_gap_suppresses_only_hand_written_field(self):
        reports = [
            {"owner": "hand-written", "type": "Shape", "json": "sandbox"},
            {"owner": "hand-written", "type": "Shape", "json": "paused"},
            {"owner": "generated", "type": "Shape", "json": "sandbox"},
        ]
        known = []
        matched = set()
        gaps = {"Shape": {"optional_bool_pointer": {"sandbox": "#901"}}}
        with mock.patch.object(lint, "KNOWN_SCHEMA_GAPS", gaps):
            kept = lint.split_known_pointer_reports(
                reports, "optional_bool_pointer", known, matched,
            )
            kept_numeric = lint.split_known_pointer_reports(
                reports, "optional_numeric_pointer", [], set(),
            )

        self.assertEqual(reports[1:], kept)
        self.assertEqual(reports, kept_numeric)
        self.assertEqual({("Shape", "optional_bool_pointer", "sandbox")}, matched)

    def test_current_known_gaps_have_issue_references(self):
        self.assertEqual([], lint.validate_known_schema_gaps_config())

    def test_a_plain_umbrella_never_owns_an_entry(self):
        owners = [
            ref
            for kinds in lint.KNOWN_SCHEMA_GAPS.values()
            for fields in kinds.values()
            for ref in fields.values()
        ] + [ref for _, ref in lint.EXEMPT_UNPAIRED.values()]

        self.assertTrue(owners)
        for ref in owners:
            self.assertNotIn(ref, lint.UMBRELLA_ISSUES)

    def test_known_gap_types_are_paired(self):
        for type_name in lint.KNOWN_SCHEMA_GAPS:
            with self.subTest(type=type_name):
                paired = (
                    type_name in lint.EXPLICIT_SCHEMA and type_name not in lint.EXEMPT
                ) or type_name in lint.gen.HAND_WRITTEN_INLINE_SCHEMA_SPECS
                self.assertTrue(paired, f"{type_name} has known gaps but is not paired")

    @unittest.skipUnless(CREATIVE_INPUT_SCHEMA_EXISTS, "sync_creatives schema is not present")
    def test_creative_input_pairing_reports_dropped_request_fields(self):
        go_structs = lint.parse_go_structs()
        drift = lint.diff_type(
            "CreativeInput", go_structs["CreativeInput"], CREATIVE_INPUT_SCHEMA_SPEC,
        )

        for field in ("format_kind", "tags", "representation_selection"):
            self.assertIn(field, drift["missing_in_go"])


COMMITMENT_RESPONSE_SCHEMA_EXISTS = (
    lint.SCRIPT_DIR / "media-buy" / "media-buy-commitment-response.json"
).exists()


class ObjectRefScalarAliasLintTest(unittest.TestCase):
    """adcp-go#901 check 3: an object $ref must not be typed as a scalar alias."""

    def setUp(self):
        lint.gen._reset_will_generate_cache()

    def tearDown(self):
        lint.gen._reset_will_generate_cache()

    def entries(self):
        return [{
            "kind": "struct",
            "name": "Holder",
            "schema": "holder.json",
            "schema_obj": {
                "properties": {
                    "things": {"type": "array", "items": {"$ref": "/schemas/3.2.2/core/shape.json"}},
                    "name": {"type": "string"},
                },
            },
        }]

    def test_object_ref_bound_to_a_scalar_alias_is_reported(self):
        with (
            mock.patch.object(lint.gen, "generated_schema_entries", return_value=self.entries()),
            mock.patch.object(lint.gen, "resolve_ref_schema", return_value={"type": "object"}),
            mock.patch.object(lint.gen, "scalar_alias_go_names", return_value={"Shape"}),
        ):
            reports = lint.validate_object_refs_not_scalar_aliases()

        self.assertEqual(1, len(reports))
        self.assertEqual("Holder", reports[0]["type"])
        self.assertEqual("things", reports[0]["json"])
        self.assertEqual("Shape", reports[0]["go_type"])
        self.assertEqual("holder.json", reports[0]["schema"])
        self.assertIn("REF_ALIASES", reports[0]["error"])

    def test_object_ref_with_its_own_go_name_is_clean(self):
        with (
            mock.patch.object(lint.gen, "generated_schema_entries", return_value=self.entries()),
            mock.patch.object(lint.gen, "resolve_ref_schema", return_value={"type": "object"}),
            mock.patch.object(lint.gen, "scalar_alias_go_names", return_value={"Other"}),
        ):
            self.assertEqual([], lint.validate_object_refs_not_scalar_aliases())

    def test_scalar_ref_bound_to_a_scalar_alias_is_clean(self):
        with (
            mock.patch.object(lint.gen, "generated_schema_entries", return_value=self.entries()),
            mock.patch.object(lint.gen, "resolve_ref_schema", return_value={"type": "string"}),
            mock.patch.object(lint.gen, "scalar_alias_go_names", return_value={"Shape"}),
        ):
            self.assertEqual([], lint.validate_object_refs_not_scalar_aliases())

    def test_non_struct_entries_are_skipped(self):
        entries = [dict(self.entries()[0], kind="union_alias")]
        with (
            mock.patch.object(lint.gen, "generated_schema_entries", return_value=entries),
            mock.patch.object(lint.gen, "resolve_ref_schema", return_value={"type": "object"}),
            mock.patch.object(lint.gen, "scalar_alias_go_names", return_value={"Shape"}),
        ):
            self.assertEqual([], lint.validate_object_refs_not_scalar_aliases())

    @unittest.skipUnless(COMMITMENT_RESPONSE_SCHEMA_EXISTS, "commitment response schema is not present")
    def test_current_generated_structs_bind_no_object_ref_to_a_scalar_alias(self):
        self.assertEqual([], lint.validate_object_refs_not_scalar_aliases())

    @unittest.skipUnless(COMMITMENT_RESPONSE_SCHEMA_EXISTS, "commitment response schema is not present")
    def test_guard_fires_on_the_tables_from_before_the_fix(self):
        # main kept CanonicalMediaBuyAction in KNOWN_TYPES, so the generator
        # trusted the name and typed available_actions as []string.
        aliases = {
            key: value for key, value in lint.gen.REF_ALIASES.items()
            if key != "CanonicalMediaBuyAction"
        }
        known = set(lint.gen.KNOWN_TYPES) | {"CanonicalMediaBuyAction"}
        with (
            mock.patch.object(lint.gen, "REF_ALIASES", aliases),
            mock.patch.object(lint.gen, "KNOWN_TYPES", known),
        ):
            lint.gen._reset_will_generate_cache()
            reports = lint.validate_object_refs_not_scalar_aliases()

        # control_media_buy's completed variant lists the same objects.
        self.assertEqual(
            [
                ("CommittedMediaBuy", "available_actions", "CanonicalMediaBuyAction"),
                ("ControlApplied", "available_actions", "CanonicalMediaBuyAction"),
            ],
            sorted((r["type"], r["json"], r["go_type"]) for r in reports),
        )

    @unittest.skipUnless(COMMITMENT_RESPONSE_SCHEMA_EXISTS, "commitment response schema is not present")
    def test_strict_lint_exits_nonzero_for_a_bound_object_ref(self):
        report = {"type": "Holder", "schema": "h.json", "json": "things", "ref": "r", "go_type": "Shape", "error": "e"}
        with (
            mock.patch.object(lint, "validate_object_refs_not_scalar_aliases", return_value=[report]),
            mock.patch.object(sys, "argv", ["lint.py", "--strict"]),
            contextlib.redirect_stdout(io.StringIO()) as out,
        ):
            exit_code = lint.main()

        self.assertEqual(1, exit_code)
        self.assertIn("Object $ref bound to a scalar Go type in 1 field(s)", out.getvalue())


PLAN_BUDGET_SCHEMA_SPEC = lint.gen.HAND_WRITTEN_SCHEMA_SPECS["PlanBudget"]
PLAN_BUDGET_SCHEMA_EXISTS = (
    lint.SCRIPT_DIR / PLAN_BUDGET_SCHEMA_SPEC.split("#", 1)[0]
).exists()


class ExemptConfigLintTest(unittest.TestCase):
    """adcp-go#901, check 4: EXEMPT holds only what lint cannot compare."""

    def validate(self, no_schema=None, unpaired=None, structs=("Thing",), paired=None):
        go_structs = {name: [] for name in structs}
        with (
            mock.patch.object(lint, "EXEMPT_NO_SCHEMA", no_schema or {}),
            mock.patch.object(lint, "EXEMPT_UNPAIRED", unpaired or {}),
            mock.patch.object(lint, "EXPLICIT_SCHEMA", paired or {}),
            mock.patch.object(lint.gen, "HAND_WRITTEN_INLINE_SCHEMA_SPECS", {}),
        ):
            return lint.validate_exempt_config(go_structs)

    def test_current_lists_are_clean(self):
        self.assertEqual([], lint.validate_exempt_config(lint.parse_go_structs()))

    def test_exempt_is_the_union_of_both_lists(self):
        self.assertEqual(
            set(lint.EXEMPT_NO_SCHEMA) | set(lint.EXEMPT_UNPAIRED), lint.EXEMPT,
        )

    def test_a_valid_entry_in_each_list_is_clean(self):
        self.assertEqual([], self.validate(
            no_schema={"Thing": "a handler table"},
            unpaired={"Other": ("blocked on the rework", "#901")},
            structs=("Thing", "Other"),
        ))

    def test_an_entry_that_is_no_scanned_struct_fails(self):
        # A response-builder func and a generated type are not structs lint scans.
        for name in ("SyncCreativesResponse", "DeliveryTotals"):
            with self.subTest(name=name):
                reports = self.validate(no_schema={name: "because"}, structs=("Thing",))
                self.assertEqual([name], [r["type"] for r in reports])
                self.assertIn("not a struct", reports[0]["error"])

    def test_an_entry_that_is_also_paired_fails(self):
        reports = self.validate(
            unpaired={"Thing": ("blocked", "#901")}, paired={"Thing": "thing.json"},
        )
        self.assertEqual(1, len(reports))
        self.assertIn("paired", reports[0]["error"])

    def test_an_entry_in_both_lists_fails(self):
        reports = self.validate(
            no_schema={"Thing": "a"}, unpaired={"Thing": ("b", "#901")},
        )
        self.assertIn("both", [r["list"] for r in reports])

    def test_an_entry_without_a_reason_fails(self):
        for reason in ("", "  ", None):
            with self.subTest(reason=reason):
                self.assertEqual(
                    ["needs a reason"],
                    [r["error"] for r in self.validate(no_schema={"Thing": reason})],
                )
                self.assertEqual(
                    ["needs a reason"],
                    [r["error"] for r in self.validate(unpaired={"Thing": (reason, "#901")})],
                )

    def test_an_unpaired_entry_without_an_issue_fails(self):
        for ref in ("", "TODO-issue", "564", "#0", None):
            with self.subTest(ref=ref):
                reports = self.validate(unpaired={"Thing": ("blocked", ref)})
                self.assertEqual(1, len(reports))
                self.assertIn("issue", reports[0]["error"])

    def test_an_unpaired_entry_owned_by_an_umbrella_fails(self):
        reports = self.validate(unpaired={"Thing": ("blocked", "#564")})

        self.assertEqual(1, len(reports))
        self.assertIn("umbrella", reports[0]["error"])

    def test_an_unpaired_entry_accepts_a_cross_repository_issue(self):
        self.assertEqual(
            [], self.validate(unpaired={"Thing": ("blocked", "adcontextprotocol/adcp#8212")}),
        )

    def test_a_no_schema_entry_with_a_schema_file_by_its_name_fails(self):
        with mock.patch.object(lint, "resolve_schema_spec", return_value="core/thing.json"):
            reports = self.validate(no_schema={"Thing": "because"})
        self.assertEqual(1, len(reports))
        self.assertIn("pair it", reports[0]["error"])

    def test_each_current_unpaired_entry_carries_its_reason_and_issue(self):
        for name, (reason, ref) in lint.EXEMPT_UNPAIRED.items():
            with self.subTest(name=name):
                self.assertTrue(reason.strip())
                self.assertRegex(ref, lint.ISSUE_REF_RE)

    def test_strict_lint_exits_nonzero_for_an_exempt_error(self):
        report = {"type": "Gone", "list": "EXEMPT_NO_SCHEMA", "error": "not a struct"}
        with (
            mock.patch.object(lint, "validate_exempt_config", return_value=[report]),
            mock.patch.object(sys, "argv", ["lint.py", "--strict"]),
            contextlib.redirect_stdout(io.StringIO()) as out,
        ):
            exit_code = lint.main()

        self.assertEqual(1, exit_code)
        self.assertIn("EXEMPT errors (1)", out.getvalue())


class KnownGapsBundleLintTest(unittest.TestCase):
    """The gap table is a baseline of one bundle, which schemas/VERSION names."""

    def test_current_baseline_matches_the_bundle_version(self):
        self.assertEqual([], lint.validate_known_gaps_bundle())

    def test_a_different_bundle_fails(self):
        with mock.patch.object(lint, "KNOWN_SCHEMA_GAPS_BUNDLE", "0.0.0"):
            reports = lint.validate_known_gaps_bundle()

        self.assertEqual(1, len(reports))
        self.assertIn("0.0.0", reports[0]["error"])
        self.assertIn((lint.SCRIPT_DIR / "VERSION").read_text().strip(), reports[0]["error"])

    def test_an_unreadable_version_file_fails(self):
        with mock.patch.object(lint.Path, "read_text", side_effect=OSError("gone")):
            reports = lint.validate_known_gaps_bundle()

        self.assertEqual(1, len(reports))
        self.assertIn("cannot read", reports[0]["error"])

    def test_a_stale_baseline_fails_strict_lint(self):
        with (
            mock.patch.object(lint, "KNOWN_SCHEMA_GAPS_BUNDLE", "0.0.0"),
            mock.patch.object(sys, "argv", ["lint.py", "--strict"]),
            contextlib.redirect_stdout(io.StringIO()) as out,
        ):
            exit_code = lint.main()

        self.assertEqual(1, exit_code)
        self.assertIn("baselined on bundle 0.0.0", out.getvalue())


@unittest.skipUnless(PLAN_BUDGET_SCHEMA_EXISTS, "sync_plans schema is not present")
class PairedTypeDriftLintTest(unittest.TestCase):
    """A hand-written type that left EXEMPT fails strict lint when it drifts."""

    PAIRED_FROM_EXEMPT = (
        "PlanBudget", "PlanDelegationBudget", "PlanPortfolioBudgetCap",
        "ReportingPeriod", "ReportingReceiptResult", "CancellationFee",
        "BillingMeasurement", "BaseCollectionSource", "SignalID", "ActivationKey",
    )

    def run_strict(self, go_structs):
        with (
            mock.patch.object(lint, "parse_go_structs", return_value=go_structs),
            mock.patch.object(sys, "argv", ["lint.py", "--strict", "--json"]),
            contextlib.redirect_stdout(io.StringIO()) as out,
        ):
            rc = lint.main()
        return rc, json.loads(out.getvalue())

    def test_pairing_the_real_sources_passes(self):
        rc, payload = self.run_strict(lint.parse_go_structs())

        self.assertEqual(0, rc, payload["drift"])

    def test_each_type_that_left_exempt_is_compared(self):
        structs = lint.parse_go_structs()
        for name in self.PAIRED_FROM_EXEMPT:
            with self.subTest(name=name):
                self.assertNotIn(name, lint.EXEMPT)
                self.assertTrue(
                    name in lint.EXPLICIT_SCHEMA
                    or lint.resolve_schema_spec(name) is not None,
                )
                self.assertIn(name, structs)

    def test_a_field_dropped_from_a_paired_type_fails(self):
        for name, dropped in (
            ("PlanBudget", "currency"),
            ("PlanDelegationBudget", "amount"),
            ("ReportingPeriod", "end"),
            ("ReportingReceiptResult", "errors"),
            ("BillingMeasurement", "vendor"),
        ):
            with self.subTest(name=name, dropped=dropped):
                structs = lint.parse_go_structs()
                structs[name] = [f for f in structs[name] if f[2] != dropped]

                rc, payload = self.run_strict(structs)

                self.assertEqual(1, rc)
                missing = {
                    (r["type"], f) for r in payload["drift"] for f in r["missing_in_go"]
                }
                self.assertIn((name, dropped), missing)

    def test_a_field_added_to_a_paired_type_fails(self):
        structs = lint.parse_go_structs()
        structs["PlanBudget"] = structs["PlanBudget"] + [
            ("Surplus", "string", "surplus", True),
        ]

        rc, payload = self.run_strict(structs)

        self.assertEqual(1, rc)
        extra = {(r["type"], f) for r in payload["drift"] for f in r["extra_in_go"]}
        self.assertIn(("PlanBudget", "surplus"), extra)

    def test_a_required_field_made_omitempty_fails(self):
        structs = lint.parse_go_structs()
        structs["PlanBudget"] = [
            (n, t, tag, True if tag == "total" else omit)
            for n, t, tag, omit in structs["PlanBudget"]
        ]

        rc, payload = self.run_strict(structs)

        self.assertEqual(1, rc)
        flagged = {
            (r["type"], f) for r in payload["drift"] for f in r["required_with_omitempty"]
        }
        self.assertIn(("PlanBudget", "total"), flagged)

    def test_without_the_envelope_subtraction_the_envelope_is_missing_in_go(self):
        with mock.patch.object(lint, "ENVELOPE_PAYLOAD_TYPES", set()):
            rc, payload = self.run_strict(lint.parse_go_structs())

        self.assertEqual(1, rc)
        missing = {(r["type"], f) for r in payload["drift"] for f in r["missing_in_go"]}
        self.assertIn(("ProductsData", "task_id"), missing)
        self.assertIn(("DeliveryData", "replayed"), missing)

    def test_a_known_gap_that_closes_fails_as_stale(self):
        structs = lint.parse_go_structs()
        structs["PreviewRender"] = [f for f in structs["PreviewRender"] if f[2] != "html"]

        rc, payload = self.run_strict(structs)

        self.assertEqual(1, rc)
        stale = {
            (e["type"], e.get("json")) for e in payload["known_schema_gap_errors"]
        }
        self.assertIn(("PreviewRender", "html"), stale)


class EnvelopePayloadLintTest(unittest.TestCase):
    """DeliveryData and ProductsData are compared without the protocol envelope."""

    SCHEMA = {
        "type": "object",
        "required": ["products", "task_id"],
        "properties": {
            "products": {"type": "array"},
            "pagination": {"type": "object"},
            "status": {"type": "string"},
            "task_id": {"type": "string"},
        },
    }
    GO_FIELDS = [
        ("Products", "[]Product", "products", False),
        ("Status", "string", "status", False),
    ]

    def diff(self, envelope_types, go_fields=None):
        with (
            mock.patch.object(lint, "ENVELOPE_PAYLOAD_TYPES", envelope_types),
            mock.patch.object(lint, "protocol_envelope_properties", return_value={"status", "task_id"}),
            mock.patch.object(lint, "load_schema_spec", return_value=self.SCHEMA),
        ):
            return lint.diff_type("Shape", go_fields or self.GO_FIELDS, "core/product.json")

    def test_envelope_properties_are_not_missing_from_the_payload_type(self):
        drift = self.diff({"Shape"})

        self.assertEqual(["pagination"], drift["missing_in_go"])
        self.assertEqual([], drift["extra_in_go"])

    def test_a_type_outside_the_set_is_compared_with_the_envelope(self):
        drift = self.diff(set())

        self.assertEqual(["pagination", "task_id"], drift["missing_in_go"])

    def test_a_payload_field_dropped_still_fails(self):
        drift = self.diff({"Shape"}, [("Status", "string", "status", False)])

        self.assertEqual(["pagination", "products"], drift["missing_in_go"])

    def test_the_envelope_comes_from_the_schema_file(self):
        optional = lint.protocol_envelope_properties()

        self.assertTrue({"task_id", "replayed", "message"} <= optional)

    def test_the_required_envelope_properties_are_not_subtracted(self):
        envelope = lint.load_schema(lint.SCRIPT_DIR / lint.ENVELOPE_SCHEMA)

        self.assertEqual(["status"], envelope["required"])
        self.assertNotIn("status", lint.protocol_envelope_properties())

    def test_a_required_envelope_property_the_type_lacks_is_missing(self):
        schema = {
            "type": "object",
            "required": ["status"],
            "properties": {"products": {"type": "array"}, "status": {"type": "string"}},
        }
        with (
            mock.patch.object(lint, "ENVELOPE_PAYLOAD_TYPES", {"Shape"}),
            mock.patch.object(lint, "protocol_envelope_properties", return_value={"task_id"}),
            mock.patch.object(lint, "load_schema_spec", return_value=schema),
        ):
            drift = lint.diff_type(
                "Shape", [("Products", "[]Product", "products", False)], "core/product.json",
            )

        self.assertEqual(["status"], drift["missing_in_go"])

    def test_the_payload_types_are_the_two_response_data_types(self):
        self.assertEqual({"DeliveryData", "ProductsData"}, lint.ENVELOPE_PAYLOAD_TYPES)


class WaiverDerivationLintTest(unittest.TestCase):
    """Findings the schema itself shows to be false positives are not reported."""

    def test_a_boolean_defaulting_to_false_keeps_its_pointer(self):
        self.assertFalse(lint.bool_false_is_omission("f", {"type": "boolean", "default": False}, {}))

    def test_a_boolean_pinned_to_true_needs_no_pointer(self):
        self.assertTrue(lint.bool_false_is_omission("f", {"type": "boolean", "const": True}, {}))

    def test_a_boolean_pinned_or_forbidden_by_every_branch_needs_no_pointer(self):
        schema = {"oneOf": [
            {"required": ["a"], "not": {"required": ["f"]}},
            {"required": ["f"], "properties": {"f": {"const": True}}, "not": {"required": ["a"]}},
        ]}
        self.assertTrue(lint.bool_false_is_omission("f", {"type": "boolean"}, schema))

    def test_a_branch_that_leaves_the_boolean_free_keeps_its_pointer(self):
        schema = {"oneOf": [
            {"required": ["f"], "properties": {"f": {"type": "boolean"}}},
            {"required": ["f"], "properties": {"f": {"const": True}}},
        ]}
        self.assertFalse(lint.bool_false_is_omission("f", {"type": "boolean"}, schema))

    def test_a_branch_that_allows_false_keeps_its_pointer(self):
        schema = {"oneOf": [{"properties": {"f": {"const": False}}}, {"properties": {"f": {"const": True}}}]}
        self.assertFalse(lint.bool_false_is_omission("f", {"type": "boolean"}, schema))

    def test_branches_that_only_forbid_the_boolean_do_not_waive_it(self):
        schema = {"oneOf": [{"not": {"required": ["f"]}}]}
        self.assertFalse(lint.bool_false_is_omission("f", {"type": "boolean"}, schema))

    def test_a_boolean_no_branch_mentions_keeps_its_pointer(self):
        self.assertFalse(lint.bool_false_is_omission("f", {"type": "boolean"}, {"oneOf": [{"required": ["g"]}]}))
        self.assertFalse(lint.bool_false_is_omission("f", {"type": "boolean"}, {}))

    def test_a_zero_the_schema_rejects_is_not_a_loss(self):
        for prop in (
            {"type": "integer", "minimum": 1},
            {"type": "string", "minLength": 1},
            {"type": "string", "enum": ["a", "b"]},
            {"type": "array", "minItems": 1},
            {"type": "object", "minProperties": 1},
            {"type": "string", "const": "x"},
        ):
            with self.subTest(prop=prop):
                self.assertTrue(lint.go_zero_is_schema_invalid(prop))

    def test_a_zero_the_schema_accepts_is_a_loss(self):
        for prop in (
            {"type": "integer", "minimum": 0},
            {"type": "integer"},
            {"type": "string"},
            {"type": "string", "enum": ["", "a"]},
            {"type": "array"},
            {"type": ["string", "null"], "format": "date-time"},
            {"type": "boolean"},
            {"type": ["string", "integer"], "minLength": 1},
            None,
        ):
            with self.subTest(prop=prop):
                self.assertFalse(lint.go_zero_is_schema_invalid(prop))

    def test_a_ref_to_an_enum_is_followed(self):
        enum = {"type": "string", "enum": ["a", "b"]}
        with mock.patch.object(lint, "_resolve_ref", return_value=enum):
            self.assertTrue(lint.go_zero_is_schema_invalid({"$ref": "/schemas/3.2.2/core/x.json"}))

    def test_diff_type_waives_only_the_invalid_zero(self):
        schema = {
            "type": "object",
            "required": ["revision", "name"],
            "properties": {
                "revision": {"type": "integer", "minimum": 1},
                "name": {"type": "string"},
            },
        }
        go_fields = [
            ("Revision", "int", "revision", True),
            ("Name", "string", "name", True),
        ]
        with mock.patch.object(lint, "load_schema_spec", return_value=schema):
            drift = lint.diff_type("Shape", go_fields, "core/product.json")

        self.assertEqual(["name"], drift["required_with_omitempty"])

    @unittest.skipUnless(PLAN_BUDGET_SCHEMA_EXISTS, "sync_plans schema is not present")
    def test_the_real_false_positives_are_not_reported(self):
        with (
            mock.patch.object(sys, "argv", ["lint.py", "--strict", "--json"]),
            contextlib.redirect_stdout(io.StringIO()) as out,
        ):
            lint.main()
        payload = json.loads(out.getvalue())
        reported = {(g["type"], g["kind"], g["json"]) for g in payload["known_schema_gaps"]}

        for waived in (
            ("PlanBudget", "optional_bool_pointer", "reallocation_unlimited"),
            ("MediaBuyData", "required_with_omitempty", "revision"),
            ("ReportingReceipt", "required_with_omitempty", "verification_profile"),
            ("ReportingReceipt", "required_with_omitempty", "reporting_materialization_id"),
            ("GovernanceAccountInput", "required_with_omitempty", "governance_agents"),
        ):
            with self.subTest(waived=waived):
                self.assertNotIn(waived, reported)
        # A zero the schema accepts and a boolean with a default or no default
        # stay reported.
        self.assertIn(("MediaBuyData", "required_with_omitempty", "confirmed_at"), reported)
        self.assertIn(("AccountInput", "optional_bool_pointer", "sandbox"), reported)
        self.assertIn(("AccountReference", "optional_bool_pointer", "sandbox"), reported)
        self.assertIn(("Plan", "optional_bool_pointer", "human_review_required"), reported)
        self.assertIn(("DeliveryData", "missing_in_go", "status"), reported)




if __name__ == "__main__":
    unittest.main()
