import subprocess
import shutil
import tempfile
import textwrap
import unittest
from collections import OrderedDict

import generate


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

    def test_scalar_or_array_union_helper_includes_constructor(self):
        src = generate.scalar_or_array_union_to_type("TestUnion", scalar_or_array_schema())

        self.assertIn("func NewTestUnion(values ...string) *TestUnion", src)
        self.assertIn("It returns nil when called with no values", src)
        self.assertIn("triggering a MarshalJSON error on a schema-invalid empty array", src)
        self.assertIn("if len(values) == 0", src)
        self.assertIn("return nil", src)

    def test_scalar_or_array_union_helper_documents_empty_accepting_constructor(self):
        src = generate.scalar_or_array_union_to_type("TestUnion", scalar_or_array_schema(min_items=0))

        self.assertIn("func NewTestUnion(values ...string) *TestUnion", src)
        self.assertIn("Use nil instead of NewTestUnion() when an optional field should be omitted.", src)
        self.assertIn("NewTestUnion() returns a non-nil empty slice pointer that marshals as [].", src)
        self.assertIn("v := append(TestUnion{}, values...)", src)
        self.assertNotIn("if len(values) == 0", src)

    @unittest.skipUnless(shutil.which("go"), "go command not found")
    def test_empty_accepting_constructor_marshal_runtime(self):
        generated = generate.scalar_or_array_union_to_type("TestUnion", scalar_or_array_schema(min_items=0))
        source = textwrap.dedent(f"""
            package uniontest

            import (
                "encoding/json"
                "fmt"
                "testing"
            )

            {generated}

            func TestEmptyConstructorMarshalsArray(t *testing.T) {{
                got := NewTestUnion()
                if got == nil {{
                    t.Fatal("NewTestUnion() returned nil")
                }}
                raw, err := json.Marshal(got)
                if err != nil {{
                    t.Fatalf("marshal empty test union: %v", err)
                }}
                if string(raw) != "[]" {{
                    t.Fatalf("empty constructor marshaled %s, want []", raw)
                }}
            }}
        """)
        with tempfile.TemporaryDirectory() as tmp:
            with open(f"{tmp}/go.mod", "w") as f:
                f.write("module uniontest\n\ngo 1.22\n")
            with open(f"{tmp}/union_test.go", "w") as f:
                f.write(source)
            subprocess.run(["go", "test", "."], cwd=tmp, check=True)


if __name__ == "__main__":
    unittest.main()
