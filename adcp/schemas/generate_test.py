import contextlib
import io
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from collections import OrderedDict
from unittest import mock

import generate


def emit_unformatted_go():
    print('package adcp')
    print('type Test struct {')
    print('Name string `json:"name"`')
    print('}')


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


class GenerateMainTest(unittest.TestCase):
    def test_main_formats_generated_go(self):
        completed = subprocess.CompletedProcess(
            ["gofmt"],
            0,
            stdout='package adcp\n\ntype Test struct {\n\tName string `json:"name"`\n}\n',
            stderr="",
        )

        with mock.patch.object(generate, "generate", emit_unformatted_go), \
                mock.patch.object(generate.subprocess, "run", return_value=completed):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = generate.main([])

        self.assertEqual(0, rc)
        self.assertIn("\tName string", out.getvalue())

    def test_main_falls_back_when_gofmt_is_missing(self):
        with mock.patch.object(generate, "generate", emit_unformatted_go), \
                mock.patch.object(generate.subprocess, "run", side_effect=OSError("missing")):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = generate.main([])

        self.assertEqual(0, rc)
        self.assertIn("type Test struct {", out.getvalue())

    def test_main_fails_when_gofmt_fails(self):
        err = subprocess.CalledProcessError(1, ["gofmt"], stderr="bad go\n")
        with mock.patch.object(generate, "generate", emit_unformatted_go), \
                mock.patch.object(generate.subprocess, "run", side_effect=err):
            out = io.StringIO()
            err_out = io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err_out):
                rc = generate.main([])

        self.assertEqual(1, rc)
        self.assertEqual("", out.getvalue())
        self.assertIn("bad go", err_out.getvalue())
        self.assertIn("gofmt failed", err_out.getvalue())


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
        src = generate.generate_enums()

        self.assertIn("type OptimizationMetric = string", src)
        self.assertIn('OptimizationMetricClicks OptimizationMetric = "clicks"', src)
        self.assertIn("func KnownOptimizationMetricValues() []OptimizationMetric", src)
        self.assertIn("func IsKnownOptimizationMetric(v OptimizationMetric) bool", src)
        schema = generate.load_inline_enum_schema(generate.INLINE_ENUM_TYPES["OptimizationMetric"])
        for value in schema["enum"]:
            const_name = "OptimizationMetric" + generate.pascal_case(value)
            self.assertIn(f'{const_name} OptimizationMetric = "{value}"', src)

    def test_generate_enums_resolves_inline_enum_by_kind(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "TestInlineEnum",
                {
                    "schema": "test.json",
                    "one_of_kind": "metric",
                    "property": "value",
                },
            ),
        ])

        def load_schema_spec(spec):
            self.assertEqual("test.json", spec)
            return {
                "oneOf": [
                    {
                        "properties": {
                            "kind": {"const": "event"},
                            "value": {"enum": ["wrong"]},
                        },
                    },
                    {
                        "properties": {
                            "kind": {"const": "metric"},
                            "value": {"enum": ["right"]},
                        },
                    },
                ],
            }

        generate.load_schema_spec = load_schema_spec

        src = generate.generate_enums()
        self.assertIn('TestInlineEnumRight TestInlineEnum = "right"', src)
        self.assertNotIn("TestInlineEnumWrong", src)

    def test_generate_enums_rejects_duplicate_inline_enum_name(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "MediaBuyStatus",
                {
                    "schema": "test.json",
                    "one_of_kind": "metric",
                    "property": "value",
                },
            ),
        ])

        def load_schema_spec(spec):
            self.assertEqual("test.json", spec)
            return {
                "oneOf": [
                    {
                        "properties": {
                            "kind": {"const": "metric"},
                            "value": {"enum": ["x"]},
                        },
                    },
                ],
            }

        generate.load_schema_spec = load_schema_spec

        with self.assertRaisesRegex(ValueError, "duplicate enum type MediaBuyStatus"):
            generate.generate_enums(_seen={"MediaBuyStatus": "enums/media-buy-status.json"})

    def test_generate_enums_requires_inline_enum_values(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "TestInlineEnum",
                {
                    "schema": "test.json",
                    "one_of_kind": "metric",
                    "property": "value",
                },
            ),
        ])

        def load_schema_spec(spec):
            self.assertEqual("test.json", spec)
            return {
                "oneOf": [
                    {
                        "properties": {
                            "kind": {"const": "metric"},
                            "value": {"type": "string"},
                        },
                    },
                ],
            }

        generate.load_schema_spec = load_schema_spec

        with self.assertRaisesRegex(ValueError, "no longer defines enum values"):
            generate.generate_enums()

    def test_generate_enums_rejects_duplicate_inline_kind_matches(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "TestInlineEnum",
                {
                    "schema": "test.json",
                    "one_of_kind": "metric",
                    "property": "value",
                },
            ),
        ])

        def load_schema_spec(spec):
            self.assertEqual("test.json", spec)
            return {
                "oneOf": [
                    {
                        "properties": {
                            "kind": {"const": "metric"},
                            "value": {"enum": ["first"]},
                        },
                    },
                    {
                        "properties": {
                            "kind": {"const": "metric"},
                            "value": {"enum": ["second"]},
                        },
                    },
                ],
            }

        generate.load_schema_spec = load_schema_spec

        with self.assertRaisesRegex(ValueError, "matched multiple oneOf branches"):
            generate.generate_enums()

    def test_generate_enums_rejects_unmatched_inline_kind(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "TestInlineEnum",
                {
                    "schema": "test.json",
                    "one_of_kind": "metric",
                    "property": "value",
                },
            ),
        ])

        def load_schema_spec(spec):
            self.assertEqual("test.json", spec)
            return {
                "oneOf": [
                    {
                        "properties": {
                            "kind": {"const": "event"},
                            "value": {"enum": ["wrong"]},
                        },
                    },
                ],
            }

        generate.load_schema_spec = load_schema_spec

        with self.assertRaisesRegex(
            ValueError,
            r"INLINE_ENUM_TYPES\['TestInlineEnum'\].*did not match a oneOf branch",
        ):
            generate.generate_enums()

    def test_generate_enums_does_not_match_single_value_enum_discriminator(self):
        generate.INLINE_ENUM_TYPES = OrderedDict([
            (
                "TestInlineEnum",
                {
                    "schema": "test.json",
                    "one_of_kind": "metric",
                    "property": "value",
                },
            ),
        ])

        def load_schema_spec(spec):
            self.assertEqual("test.json", spec)
            return {
                "oneOf": [
                    {
                        "properties": {
                            "kind": {"enum": ["metric"]},
                            "value": {"enum": ["x"]},
                        },
                    },
                ],
            }

        generate.load_schema_spec = load_schema_spec

        with self.assertRaisesRegex(
            ValueError,
            r"INLINE_ENUM_TYPES\['TestInlineEnum'\].*did not match a oneOf branch",
        ):
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


