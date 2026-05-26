import unittest
from collections import OrderedDict

import generate


class TestDeprecatedField(unittest.TestCase):
    """Tests that deprecated: true in a JSON Schema property produces a
    preceding // Deprecated: doc comment in the emitted Go struct field."""

    def _struct_lines(self, schema):
        output = generate.schema_to_struct("TestStruct", schema)
        return [l for l in output.splitlines() if l.startswith('\t')]

    def test_deprecated_field_emits_preceding_comment(self):
        schema = {
            "type": "object",
            "properties": {
                "status": {
                    "type": "string",
                    "deprecated": True,
                    "description": "Use new_status instead.",
                }
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 2,
            "Deprecated field should produce exactly 2 lines: comment + field")
        self.assertEqual(lines[0], "\t// Deprecated: Use new_status instead.")
        self.assertTrue(lines[1].startswith("\tStatus string"),
            f"Field line should start with field declaration, got: {lines[1]!r}")
        self.assertNotIn("//", lines[1],
            "Trailing inline comment suppressed for deprecated fields")

    def test_deprecated_field_without_description_uses_fallback(self):
        schema = {
            "type": "object",
            "properties": {
                "old_field": {"type": "string", "deprecated": True}
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(lines[0], "\t// Deprecated: No replacement specified.")

    def test_non_deprecated_field_unchanged(self):
        schema = {
            "type": "object",
            "properties": {
                "name": {"type": "string", "description": "The display name."}
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 1,
            "Non-deprecated field should produce exactly one line")
        self.assertIn("// The display name.", lines[0])

    def test_non_deprecated_field_no_description(self):
        schema = {
            "type": "object",
            "properties": {"count": {"type": "integer"}},
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 1)
        self.assertNotIn("//", lines[0])

    def test_mixed_struct_deprecated_and_normal(self):
        """Regression: other fields in the same struct are unaffected."""
        schema = {
            "type": "object",
            "required": ["id"],
            "properties": {
                "id": {"type": "string", "description": "Unique identifier."},
                "status": {
                    "type": "string",
                    "deprecated": True,
                    "description": "Use new_status instead.",
                },
                "new_status": {"type": "string", "description": "Current status."},
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 4)
        deprecated_comment_idx = next(
            (i for i, l in enumerate(lines) if "// Deprecated:" in l), None
        )
        self.assertIsNotNone(deprecated_comment_idx)
        self.assertIn("Status string", lines[deprecated_comment_idx + 1])

    def test_deprecated_field_double_prefix_stripped(self):
        """Schema author sets deprecated:true and prefixes description with
        'Deprecated: ' — generator must not emit '// Deprecated: Deprecated: …'."""
        schema = {
            "type": "object",
            "properties": {
                "axe_include_segment": {
                    "type": "string",
                    "deprecated": True,
                    "description": "Deprecated: Use TMP provider fields instead.",
                }
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(lines[0], "\t// Deprecated: Use TMP provider fields instead.")
        self.assertNotIn("Deprecated: Deprecated:", lines[0])

    def test_deprecated_case_insensitive_prefix_strip(self):
        schema = {
            "type": "object",
            "properties": {
                "old_field": {
                    "type": "string",
                    "deprecated": True,
                    "description": "deprecated: use new_field.",
                }
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(lines[0], "\t// Deprecated: use new_field.")


def scalar_or_array_schema(min_items=1, description="Test union"):
    return {
        "description": description,
        "oneOf": [
            {"type": "string"},
            {
                "type": "array",
                "items": {"type": "string"},
                "minItems": min_items,
            },
        ],
    }


class UnionHelperGenerationTest(unittest.TestCase):
    def setUp(self):
        self.original_union_schema_types = generate.UNION_SCHEMA_TYPES
        self.original_load_schema_spec = generate.load_schema_spec
        self.original_known_types = generate.KNOWN_TYPES
        generate._reset_will_generate_cache()

    def tearDown(self):
        generate.UNION_SCHEMA_TYPES = self.original_union_schema_types
        generate.load_schema_spec = self.original_load_schema_spec
        generate.KNOWN_TYPES = self.original_known_types
        generate._reset_will_generate_cache()

    def install_union_fixture(self, specs, schemas):
        generate.UNION_SCHEMA_TYPES = OrderedDict([("TestUnion", specs)])

        def load_schema_spec(spec):
            try:
                return schemas[spec]
            except KeyError as exc:
                raise KeyError(spec) from exc

        generate.load_schema_spec = load_schema_spec

    def test_supported_union_schemas_resolves_equivalent_schema_specs(self):
        self.install_union_fixture(
            ("request-a.json#/properties/filter", "request-b.json#/properties/filter"),
            {
                "request-a.json#/properties/filter": scalar_or_array_schema(
                    description="Filter by status. Defaults to active for request A."
                ),
                "request-b.json#/properties/filter": scalar_or_array_schema(
                    description="Filter by status."
                ),
            },
        )

        schemas = generate.supported_union_schemas()

        self.assertEqual(["TestUnion"], list(schemas))
        self.assertEqual("string", generate.scalar_union_go_type(schemas["TestUnion"]))
        self.assertEqual("Filter by status.", schemas["TestUnion"]["description"])

    def test_supported_union_schemas_rejects_broken_pointer(self):
        self.install_union_fixture(
            "request-a.json#/properties/missing",
            {},
        )

        with self.assertRaises(ValueError):
            generate.supported_union_schemas()

    def test_supported_union_schemas_rejects_non_equivalent_element_types(self):
        self.install_union_fixture(
            ("request-a.json#/properties/filter", "request-b.json#/properties/filter"),
            {
                "request-a.json#/properties/filter": scalar_or_array_schema(),
                "request-b.json#/properties/filter": {
                    "oneOf": [
                        {"type": "string"},
                        {"type": "array", "items": {"type": "integer"}, "minItems": 1},
                    ],
                },
            },
        )

        with self.assertRaises(ValueError):
            generate.supported_union_schemas()

    def test_supported_union_schemas_rejects_different_empty_array_constraints(self):
        self.install_union_fixture(
            ("request-a.json#/properties/filter", "request-b.json#/properties/filter"),
            {
                "request-a.json#/properties/filter": scalar_or_array_schema(min_items=1),
                "request-b.json#/properties/filter": scalar_or_array_schema(min_items=0),
            },
        )

        with self.assertRaises(ValueError):
            generate.supported_union_schemas()

    def test_coverage_mode_fails_when_configured_union_is_invalid(self):
        self.install_union_fixture(
            "request-a.json#/properties/missing",
            {},
        )

        with self.assertRaises(ValueError):
            generate.main(["--coverage-max-unreviewed-any", "999"])

    def test_schema_accepts_empty_array_is_defensive_for_non_dict_schema(self):
        self.assertTrue(generate.schema_accepts_empty_array(None))
        self.assertTrue(generate.schema_accepts_empty_array([]))


if __name__ == "__main__":
    unittest.main()
