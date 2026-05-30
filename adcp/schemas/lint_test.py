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
            mock.patch.object(lint, "validate_union_schema_specs", return_value=[]),
            mock.patch.object(lint, "validate_custom_wire_fields", return_value=[]),
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
            "closed inline type schema is not additionalProperties:false",
            reports[0]["error"],
        )
        self.assertIn("OPEN_INLINE_SCHEMA_TYPES", reports[0]["remediation"])

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
            "open inline type schema is additionalProperties:false",
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


if __name__ == "__main__":
    unittest.main()