class StructGenerationTest(unittest.TestCase):
    def test_deprecated_property_emits_go_deprecation_comment(self):
        schema = {
            "type": "object",
            "properties": OrderedDict(
                [
                    (
                        "status",
                        {
                            "type": "string",
                            "description": "Deprecated: Use media_buy_status instead.",
                            "deprecated": True,
                        },
                    )
                ]
            ),
        }

        got = generate.schema_to_struct("CreateMediaBuySuccess", schema)

        self.assertIn(
            "\t// Deprecated: Use media_buy_status instead.\n"
            "\tStatus string `json:\"status,omitempty\"` // Deprecated: Use media_buy_status instead.",
            got,
        )

    def test_deprecated_property_without_description_emits_fallback_comment(self):
        schema = {
            "type": "object",
            "properties": OrderedDict(
                [
                    (
                        "legacy_id",
                        {
                            "type": "string",
                            "deprecated": True,
                        },
                    )
                ]
            ),
        }

        got = generate.schema_to_struct("DeprecatedFallback", schema)

        self.assertIn(
            "\t// Deprecated: This field is deprecated.\n"
            "\tLegacyID string `json:\"legacy_id,omitempty\"`",
            got,
        )


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
        self.assert_field_type(
            "media-buy/package-request.json",
            "PackageInput",
            "bid_price",
            "*float64",
        )
        self.assert_field_type(
            "media-buy/package-request.json",
            "PackageInput",
            "impressions",
            "*float64",
        )
        self.assert_field_type(
            "media-buy/package-update.json",
            "PackageUpdate",
            "budget",
            "*float64",
        )
        self.assert_field_type(
            "media-buy/package-update.json",
            "PackageUpdate",
            "bid_price",
            "*float64",
        )
        self.assert_field_type(
            "media-buy/package-update.json",
            "PackageUpdate",
            "impressions",
            "*float64",
        )
        package_update = generate.load_schema("media-buy/package-update.json")
        keyword_add = generate.json_pointer_get(
            package_update,
            "/properties/keyword_targets_add/items",
        )
        bid_price_type, _ = generate.field_go_type_info(
            "KeywordTargetUpdate",
            "bid_price",
            keyword_add["properties"]["bid_price"],
            set(keyword_add.get("required", [])),
        )
        self.assertEqual("*float64", bid_price_type)


