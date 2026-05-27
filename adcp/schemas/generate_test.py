import shutil
import subprocess
import tempfile
import textwrap
import unittest
from collections import OrderedDict

import generate


def without_descriptions(value):
    if isinstance(value, dict):
        return {
            key: without_descriptions(item)
            for key, item in value.items()
            if key != "description"
        }
    if isinstance(value, list):
        return [without_descriptions(item) for item in value]
    return value


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


class EnumGenerationTest(unittest.TestCase):
    def setUp(self):
        self.original_inline_enum_types = generate.INLINE_ENUM_TYPES
        self.original_load_schema_spec = generate.load_schema_spec

    def tearDown(self):
        generate.INLINE_ENUM_TYPES = self.original_inline_enum_types
        generate.load_schema_spec = self.original_load_schema_spec

    def test_enum_helpers_are_opt_in_and_do_not_echo_rejected_value(self):
        src = generate.enum_to_type("TestStatus", "Test enum", ["active", "pending-start"])

        self.assertIn("type TestStatus = string", src)
        self.assertIn('TestStatusActive TestStatus = "active"', src)
        self.assertIn('TestStatusPendingStart TestStatus = "pending-start"', src)
        self.assertIn("KnownTestStatusValues returns the current schema-defined values", src)
        self.assertIn("func KnownTestStatusValues() []TestStatus", src)
        self.assertIn("return []TestStatus{TestStatusActive, TestStatusPendingStart}", src)
        self.assertIn("opt-in strict helper; JSON unmarshalling preserves unknown values", src)
        self.assertIn("func IsKnownTestStatus(v TestStatus) bool", src)
        self.assertIn("case TestStatusActive, TestStatusPendingStart:", src)
        self.assertIn("func ParseTestStatus(s string) (TestStatus, error)", src)
        self.assertIn('fmt.Errorf("unknown TestStatus value")', src)
        self.assertNotIn("%q", src)

    def test_enum_members_rejects_values_without_unique_go_constants(self):
        with self.assertRaises(ValueError):
            generate.enum_members("TestStatus", ["foo-bar", "foo_bar"])

        with self.assertRaises(ValueError):
            generate.enum_members("TestStatus", [""])

        with self.assertRaises(ValueError):
            generate.enum_members("TestStatus", ["active", 1])

    def test_generate_enums_includes_inline_optimization_metric_helpers(self):
        schema = generate.load_schema_spec(
            "core/optimization-goal.json#/oneOf/0/properties/metric"
        )
        src = generate.generate_enums()

        self.assertIn("type OptimizationMetric = string", src)
        self.assertIn("func KnownOptimizationMetricValues() []OptimizationMetric", src)
        self.assertIn("func IsKnownOptimizationMetric(v OptimizationMetric) bool", src)
        for value in schema["enum"]:
            const_name = "OptimizationMetric" + generate.pascal_case(value)
            self.assertIn(f'{const_name} OptimizationMetric = "{value}"', src)

    def test_generate_enums_rejects_duplicate_inline_enum_name(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "MediaBuyStatus",
                "core/optimization-goal.json#/oneOf/0/properties/metric",
            ),
        ])

        with self.assertRaisesRegex(ValueError, "duplicate enum type MediaBuyStatus"):
            generate.generate_enums()

    def test_generate_enums_requires_inline_enum_values(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([("TestInlineEnum", "test.json#/properties/value")])

        def load_schema_spec(spec):
            self.assertEqual("test.json#/properties/value", spec)
            return {"type": "string"}

        generate.load_schema_spec = load_schema_spec

        with self.assertRaisesRegex(ValueError, "no longer defines enum values"):
            generate.generate_enums()

    @unittest.skipUnless(shutil.which("go"), "go command not found")
    def test_enum_helpers_runtime(self):
        generated = generate.enum_to_type("TestStatus", "Test enum", ["active", "paused"])
        source = textwrap.dedent(f"""
            package enumtest

            import (
                "fmt"
                "testing"
            )

            {generated}

            func TestEnumHelpers(t *testing.T) {{
                values := KnownTestStatusValues()
                if len(values) != 2 || values[0] != TestStatusActive || values[1] != TestStatusPaused {{
                    t.Fatalf("known values = %#v", values)
                }}
                if !IsKnownTestStatus(TestStatusActive) {{
                    t.Fatal("active should be known")
                }}
                if IsKnownTestStatus(TestStatus("future_status")) {{
                    t.Fatal("future_status should not be known")
                }}
                parsed, err := ParseTestStatus("paused")
                if err != nil {{
                    t.Fatalf("parse known value: %v", err)
                }}
                if parsed != TestStatusPaused {{
                    t.Fatalf("parsed = %q", parsed)
                }}
                if _, err := ParseTestStatus("future_status"); err == nil {{
                    t.Fatal("parse unknown value succeeded")
                }} else if got := err.Error(); got != "unknown TestStatus value" {{
                    t.Fatalf("parse unknown error = %q", got)
                }}
            }}
        """)
        with tempfile.TemporaryDirectory() as tmp:
            with open(f"{tmp}/go.mod", "w") as f:
                f.write("module enumtest\n\ngo 1.22\n")
            with open(f"{tmp}/enum_test.go", "w") as f:
                f.write(source)
            subprocess.run(["go", "test", "."], cwd=tmp, check=True)


class OptimizationGoalSchemaTest(unittest.TestCase):
    def setUp(self):
        self.schema = generate.load_schema("core/optimization-goal.json")

    def test_cost_per_target_branches_stay_structurally_equivalent(self):
        metric_cost_per = generate.json_pointer_get(
            self.schema,
            "/oneOf/0/properties/target/oneOf/0",
        )
        event_cost_per = generate.json_pointer_get(
            self.schema,
            "/oneOf/1/properties/target/oneOf/0",
        )

        self.assertEqual(
            without_descriptions(metric_cost_per),
            without_descriptions(event_cost_per),
        )

    def test_target_branches_allow_additive_fields(self):
        metric_target = generate.json_pointer_get(
            self.schema,
            "/oneOf/0/properties/target",
        )
        event_target = generate.json_pointer_get(
            self.schema,
            "/oneOf/1/properties/target",
        )
        self.assertEqual(2, len(metric_target["oneOf"]))
        self.assertEqual(3, len(event_target["oneOf"]))

        target_branch_pointers = [
            "/oneOf/0/properties/target/oneOf/0",
            "/oneOf/0/properties/target/oneOf/1",
            "/oneOf/1/properties/target/oneOf/0",
            "/oneOf/1/properties/target/oneOf/1",
            "/oneOf/1/properties/target/oneOf/2",
        ]
        for pointer in target_branch_pointers:
            with self.subTest(pointer=pointer):
                target_branch = generate.json_pointer_get(self.schema, pointer)
                self.assertIs(
                    True,
                    target_branch.get("additionalProperties"),
                    f"{pointer} must allow additive fields while the Go target variant preserves Extra",
                )

    def test_optional_numeric_policy_for_optimization_goal_fields(self):
        event_source = generate.json_pointer_get(
            self.schema,
            "/oneOf/1/properties/event_sources/items",
        )
        value_factor_type, _ = generate.field_go_type_info(
            "OptimizationGoalEventSource",
            "value_factor",
            event_source["properties"]["value_factor"],
            set(event_source.get("required", [])),
        )
        self.assertEqual("*float64", value_factor_type)

        metric_goal = generate.json_pointer_get(self.schema, "/oneOf/0")
        view_duration_type, _ = generate.field_go_type_info(
            "OptimizationGoal",
            "view_duration_seconds",
            metric_goal["properties"]["view_duration_seconds"],
            set(metric_goal.get("required", [])),
        )
        self.assertEqual("float64", view_duration_type)


class OptionalNumericPolicyTest(unittest.TestCase):
    def assert_field_type(self, schema_path, type_name, json_name, want):
        schema = generate.load_schema(schema_path)
        prop = generate.schema_properties(schema)[json_name]
        required_set = generate.schema_required_names(schema)

        go_type, _ = generate.field_go_type_info(
            type_name,
            json_name,
            prop,
            required_set,
        )

        self.assertEqual(want, go_type)

    def test_zero_valid_optional_numeric_fields_use_pointers(self):
        self.assert_field_type(
            "core/creative-asset.json",
            "CreativeAsset",
            "weight",
            "*float64",
        )
        targeting = generate.load_schema("core/targeting.json")
        keyword_target = generate.json_pointer_get(
            targeting,
            "/properties/keyword_targets/items",
        )
        bid_price_type, _ = generate.field_go_type_info(
            "KeywordTarget",
            "bid_price",
            keyword_target["properties"]["bid_price"],
            set(keyword_target.get("required", [])),
        )
        self.assertEqual("*float64", bid_price_type)
        self.assert_field_type(
            "core/audience-selector.json",
            "AudienceSelector",
            "min_value",
            "*float64",
        )
        self.assert_field_type(
            "core/audience-selector.json",
            "AudienceSelector",
            "max_value",
            "*float64",
        )
        self.assert_field_type(
            "core/forecast-point.json",
            "ForecastPoint",
            "budget",
            "*float64",
        )


if __name__ == "__main__":
    unittest.main()
