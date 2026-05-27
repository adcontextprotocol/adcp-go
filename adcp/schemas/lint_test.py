import unittest

import lint


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