class InlineObjectGenerationTest(unittest.TestCase):
    def assert_field_type(self, schema_path, type_name, json_name, want):
        schema = generate.load_schema(schema_path)
        prop = generate.schema_properties(schema)[json_name]
        required_set = generate.schema_required_names(schema)

        go_type, reason = generate.field_go_type_info(
            type_name,
            json_name,
            prop,
            required_set,
        )

        self.assertEqual(want, go_type)
        self.assertIsNone(reason)

    def test_low_risk_inline_objects_are_typed(self):
        self.assert_field_type(
            "creative/list-creatives-request.json",
            "ListCreativesRequest",
            "sort",
            "*ListCreativesSort",
        )
        self.assert_field_type(
            "content-standards/artifact-webhook-payload.json",
            "ArtifactWebhookPayload",
            "pagination",
            "*ArtifactWebhookPagination",
        )
        self.assert_field_type(
            "core/performance-feedback.json",
            "PerformanceFeedback",
            "measurement_period",
            "DatetimeRange",
        )
        self.assert_field_type(
            "core/planned-delivery.json",
            "PlannedDelivery",
            "geo",
            "*PlannedDeliveryGeo",
        )
        self.assert_field_type(
            "core/account.json",
            "Account",
            "setup",
            "*AccountSetup",
        )
        self.assert_field_type(
            "core/creative-brief.json",
            "CreativeBrief",
            "messaging",
            "*CreativeBriefMessaging",
        )
        self.assert_field_type(
            "core/creative-brief.json",
            "CreativeBrief",
            "compliance",
            "*CreativeBriefCompliance",
        )
        self.assert_field_type(
            "core/event-custom-data.json",
            "EventCustomData",
            "contents",
            "[]EventContentItem",
        )
        self.assert_field_type(
            "governance/policy-category-definition.json",
            "PolicyCategoryDefinition",
            "regulatory_frameworks",
            "[]PolicyRegulatoryFramework",
        )
        self.assert_field_type(
            "core/format.json",
            "CreativeFormat",
            "supported_macros",
            "[]string",
        )
        self.assert_field_type(
            "core/user-match.json",
            "UserMatch",
            "uids",
            "[]UserMatchUID",
        )
        self.assert_field_type(
            "bundled/media-buy/get-products-request.json",
            "GetProductsRequest",
            "filters",
            "*ProductFilters",
        )
        self.assert_field_type(
            "core/product-filters.json",
            "ProductFilters",
            "budget_range",
            "*ProductFilterBudgetRange",
        )
        self.assert_field_type(
            "core/product-filters.json",
            "ProductFilters",
            "required_features",
            "map[string]bool",
        )
        self.assert_field_type(
            "core/product-filters.json",
            "ProductFilters",
            "signal_targeting",
            "[]SignalTargeting",
        )
        self.assert_field_type(
            "core/product-filters.json",
            "ProductFilters",
            "geo_proximity",
            "[]ProductFilterGeoProximity",
        )
        self.assert_field_type(
            "core/targeting.json",
            "Targeting",
            "geo_proximity",
            "[]GeoProximityTarget",
        )
        self.assert_field_type(
            "core/signal-targeting.json",
            "SignalTargeting",
            "min_value",
            "*float64",
        )
        self.assert_field_type(
            "core/signal-targeting.json",
            "SignalTargeting",
            "max_value",
            "*float64",
        )
        self.assert_field_type(
            "governance/check-governance-request.json",
            "CheckGovernanceRequest",
            "delivery_metrics",
            "*GovernanceDeliveryMetrics",
        )
        self.assert_field_type(
            "governance/report-plan-outcome-request.json",
            "ReportPlanOutcomeRequest",
            "delivery",
            "*ReportPlanOutcomeDelivery",
        )
        self.assert_field_type(
            "governance/report-plan-outcome-request.json",
            "ReportPlanOutcomeRequest",
            "error",
            "*ReportPlanOutcomeError",
        )
        self.assert_field_type(
            "governance/report-plan-outcome-response.json",
            "ReportPlanOutcomeResponse",
            "plan_summary",
            "*ReportPlanOutcomePlanSummary",
        )
        self.assert_field_type(
            "governance/policy-entry.json",
            "PolicyEntry",
            "exemplars",
            "*PolicyExemplars",
        )
        self.assert_field_type(
            "media-buy/get-products-response.json",
            "GetProductsResponse",
            "incomplete",
            "[]GetProductsIncompleteItem",
        )
        self.assert_field_type(
            "governance/sync-plans-response.json",
            "SyncPlansResponse",
            "plans",
            "[]SyncPlansPlan",
        )
        self.assert_field_type(
            "governance/check-governance-response.json",
            "CheckGovernanceResponse",
            "findings",
            "[]CheckGovernanceFinding",
        )
        self.assert_field_type(
            "governance/report-plan-outcome-response.json",
            "ReportPlanOutcomeResponse",
            "findings",
            "[]ReportPlanOutcomeFinding",
        )
        self.assert_field_type(
            "governance/check-governance-response.json",
            "CheckGovernanceResponse",
            "conditions",
            "[]CheckGovernanceCondition",
        )
        self.assert_field_type(
            "governance/get-plan-audit-logs-response.json",
            "GetPlanAuditLogsResponse",
            "plans",
            "[]PlanAuditLog",
        )

    def test_inline_object_structs_are_generated_from_schema_pointers(self):
        sort_schema = generate.load_schema_spec(
            "creative/list-creatives-request.json#/properties/sort",
        )
        sort_generated = generate.schema_to_struct("ListCreativesSort", sort_schema)
        self.assertIn("Field string `json:\"field,omitempty\"`", sort_generated)
        self.assertIn("Direction string `json:\"direction,omitempty\"`", sort_generated)

        pagination_schema = generate.load_schema_spec(
            "content-standards/artifact-webhook-payload.json#/properties/pagination",
        )
        pagination_generated = generate.schema_to_struct(
            "ArtifactWebhookPagination",
            pagination_schema,
        )
        self.assertIn("TotalArtifacts int `json:\"total_artifacts,omitempty\"`", pagination_generated)
        self.assertIn("BatchNumber int `json:\"batch_number,omitempty\"`", pagination_generated)
        self.assertIn("TotalBatches int `json:\"total_batches,omitempty\"`", pagination_generated)

        geo_schema = generate.load_schema_spec(
            "core/planned-delivery.json#/properties/geo",
        )
        geo_generated = generate.schema_to_struct("PlannedDeliveryGeo", geo_schema)
        self.assertIn("Countries []string `json:\"countries,omitempty\"`", geo_generated)
        self.assertIn("Regions []string `json:\"regions,omitempty\"`", geo_generated)

        messaging_schema = generate.load_schema_spec(
            "core/creative-brief.json#/properties/messaging",
        )
        messaging_generated = generate.schema_to_struct(
            "CreativeBriefMessaging",
            messaging_schema,
        )
        self.assertIn("Headline string `json:\"headline,omitempty\"`", messaging_generated)
        self.assertIn("Tagline string `json:\"tagline,omitempty\"`", messaging_generated)
        self.assertIn("CTA string `json:\"cta,omitempty\"`", messaging_generated)
        self.assertIn("KeyMessages []string `json:\"key_messages,omitempty\"`", messaging_generated)

        compliance_schema = generate.load_schema_spec(
            "core/creative-brief.json#/properties/compliance",
        )
        compliance_generated = generate.schema_to_struct(
            "CreativeBriefCompliance",
            compliance_schema,
        )
        self.assertIn(
            "RequiredDisclosures []CreativeBriefDisclosure `json:\"required_disclosures,omitempty\"`",
            compliance_generated,
        )
        self.assertIn(
            "ProhibitedClaims []string `json:\"prohibited_claims,omitempty\"`",
            compliance_generated,
        )

        disclosure_schema = generate.load_schema_spec(
            "core/creative-brief.json#/properties/compliance"
            "/properties/required_disclosures/items",
        )
        disclosure_generated = generate.schema_to_struct(
            "CreativeBriefDisclosure",
            disclosure_schema,
        )
        self.assertIn("Text string `json:\"text\"`", disclosure_generated)
        self.assertIn(
            "Jurisdictions []string `json:\"jurisdictions,omitempty\"`",
            disclosure_generated,
        )
        self.assertIn("Persistence string `json:\"persistence,omitempty\"`", disclosure_generated)

        event_item_schema = generate.load_schema_spec(
            "core/event-custom-data.json#/properties/contents/items",
        )
        event_item_generated = generate.schema_to_struct(
            "EventContentItem",
            event_item_schema,
        )
        self.assertIn("ID string `json:\"id\"`", event_item_generated)
        self.assertIn("Quantity int `json:\"quantity,omitempty\"`", event_item_generated)
        self.assertIn("Price float64 `json:\"price,omitempty\"`", event_item_generated)

        framework_schema = generate.load_schema_spec(
            "governance/policy-category-definition.json"
            "#/properties/regulatory_frameworks/items",
        )
        framework_generated = generate.schema_to_struct(
            "PolicyRegulatoryFramework",
            framework_schema,
        )
        self.assertIn("Name string `json:\"name\"`", framework_generated)
        self.assertIn("Summary string `json:\"summary\"`", framework_generated)
        self.assertIn("PolicyIDs []string `json:\"policy_ids,omitempty\"`", framework_generated)

        uid_schema = generate.load_schema_spec(
            "core/user-match.json#/properties/uids/items",
        )
        uid_generated = generate.schema_to_struct("UserMatchUID", uid_schema)
        self.assertIn("Type string `json:\"type\"`", uid_generated)
        self.assertIn("Value string `json:\"value\"`", uid_generated)

        filters_schema = generate.load_schema("core/product-filters.json")
        filters_generated = generate.schema_to_struct("ProductFilters", filters_schema)
        self.assertIn(
            "BudgetRange *ProductFilterBudgetRange `json:\"budget_range,omitempty\"`",
            filters_generated,
        )
        self.assertIn(
            "RequiredFeatures map[string]bool `json:\"required_features,omitempty\"`",
            filters_generated,
        )
        self.assertIn(
            "SignalTargeting []SignalTargeting `json:\"signal_targeting,omitempty\"`",
            filters_generated,
        )
        self.assertIn(
            "GeoProximity []ProductFilterGeoProximity `json:\"geo_proximity,omitempty\"`",
            filters_generated,
        )

        budget_schema = generate.load_schema_spec(
            "core/product-filters.json#/properties/budget_range",
        )
        budget_generated = generate.schema_to_struct(
            "ProductFilterBudgetRange",
            budget_schema,
        )
        self.assertIn("Min *float64 `json:\"min,omitempty\"`", budget_generated)
        self.assertIn("Max *float64 `json:\"max,omitempty\"`", budget_generated)
        self.assertIn("Currency string `json:\"currency\"`", budget_generated)

        proximity_schema = generate.load_schema_spec(
            "core/product-filters.json#/properties/geo_proximity/items",
        )
        proximity_generated = generate.schema_to_struct(
            "ProductFilterGeoProximity",
            proximity_schema,
        )
        self.assertIn("Lat *float64 `json:\"lat,omitempty\"`", proximity_generated)
        self.assertIn("Lng *float64 `json:\"lng,omitempty\"`", proximity_generated)
        self.assertIn(
            "TravelTime *ProductFilterTravelTime `json:\"travel_time,omitempty\"`",
            proximity_generated,
        )
        self.assertIn(
            "Geometry *ProductFilterGeometry `json:\"geometry,omitempty\"`",
            proximity_generated,
        )

        geometry_schema = generate.load_schema_spec(
            "core/product-filters.json#/properties/geo_proximity/items/properties/geometry",
        )
        geometry_generated = generate.schema_to_struct(
            "ProductFilterGeometry",
            geometry_schema,
        )
        self.assertIn("Type string `json:\"type\"`", geometry_generated)
        self.assertIn("Coordinates []any `json:\"coordinates\"`", geometry_generated)

        targeting_proximity_schema = generate.load_schema_spec(
            "core/targeting.json#/properties/geo_proximity/items",
        )
        targeting_proximity_generated = generate.schema_to_struct(
            "GeoProximityTarget",
            targeting_proximity_schema,
        )
        self.assertIn("Lat *float64 `json:\"lat,omitempty\"`", targeting_proximity_generated)
        self.assertIn("Lng *float64 `json:\"lng,omitempty\"`", targeting_proximity_generated)
        self.assertIn(
            "TravelTime *GeoProximityTravelTime `json:\"travel_time,omitempty\"`",
            targeting_proximity_generated,
        )
        self.assertIn(
            "Geometry *GeoProximityGeometry `json:\"geometry,omitempty\"`",
            targeting_proximity_generated,
        )
        self.assertIn("Ext any `json:\"ext,omitempty\"`", targeting_proximity_generated)

        targeting_geometry_schema = generate.load_schema_spec(
            "core/targeting.json#/properties/geo_proximity/items/properties/geometry",
        )
        targeting_geometry_generated = generate.schema_to_struct(
            "GeoProximityGeometry",
            targeting_geometry_schema,
        )
        self.assertIn("Type string `json:\"type\"`", targeting_geometry_generated)
        self.assertIn(
            "Coordinates []any `json:\"coordinates\"`",
            targeting_geometry_generated,
        )

        delivery_schema = generate.load_schema_spec(
            "governance/check-governance-request.json#/properties/delivery_metrics",
        )
        delivery_generated = generate.schema_to_struct(
            "GovernanceDeliveryMetrics",
            delivery_schema,
        )
        self.assertIn(
            "ReportingPeriod GovernanceDeliveryReportingPeriod `json:\"reporting_period\"`",
            delivery_generated,
        )
        self.assertIn("Spend *float64 `json:\"spend,omitempty\"`", delivery_generated)
        self.assertIn(
            "CumulativeSpend *float64 `json:\"cumulative_spend,omitempty\"`",
            delivery_generated,
        )
        self.assertIn(
            "Impressions *int `json:\"impressions,omitempty\"`",
            delivery_generated,
        )
        self.assertIn(
            "CumulativeImpressions *int `json:\"cumulative_impressions,omitempty\"`",
            delivery_generated,
        )
        self.assertIn(
            "GeoDistribution map[string]float64 `json:\"geo_distribution,omitempty\"`",
            delivery_generated,
        )
        self.assertIn(
            "ChannelDistribution map[string]float64 `json:\"channel_distribution,omitempty\"`",
            delivery_generated,
        )
        self.assertIn("Pacing string `json:\"pacing,omitempty\"`", delivery_generated)
        self.assertIn(
            "AudienceDistribution *GovernanceAudienceDistribution `json:\"audience_distribution,omitempty\"`",
            delivery_generated,
        )

        reporting_period_schema = generate.load_schema_spec(
            "governance/check-governance-request.json#/properties/delivery_metrics"
            "/properties/reporting_period",
        )
        reporting_period_generated = generate.schema_to_struct(
            "GovernanceDeliveryReportingPeriod",
            reporting_period_schema,
        )
        self.assertIn("Start string `json:\"start\"`", reporting_period_generated)
        self.assertIn("End string `json:\"end\"`", reporting_period_generated)

        audience_schema = generate.load_schema_spec(
            "governance/check-governance-request.json#/properties/delivery_metrics"
            "/properties/audience_distribution",
        )
        audience_generated = generate.schema_to_struct(
            "GovernanceAudienceDistribution",
            audience_schema,
        )
        self.assertIn("Baseline string `json:\"baseline\"`", audience_generated)
        self.assertIn(
            "BaselineDescription string `json:\"baseline_description,omitempty\"`",
            audience_generated,
        )
        self.assertIn("Indices map[string]float64 `json:\"indices\"`", audience_generated)
        self.assertIn(
            "CumulativeIndices map[string]float64 `json:\"cumulative_indices,omitempty\"`",
            audience_generated,
        )

        outcome_delivery_schema = generate.load_schema_spec(
            "governance/report-plan-outcome-request.json#/properties/delivery",
        )
        outcome_delivery_generated = generate.schema_to_struct(
            "ReportPlanOutcomeDelivery",
            outcome_delivery_schema,
        )
        self.assertIn(
            "ReportingPeriod *ReportPlanOutcomeDeliveryReportingPeriod `json:\"reporting_period,omitempty\"`",
            outcome_delivery_generated,
        )
        self.assertIn(
            "Impressions *int `json:\"impressions,omitempty\"`",
            outcome_delivery_generated,
        )
        self.assertIn("Spend *float64 `json:\"spend,omitempty\"`", outcome_delivery_generated)
        self.assertIn("CPM *float64 `json:\"cpm,omitempty\"`", outcome_delivery_generated)
        self.assertIn(
            "ViewabilityRate *float64 `json:\"viewability_rate,omitempty\"`",
            outcome_delivery_generated,
        )
        self.assertIn(
            "CompletionRate *float64 `json:\"completion_rate,omitempty\"`",
            outcome_delivery_generated,
        )

        outcome_reporting_period_schema = generate.load_schema_spec(
            "governance/report-plan-outcome-request.json#/properties/delivery"
            "/properties/reporting_period",
        )
        outcome_reporting_period_generated = generate.schema_to_struct(
            "ReportPlanOutcomeDeliveryReportingPeriod",
            outcome_reporting_period_schema,
        )
        self.assertIn("Start string `json:\"start\"`", outcome_reporting_period_generated)
        self.assertIn("End string `json:\"end\"`", outcome_reporting_period_generated)

        outcome_error_schema = generate.load_schema_spec(
            "governance/report-plan-outcome-request.json#/properties/error",
        )
        outcome_error_generated = generate.schema_to_struct(
            "ReportPlanOutcomeError",
            outcome_error_schema,
        )
        self.assertIn("Code string `json:\"code,omitempty\"`", outcome_error_generated)
        self.assertIn("Message string `json:\"message,omitempty\"`", outcome_error_generated)

        outcome_plan_summary_schema = generate.load_schema_spec(
            "governance/report-plan-outcome-response.json#/properties/plan_summary",
        )
        outcome_plan_summary_generated = generate.schema_to_struct(
            "ReportPlanOutcomePlanSummary",
            outcome_plan_summary_schema,
        )
        self.assertIn(
            "TotalCommitted *float64 `json:\"total_committed,omitempty\"`",
            outcome_plan_summary_generated,
        )
        self.assertIn(
            "BudgetRemaining *float64 `json:\"budget_remaining,omitempty\"`",
            outcome_plan_summary_generated,
        )

        policy_exemplars_schema = generate.load_schema_spec(
            "governance/policy-entry.json#/properties/exemplars",
        )
        policy_exemplars_generated = generate.schema_to_struct(
            "PolicyExemplars",
            policy_exemplars_schema,
        )
        self.assertIn(
            "Pass []PolicyExemplar `json:\"pass,omitempty\"`",
            policy_exemplars_generated,
        )
        self.assertIn(
            "Fail []PolicyExemplar `json:\"fail,omitempty\"`",
            policy_exemplars_generated,
        )

        policy_exemplar_schema = generate.load_schema_spec(
            "governance/policy-entry.json#/$defs/exemplar",
        )
        policy_exemplar_generated = generate.schema_to_struct(
            "PolicyExemplar",
            policy_exemplar_schema,
        )
        self.assertIn("Scenario string `json:\"scenario\"`", policy_exemplar_generated)
        self.assertIn("Explanation string `json:\"explanation\"`", policy_exemplar_generated)

        incomplete_schema = generate.load_schema_spec(
            "media-buy/get-products-response.json#/properties/incomplete/items",
        )
        incomplete_generated = generate.schema_to_struct(
            "GetProductsIncompleteItem",
            incomplete_schema,
        )
        self.assertIn("Scope string `json:\"scope\"`", incomplete_generated)
        self.assertIn("Description string `json:\"description\"`", incomplete_generated)
        self.assertIn(
            "EstimatedWait *Duration `json:\"estimated_wait,omitempty\"`",
            incomplete_generated,
        )

        sync_plan_schema = generate.load_schema_spec(
            "governance/sync-plans-response.json#/properties/plans/items",
        )
        sync_plan_generated = generate.schema_to_struct(
            "SyncPlansPlan",
            sync_plan_schema,
        )
        self.assertIn("PlanID string `json:\"plan_id\"`", sync_plan_generated)
        self.assertIn("Status string `json:\"status\"`", sync_plan_generated)
        self.assertIn("Version int `json:\"version\"`", sync_plan_generated)
        self.assertIn(
            "Categories []SyncPlansPlanCategory `json:\"categories,omitempty\"`",
            sync_plan_generated,
        )
        self.assertIn(
            "ResolvedPolicies []SyncPlansResolvedPolicy `json:\"resolved_policies,omitempty\"`",
            sync_plan_generated,
        )

        sync_plan_category_schema = generate.load_schema_spec(
            "governance/sync-plans-response.json#/properties/plans/items"
            "/properties/categories/items",
        )
        sync_plan_category_generated = generate.schema_to_struct(
            "SyncPlansPlanCategory",
            sync_plan_category_schema,
        )
        self.assertIn(
            "CategoryID string `json:\"category_id\"`",
            sync_plan_category_generated,
        )
        self.assertIn("Status string `json:\"status\"`", sync_plan_category_generated)

        sync_plan_policy_schema = generate.load_schema_spec(
            "governance/sync-plans-response.json#/properties/plans/items"
            "/properties/resolved_policies/items",
        )
        sync_plan_policy_generated = generate.schema_to_struct(
            "SyncPlansResolvedPolicy",
            sync_plan_policy_schema,
        )
        self.assertIn("PolicyID string `json:\"policy_id\"`", sync_plan_policy_generated)
        self.assertIn("Source string `json:\"source\"`", sync_plan_policy_generated)
        self.assertIn(
            "Enforcement PolicyEnforcement `json:\"enforcement\"`",
            sync_plan_policy_generated,
        )
        self.assertIn("Reason string `json:\"reason,omitempty\"`", sync_plan_policy_generated)

        check_finding_schema = generate.load_schema_spec(
            "governance/check-governance-response.json#/properties/findings/items",
        )
        check_finding_generated = generate.schema_to_struct(
            "CheckGovernanceFinding",
            check_finding_schema,
        )
        self.assertIn("CategoryID string `json:\"category_id\"`", check_finding_generated)
        self.assertIn("PolicyID string `json:\"policy_id,omitempty\"`", check_finding_generated)
        self.assertIn("SourcePlanID string `json:\"source_plan_id,omitempty\"`", check_finding_generated)
        self.assertIn(
            "Severity EscalationSeverity `json:\"severity\"`",
            check_finding_generated,
        )
        self.assertIn("Explanation string `json:\"explanation\"`", check_finding_generated)
        self.assertIn("Details map[string]any `json:\"details,omitempty\"`", check_finding_generated)
        self.assertIn("Confidence *float64 `json:\"confidence,omitempty\"`", check_finding_generated)
        self.assertIn(
            "UncertaintyReason string `json:\"uncertainty_reason,omitempty\"`",
            check_finding_generated,
        )

        outcome_finding_schema = generate.load_schema_spec(
            "governance/report-plan-outcome-response.json#/properties/findings/items",
        )
        outcome_finding_generated = generate.schema_to_struct(
            "ReportPlanOutcomeFinding",
            outcome_finding_schema,
        )
        self.assertIn("CategoryID string `json:\"category_id\"`", outcome_finding_generated)
        self.assertIn(
            "Severity EscalationSeverity `json:\"severity\"`",
            outcome_finding_generated,
        )
        self.assertIn("Explanation string `json:\"explanation\"`", outcome_finding_generated)
        self.assertIn("Details map[string]any `json:\"details,omitempty\"`", outcome_finding_generated)

        plan_audit_schema = generate.load_schema_spec(
            "governance/get-plan-audit-logs-response.json#/properties/plans/items",
        )
        plan_audit_generated = generate.schema_to_struct(
            "PlanAuditLog",
            plan_audit_schema,
        )
        self.assertIn("PlanID string `json:\"plan_id\"`", plan_audit_generated)
        self.assertIn("PlanVersion int `json:\"plan_version\"`", plan_audit_generated)
        self.assertIn("Budget PlanAuditBudget `json:\"budget\"`", plan_audit_generated)
        self.assertIn(
            "ChannelAllocation map[string]PlanAuditChannelAllocation `json:\"channel_allocation,omitempty\"`",
            plan_audit_generated,
        )
        self.assertIn("Summary PlanAuditSummary `json:\"summary\"`", plan_audit_generated)
        self.assertIn(
            "Entries []PlanAuditEntry `json:\"entries,omitempty\"`",
            plan_audit_generated,
        )
        self.assertIn(
            "GovernedActions []PlanAuditGovernedAction `json:\"governed_actions\"`",
            plan_audit_generated,
        )

        plan_audit_budget_schema = generate.load_schema_spec(
            "governance/get-plan-audit-logs-response.json#/properties/plans/items"
            "/properties/budget",
        )
        plan_audit_budget_generated = generate.schema_to_struct(
            "PlanAuditBudget",
            plan_audit_budget_schema,
        )
        self.assertIn("Authorized *float64 `json:\"authorized,omitempty\"`", plan_audit_budget_generated)
        self.assertIn("Committed *float64 `json:\"committed,omitempty\"`", plan_audit_budget_generated)

        plan_audit_summary_schema = generate.load_schema_spec(
            "governance/get-plan-audit-logs-response.json#/properties/plans/items"
            "/properties/summary",
        )
        plan_audit_summary_generated = generate.schema_to_struct(
            "PlanAuditSummary",
            plan_audit_summary_schema,
        )
        self.assertIn("ChecksPerformed *int `json:\"checks_performed,omitempty\"`", plan_audit_summary_generated)
        self.assertIn("Statuses *PlanAuditStatusCounts `json:\"statuses,omitempty\"`", plan_audit_summary_generated)
        self.assertIn("Escalations []PlanAuditEscalation `json:\"escalations,omitempty\"`", plan_audit_summary_generated)
        self.assertIn("DriftMetrics *PlanAuditDriftMetrics `json:\"drift_metrics,omitempty\"`", plan_audit_summary_generated)

        plan_audit_entry_schema = generate.load_schema_spec(
            "governance/get-plan-audit-logs-response.json#/properties/plans/items"
            "/properties/entries/items",
        )
        plan_audit_entry_generated = generate.schema_to_struct(
            "PlanAuditEntry",
            plan_audit_entry_schema,
        )
        self.assertIn("Verdict *GovernanceDecision `json:\"verdict,omitempty\"`", plan_audit_entry_generated)
        self.assertIn("Mode *GovernanceMode `json:\"mode,omitempty\"`", plan_audit_entry_generated)
        self.assertIn("Findings []PlanAuditFinding `json:\"findings,omitempty\"`", plan_audit_entry_generated)
        self.assertIn("Outcome *OutcomeType `json:\"outcome,omitempty\"`", plan_audit_entry_generated)
        self.assertIn("CommittedBudget *float64 `json:\"committed_budget,omitempty\"`", plan_audit_entry_generated)
        self.assertIn("PurchaseType *PurchaseType `json:\"purchase_type,omitempty\"`", plan_audit_entry_generated)

        plan_audit_finding_schema = generate.load_schema_spec(
            "governance/get-plan-audit-logs-response.json#/properties/plans/items"
            "/properties/entries/items/properties/findings/items",
        )
        plan_audit_finding_generated = generate.schema_to_struct(
            "PlanAuditFinding",
            plan_audit_finding_schema,
        )
        self.assertIn("Severity EscalationSeverity `json:\"severity\"`", plan_audit_finding_generated)
        self.assertIn("Confidence *float64 `json:\"confidence,omitempty\"`", plan_audit_finding_generated)

        plan_audit_action_schema = generate.load_schema_spec(
            "governance/get-plan-audit-logs-response.json#/properties/plans/items"
            "/properties/governed_actions/items",
        )
        plan_audit_action_generated = generate.schema_to_struct(
            "PlanAuditGovernedAction",
            plan_audit_action_schema,
        )
        self.assertIn("PurchaseType PurchaseType `json:\"purchase_type\"`", plan_audit_action_generated)
        self.assertIn("Committed float64 `json:\"committed\"`", plan_audit_action_generated)

    def test_typed_inline_objects_leave_unreviewed_any_coverage(self):
        records = [
            (record["type"], record["json"])
            for record in generate.any_coverage_report()["records"]
            if not record["allowed"]
        ]

        self.assertNotIn(("ListCreativesRequest", "sort"), records)
        self.assertNotIn(("ArtifactWebhookPayload", "pagination"), records)
        self.assertNotIn(("PerformanceFeedback", "measurement_period"), records)
        self.assertNotIn(("PlannedDelivery", "geo"), records)
        self.assertNotIn(("Account", "setup"), records)
        self.assertNotIn(("CreativeBrief", "messaging"), records)
        self.assertNotIn(("CreativeBrief", "compliance"), records)
        self.assertNotIn(("EventCustomData", "contents"), records)
        self.assertNotIn(("PolicyCategoryDefinition", "regulatory_frameworks"), records)
        self.assertNotIn(("CreativeFormat", "supported_macros"), records)
        self.assertNotIn(("UserMatch", "uids"), records)
        self.assertNotIn(("GetProductsRequest", "filters"), records)
        self.assertNotIn(("ProductFilters", "required_features"), records)
        self.assertNotIn(("ProductFilters", "signal_targeting"), records)
        self.assertNotIn(("Targeting", "geo_proximity"), records)
        self.assertNotIn(("CheckGovernanceRequest", "delivery_metrics"), records)
        self.assertNotIn(("ReportPlanOutcomeRequest", "delivery"), records)
        self.assertNotIn(("ReportPlanOutcomeRequest", "error"), records)
        self.assertNotIn(("ReportPlanOutcomeResponse", "plan_summary"), records)
        self.assertNotIn(("PolicyEntry", "exemplars"), records)
        self.assertNotIn(("GetProductsResponse", "incomplete"), records)
        self.assertNotIn(("SyncPlansResponse", "plans"), records)
        self.assertNotIn(("CheckGovernanceResponse", "findings"), records)
        self.assertNotIn(("ReportPlanOutcomeResponse", "findings"), records)
        self.assertNotIn(("CheckGovernanceResponse", "conditions"), records)
        self.assertNotIn(("GetPlanAuditLogsResponse", "plans"), records)

        allowed = [
            (record["type"], record["json"], record["allowance"])
            for record in generate.any_coverage_report()["records"]
            if record["allowed"]
        ]
        self.assertIn(
            (
                "ProductFilterGeometry",
                "coordinates",
                "GeoJSON coordinates are shape-dependent for Polygon/MultiPolygon",
            ),
            allowed,
        )
        self.assertIn(
            (
                "CheckGovernanceFinding",
                "details",
                "governance finding details are structured but category-specific",
            ),
            allowed,
        )
        self.assertIn(
            (
                "ReportPlanOutcomeFinding",
                "details",
                "outcome finding details are structured but category-specific",
            ),
            allowed,
        )
        self.assertIn(
            (
                "GeoProximityGeometry",
                "coordinates",
                "GeoJSON coordinates are shape-dependent for Polygon/MultiPolygon",
            ),
            allowed,
        )
        self.assertIn(
            (
                "CatalogFieldMapping",
                "value",
                "catalog static literal values can be strings, numbers, booleans, arrays, or objects",
            ),
            allowed,
        )
        self.assertIn(
            (
                "CatalogFieldMapping",
                "default",
                "catalog fallback literal values can be strings, numbers, booleans, arrays, or objects",
            ),
            allowed,
        )
        self.assertIn(
            (
                "MCPWebhookPayload",
                "result",
                "async webhook result is a task-specific response union",
            ),
            allowed,
        )


class AutoRefDiscoveryTest(unittest.TestCase):
    def setUp(self):
        self.original_core_schemas = generate.CORE_SCHEMAS
        self.original_support_schemas = generate.SUPPORT_SCHEMAS
        self.original_tool_schemas = generate.TOOL_SCHEMAS
        self.original_webhook_schemas = generate.WEBHOOK_SCHEMAS
        self.original_inline_schema_types = generate.INLINE_SCHEMA_TYPES
        self.original_union_schema_types = generate.UNION_SCHEMA_TYPES
        self.original_known_types = generate.KNOWN_TYPES
        self.original_load_schema = generate.load_schema
        self.original_schema_exists = generate.schema_exists
        self.original_ref_to_schema_path = generate.ref_to_schema_path
        generate._reset_will_generate_cache()

    def tearDown(self):
        generate.CORE_SCHEMAS = self.original_core_schemas
        generate.SUPPORT_SCHEMAS = self.original_support_schemas
        generate.TOOL_SCHEMAS = self.original_tool_schemas
        generate.WEBHOOK_SCHEMAS = self.original_webhook_schemas
        generate.INLINE_SCHEMA_TYPES = self.original_inline_schema_types
        generate.UNION_SCHEMA_TYPES = self.original_union_schema_types
        generate.KNOWN_TYPES = self.original_known_types
        generate.load_schema = self.original_load_schema
        generate.schema_exists = self.original_schema_exists
        generate.ref_to_schema_path = self.original_ref_to_schema_path
        generate._reset_will_generate_cache()

    def install_schema_fixture(self, schemas, core=None, tool=None):
        generate.CORE_SCHEMAS = list(core or [])
        generate.SUPPORT_SCHEMAS = []
        generate.TOOL_SCHEMAS = list(tool or [])
        generate.WEBHOOK_SCHEMAS = []
        generate.INLINE_SCHEMA_TYPES = OrderedDict()
        generate.UNION_SCHEMA_TYPES = OrderedDict()
        generate.KNOWN_TYPES = set()

        def schema_exists(path):
            return path in schemas

        def load_schema(path):
            return schemas[path]

        def ref_to_schema_path(ref):
            prefix = "/schemas/test/"
            if not isinstance(ref, str) or not ref.startswith(prefix):
                return None
            path = ref[len(prefix):].split("#", 1)[0]
            return path if path in schemas else None

        generate.schema_exists = schema_exists
        generate.load_schema = load_schema
        generate.ref_to_schema_path = ref_to_schema_path
        generate._reset_will_generate_cache()

    def test_auto_ref_discovery_exports_only_clean_reachable_object_types(self):
        paths = set(generate.auto_ref_schema_paths())

        self.assertIn("core/creative-variable.json", paths)
        self.assertIn("core/media-buy-features.json", paths)
        self.assertNotIn("content-standards/artifact.json", paths)
        self.assertNotIn("core/overlay.json", paths)

        unreviewed_auto_ref = [
            (record["type"], record["json"], record["reason"])
            for record in generate.any_coverage_report()["records"]
            if record["section"] == "auto_ref" and not record["allowed"]
        ]
        self.assertEqual([], unreviewed_auto_ref)

    def test_discriminated_oneof_object_refs_are_auto_generatable(self):
        schema = {
            "discriminator": {"propertyName": "scope"},
            "oneOf": [
                {
                    "type": "object",
                    "properties": {
                        "scope": {"const": "product"},
                        "signal_id": {"type": "string"},
                    },
                },
                {
                    "type": "object",
                    "properties": {
                        "scope": {"const": "data_provider"},
                        "data_provider_domain": {"type": "string"},
                    },
                },
            ],
        }

        self.assertTrue(generate.can_auto_generate_ref_schema(schema))

    def test_local_string_schema_refs_resolve_to_string(self):
        go_type, reason = generate.resolve_go_type_info({
            "$ref": "/schemas/test/core/x-entity-types.json",
        })

        self.assertEqual("string", go_type)
        self.assertIsNone(reason)

    def test_validation_only_allof_resolves_to_ref_type(self):
        go_type, reason = generate.resolve_go_type_info({
            "allOf": [
                {"$ref": "/schemas/test/core/publisher-property-selector.json"},
                {"not": {"required": ["publisher_domains"]}},
            ],
        })

        self.assertEqual("PublisherPropertySelector", go_type)
        self.assertIsNone(reason)

    def test_structural_allof_without_named_inline_type_stays_unreviewed(self):
        go_type, reason = generate.resolve_go_type_info({
            "allOf": [
                {"$ref": "/schemas/test/core/account.json"},
                {
                    "type": "object",
                    "properties": {
                        "authorization": {"type": "string"},
                    },
                },
            ],
        })

        self.assertEqual("any", go_type)
        self.assertEqual("unsupported_allOf", reason)

    def test_auto_ref_discovery_traverses_unemitted_wrapper_schemas(self):
        self.install_schema_fixture(
            {
                "root.json": {
                    "type": "object",
                    "properties": {
                        "wrapper": {"$ref": "/schemas/test/wrapper.json"},
                    },
                },
                "wrapper.json": {
                    "type": "object",
                    "properties": {
                        "leaf": {"$ref": "/schemas/test/leaf.json"},
                        "open": {
                            "type": "object",
                            "properties": {
                                "nested": {"type": "string"},
                            },
                        },
                    },
                },
                "leaf.json": {
                    "type": "object",
                    "properties": {
                        "leaf_id": {"type": "string"},
                    },
                },
            },
            core=["root.json"],
        )

        self.assertEqual(("leaf.json",), generate.auto_ref_schema_paths())

    def test_auto_ref_filter_is_fixed_point_for_dropped_peer_refs(self):
        self.install_schema_fixture(
            {
                "root.json": {
                    "type": "object",
                    "properties": {
                        "a": {"$ref": "/schemas/test/a.json"},
                    },
                },
                "a.json": {
                    "type": "object",
                    "properties": {
                        "b": {"$ref": "/schemas/test/b.json"},
                    },
                },
                "b.json": {
                    "type": "object",
                    "properties": {
                        "open": {
                            "type": "object",
                            "properties": {
                                "nested": {"type": "string"},
                            },
                        },
                    },
                },
            },
            core=["root.json"],
        )

        self.assertEqual((), generate.auto_ref_schema_paths())

    def test_auto_ref_discovery_fails_on_name_collision(self):
        self.install_schema_fixture(
            {
                "root.json": {
                    "type": "object",
                    "properties": {
                        "a": {"$ref": "/schemas/test/alpha/shared.json"},
                        "b": {"$ref": "/schemas/test/beta/shared.json"},
                    },
                },
                "alpha/shared.json": {
                    "type": "object",
                    "properties": {"a": {"type": "string"}},
                },
                "beta/shared.json": {
                    "type": "object",
                    "properties": {"b": {"type": "string"}},
                },
            },
            core=["root.json"],
        )

        with self.assertRaisesRegex(ValueError, "auto-discovered schema type name collision"):
            generate.auto_ref_schema_paths()

    def test_tool_allof_schema_is_generated_and_covered(self):
        self.install_schema_fixture(
            {
                "tool.json": {
                    "allOf": [
                        {
                            "type": "object",
                            "properties": {"id": {"type": "string"}},
                            "required": ["id"],
                        },
                        {
                            "type": "object",
                            "properties": {"name": {"type": "string"}},
                        },
                    ],
                },
            },
            tool=["tool.json"],
        )

        entries = list(generate.generated_schema_entries())

        self.assertEqual(1, len(entries))
        self.assertEqual("Tool", entries[0]["name"])
        self.assertEqual("struct", entries[0]["kind"])
        generated = generate.schema_to_struct("Tool", entries[0]["schema_obj"])
        self.assertIn("ID string `json:\"id\"`", generated)
        self.assertIn("Name string `json:\"name,omitempty\"`", generated)


class PackageSchemaOwnershipTest(unittest.TestCase):
    def assert_generated_struct_fields(self, schema_path, type_name, expected_fields):
        schema = generate.load_schema(schema_path)
        generated = generate.schema_to_struct(type_name, schema)

        self.assertNotIn("BuyerRef", generated)
        self.assertNotIn("buyer_ref", generated)
        for field in expected_fields:
            with self.subTest(type_name=type_name, field=field):
                self.assertIn(field, generated)

    def test_package_input_is_schema_owned(self):
        self.assert_generated_struct_fields(
            "media-buy/package-request.json",
            "PackageInput",
            [
                "FormatIDs []FormatRef `json:\"format_ids,omitempty\"`",
                "Paused *bool `json:\"paused,omitempty\"`",
                "Catalogs []Catalog `json:\"catalogs,omitempty\"`",
                "OptimizationGoals []OptimizationGoal `json:\"optimization_goals,omitempty\"`",
                "CreativeAssignments []CreativeAssignment `json:\"creative_assignments,omitempty\"`",
                "Creatives []CreativeAsset `json:\"creatives,omitempty\"`",
                "BidPrice *float64 `json:\"bid_price,omitempty\"`",
                "Impressions *float64 `json:\"impressions,omitempty\"`",
                "Ext any `json:\"ext,omitempty\"`",
            ],
        )

    def test_package_update_is_schema_owned(self):
        self.assert_generated_struct_fields(
            "media-buy/package-update.json",
            "PackageUpdate",
            [
                "Paused *bool `json:\"paused,omitempty\"`",
                "Canceled *bool `json:\"canceled,omitempty\"`",
                "Catalogs []Catalog `json:\"catalogs,omitempty\"`",
                "OptimizationGoals []OptimizationGoal `json:\"optimization_goals,omitempty\"`",
                "KeywordTargetsAdd []KeywordTargetUpdate `json:\"keyword_targets_add,omitempty\"`",
                "KeywordTargetsRemove []KeywordTargetRef `json:\"keyword_targets_remove,omitempty\"`",
                "NegativeKeywordsAdd []KeywordTargetRef `json:\"negative_keywords_add,omitempty\"`",
                "NegativeKeywordsRemove []KeywordTargetRef `json:\"negative_keywords_remove,omitempty\"`",
                "CreativeAssignments []CreativeAssignment `json:\"creative_assignments,omitempty\"`",
                "Creatives []CreativeAsset `json:\"creatives,omitempty\"`",
                "Budget *float64 `json:\"budget,omitempty\"`",
                "BidPrice *float64 `json:\"bid_price,omitempty\"`",
                "Impressions *float64 `json:\"impressions,omitempty\"`",
                "Ext any `json:\"ext,omitempty\"`",
            ],
        )


if __name__ == "__main__":
    unittest.main()
