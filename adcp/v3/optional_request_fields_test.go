package adcp

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The generator emits Optional[T] for every non-required request property whose
// schema has a null branch, in a struct only requests reach (generate.py,
// request_only_struct_names). These tests pin the fields that rule gives the
// typed request structs, and what they do on the wire.

// optionalRequestField names one Optional field of a generated request struct.
type optionalRequestField struct {
	parent string
	newV   func() any // pointer to a zero value of the parent
	tag    string
	value  string // a JSON value for the field
	zero   string // a JSON zero value, when the type has a meaningful one
	// released is true for a field that exists in adcp/v3.2.1 with another type.
	released bool
}

const (
	capValue     = `{"max_impressions":3,"per":"devices","window":{"interval":1,"unit":"days"}}`
	biddingValue = `{"automatic":true}`
)

func optionalRequestFields() []optionalRequestField {
	update := func() any { return &UpdateMediaBuyRequest{} }
	pkg := func() any { return &PackageUpdate{} }
	control := func() any { return &ControlMediaBuyRequest{} }
	ctlPkg := func() any { return &PackageControl{} }
	return []optionalRequestField{
		{"UpdateMediaBuyRequest", update, "daily_budget_cap", `250.5`, `0`, true},
		{"UpdateMediaBuyRequest", update, "budget_cap_timezone", `"Europe/Berlin"`, ``, true},
		{"UpdateMediaBuyRequest", update, "frequency_cap", capValue, ``, false},
		{"UpdateMediaBuyRequest", update, "bidding", biddingValue, ``, true},
		{"PackageUpdate", pkg, "budget", `250.5`, `0`, true},
		{"PackageUpdate", pkg, "min_spend_target", `250.5`, `0`, true},
		{"PackageUpdate", pkg, "daily_budget_cap", `250.5`, `0`, true},
		{"PackageUpdate", pkg, "bidding", biddingValue, ``, true},
		{"ControlMediaBuyRequest", control, "daily_budget_cap", `250.5`, `0`, false},
		{"ControlMediaBuyRequest", control, "budget_cap_timezone", `"Europe/Berlin"`, ``, false},
		{"ControlMediaBuyRequest", control, "frequency_cap", capValue, ``, false},
		{"ControlMediaBuyRequest", control, "bidding", biddingValue, ``, false},
		{"PackageControl", ctlPkg, "budget", `250.5`, `0`, false},
		{"PackageControl", ctlPkg, "min_spend_target", `250.5`, `0`, false},
		{"PackageControl", ctlPkg, "daily_budget_cap", `250.5`, `0`, false},
		{"PackageControl", ctlPkg, "bidding", biddingValue, ``, false},
	}
}

// optionalFieldState reads the Optional field of v tagged tag: its state
// ("omitted", "null" or "value") and, for a value, the value.
func optionalFieldState(t *testing.T, v any, tag string) (string, any) {
	t.Helper()
	rv := reflect.ValueOf(v).Elem()
	for i := 0; i < rv.NumField(); i++ {
		name, _, _ := strings.Cut(rv.Type().Field(i).Tag.Get("json"), ",")
		if name != tag {
			continue
		}
		f := rv.Field(i)
		require.True(t, isOptionalType(f.Type()), "%v.%s is %v, not an Optional", rv.Type(), rv.Type().Field(i).Name, f.Type())
		switch {
		case f.MethodByName("IsZero").Call(nil)[0].Bool():
			return "omitted", nil
		case f.MethodByName("IsNull").Call(nil)[0].Bool():
			return "null", nil
		}
		got := f.MethodByName("Get").Call(nil)
		require.True(t, got[1].Bool())
		return "value", got[0].Interface()
	}
	require.FailNow(t, "no field tagged "+tag, "%v", rv.Type())
	return "", nil
}

func wireKeys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &wire))
	return wire
}

// Each field survives decode and encode as omitted, null and a value, and a
// zero is a value. Before Optional, null decoded like omission and the key was
// dropped on encode.
func TestOptionalRequestFieldsKeepThreeStates(t *testing.T) {
	for _, f := range optionalRequestFields() {
		t.Run(f.parent+"."+f.tag, func(t *testing.T) {
			// The field is an Optional, and an Optional of a pointer type is never used.
			rt := reflect.TypeOf(f.newV()).Elem()
			sf, ok := fieldByJSONName(rt, f.tag)
			require.True(t, ok)
			assert.True(t, isOptionalType(sf.Type))
			assert.Equal(t, f.tag+",omitzero", sf.Tag.Get("json"))

			decode := func(raw string) any {
				v := f.newV()
				require.NoError(t, json.Unmarshal([]byte(raw), v))
				return v
			}
			field := func(v any) string {
				wire := wireKeys(t, v)[f.tag]
				return string(wire)
			}

			// omitted: no key in, no key out.
			state, _ := optionalFieldState(t, decode(`{}`), f.tag)
			assert.Equal(t, "omitted", state)
			assert.NotContains(t, wireKeys(t, decode(`{}`)), f.tag)

			// null: a seller can tell it from omission, and the key is kept on re-encode.
			nulled := decode(`{"` + f.tag + `":null}`)
			state, _ = optionalFieldState(t, nulled, f.tag)
			assert.Equal(t, "null", state)
			assert.Equal(t, "null", field(nulled))

			// a value.
			valued := decode(`{"` + f.tag + `":` + f.value + `}`)
			state, got := optionalFieldState(t, valued, f.tag)
			assert.Equal(t, "value", state)
			gotJSON, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, f.value, string(gotJSON))
			assert.JSONEq(t, f.value, field(valued))

			// a zero is a value, not omission and not null.
			if f.zero != "" {
				zeroed := decode(`{"` + f.tag + `":` + f.zero + `}`)
				state, got = optionalFieldState(t, zeroed, f.tag)
				assert.Equal(t, "value", state)
				assert.Equal(t, float64(0), got)
				assert.Equal(t, f.zero, field(zeroed))
			}

			// decode, encode, decode again gives the same state for all three.
			for _, raw := range []string{`{}`, `{"` + f.tag + `":null}`, `{"` + f.tag + `":` + f.value + `}`} {
				first := decode(raw)
				out, err := json.Marshal(first)
				require.NoError(t, err)
				assert.Equal(t, first, decode(string(out)), raw)
			}
		})
	}
}

func fieldByJSONName(rt reflect.Type, tag string) (reflect.StructField, bool) {
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == tag {
			return rt.Field(i), true
		}
	}
	return reflect.StructField{}, false
}

// A buyer builds the request with Some and Null, and the wire says what was
// asked, nested inside a package patch too.
func TestUpdateMediaBuyRequestBuilderEncodesEachState(t *testing.T) {
	req := UpdateMediaBuyRequest{
		IdempotencyKey:    "update-mb-0000001",
		Account:           AccountReference{AccountID: "acc_1"},
		MediaBuyID:        "mb_1",
		DailyBudgetCap:    Some(0.0),
		BudgetCapTimezone: Null[string](),
		Bidding:           Null[any](),
		Packages: []PackageUpdate{{
			PackageID:      "pkg_1",
			Budget:         Some(250.5),
			MinSpendTarget: Null[float64](),
			Bidding:        Some[any](map[string]any{"automatic": true}),
		}},
	}
	out, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"idempotency_key":"update-mb-0000001","account":{"account_id":"acc_1"},"media_buy_id":"mb_1",
		"daily_budget_cap":0,"budget_cap_timezone":null,"bidding":null,
		"packages":[{"package_id":"pkg_1","budget":250.5,"min_spend_target":null,"bidding":{"automatic":true}}]
	}`, string(out))
	assertSchemaValid(t, "media-buy/update-media-buy-request.json", out)

	// frequency_cap, left out, is not sent; null is.
	assert.NotContains(t, string(out), "frequency_cap")
	req.FrequencyCap = Null[any]()
	out, err = json.Marshal(req)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"frequency_cap":null`)
	assertSchemaValid(t, "media-buy/update-media-buy-request.json", out)
}

// Some of a nil any would send null, not an empty value; the request refuses it.
func TestUpdateMediaBuyRequestRefusesSomeOfNilAny(t *testing.T) {
	_, err := json.Marshal(UpdateMediaBuyRequest{Bidding: Some[any](nil)})
	assert.ErrorIs(t, err, ErrOptionalNilValue)
}

// A request through the SDK's tool path: null reaches the handler as IsNull,
// omission as IsZero, and 0 as a value, for the media buy and for each package.
func TestUpdateMediaBuyThroughTheHandlerPathKeepsNull(t *testing.T) {
	const key = "update-mb-0000001"
	register := func(server *mcp.Server, handler func(UpdateMediaBuyRequest)) {
		AddTool(server, "update_media_buy", "update", func(_ context.Context, _ *mcp.CallToolRequest, in UpdateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
			handler(in)
			return &mcp.CallToolResult{}, nil, nil
		})
	}

	run := func(t *testing.T, server *mcp.Server, got *UpdateMediaBuyRequest) {
		t.Helper()
		cs := connectInMemory(t, server)
		call := func(args map[string]any) UpdateMediaBuyRequest {
			t.Helper()
			*got = UpdateMediaBuyRequest{}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "update_media_buy", Arguments: args})
			require.NoError(t, err)
			require.False(t, res.IsError, "%+v", res.Content)
			return *got
		}
		base := func(extra map[string]any) map[string]any {
			args := map[string]any{"idempotency_key": key, "account": map[string]any{"account_id": "acc_1"}, "media_buy_id": "mb_1"}
			for k, v := range extra {
				args[k] = v
			}
			return args
		}

		omitted := call(base(nil))
		assert.True(t, omitted.DailyBudgetCap.IsZero())
		assert.True(t, omitted.BudgetCapTimezone.IsZero())
		assert.True(t, omitted.FrequencyCap.IsZero())
		assert.True(t, omitted.Bidding.IsZero())

		cleared := call(base(map[string]any{
			"daily_budget_cap": nil, "budget_cap_timezone": nil, "frequency_cap": nil, "bidding": nil,
			"packages": []any{map[string]any{"package_id": "pkg_1", "budget": nil, "min_spend_target": nil, "daily_budget_cap": nil, "bidding": nil}},
		}))
		assert.True(t, cleared.DailyBudgetCap.IsNull())
		assert.True(t, cleared.BudgetCapTimezone.IsNull())
		assert.True(t, cleared.FrequencyCap.IsNull())
		assert.True(t, cleared.Bidding.IsNull())
		require.Len(t, cleared.Packages, 1)
		pkg := cleared.Packages[0]
		assert.True(t, pkg.Budget.IsNull())
		assert.True(t, pkg.MinSpendTarget.IsNull())
		assert.True(t, pkg.DailyBudgetCap.IsNull())
		assert.True(t, pkg.Bidding.IsNull())

		valued := call(base(map[string]any{
			"daily_budget_cap": 0, "budget_cap_timezone": "UTC",
			"packages": []any{map[string]any{"package_id": "pkg_1", "budget": 0, "bidding": map[string]any{"automatic": true}}},
		}))
		v, ok := valued.DailyBudgetCap.Get()
		assert.True(t, ok)
		assert.Equal(t, 0.0, v)
		tz, ok := valued.BudgetCapTimezone.Get()
		assert.True(t, ok)
		assert.Equal(t, "UTC", tz)
		budget, ok := valued.Packages[0].Budget.Get()
		assert.True(t, ok)
		assert.Equal(t, 0.0, budget)
		bidding, ok := valued.Packages[0].Bidding.Get()
		assert.True(t, ok)
		assert.Equal(t, map[string]any{"automatic": true}, bidding)
		assert.True(t, valued.Packages[0].MinSpendTarget.IsZero())
	}

	t.Run("AddTool", func(t *testing.T) {
		server := mcp.NewServer(&mcp.Implementation{Name: "update-test", Version: "v0.0.1"}, nil)
		var got UpdateMediaBuyRequest
		register(server, func(in UpdateMediaBuyRequest) { got = in })
		run(t, server, &got)
	})

	// The idempotency wrapper a seller puts on a mutating tool hands the
	// handler the same input.
	t.Run("WithIdempotency", func(t *testing.T) {
		store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
		server := mcp.NewServer(&mcp.Implementation{Name: "update-test", Version: "v0.0.1"}, nil)
		server.AddReceivingMiddleware(withTestPrincipal("test-buyer"))
		var got UpdateMediaBuyRequest
		AddTool(server, "update_media_buy", "update", WithIdempotency(store, func(_ context.Context, _ *mcp.CallToolRequest, in UpdateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
			got = in
			return &mcp.CallToolResult{}, map[string]any{}, nil
		}))
		cs := connectInMemory(t, server)
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "update_media_buy", Arguments: map[string]any{
			"idempotency_key": key, "account": map[string]any{"account_id": "acc_1"}, "media_buy_id": "mb_1",
			"daily_budget_cap": nil,
		}})
		require.NoError(t, err)
		require.False(t, res.IsError, "%+v", res.Content)
		assert.True(t, got.DailyBudgetCap.IsNull())
		assert.True(t, got.Bidding.IsZero())
	})
}

// rebuildOptionals returns rt with every Optional field replaced by the type
// replace gives for its T, and false when rt holds no Optional. It rebuilds only
// the structs that contain an Optional.
func rebuildOptionals(rt reflect.Type, replace func(elem reflect.Type) reflect.Type) (reflect.Type, bool) {
	switch rt.Kind() {
	case reflect.Pointer:
		elem, changed := rebuildOptionals(rt.Elem(), replace)
		if changed {
			return reflect.PointerTo(elem), true
		}
	case reflect.Slice:
		elem, changed := rebuildOptionals(rt.Elem(), replace)
		if changed {
			return reflect.SliceOf(elem), true
		}
	case reflect.Map:
		elem, changed := rebuildOptionals(rt.Elem(), replace)
		if changed {
			return reflect.MapOf(rt.Key(), elem), true
		}
	case reflect.Struct:
		if isOptionalType(rt) {
			elem, _ := rebuildOptionals(optionalElemType(rt), replace)
			return replace(elem), true
		}
		fields := make([]reflect.StructField, rt.NumField())
		changed := false
		for i := range fields {
			f := rt.Field(i)
			if !f.IsExported() || f.Anonymous {
				panic(fmt.Sprintf("rebuildOptionals: %v.%s is unexported or embedded", rt, f.Name))
			}
			ft, c := rebuildOptionals(f.Type, replace)
			changed = changed || c
			tag := f.Tag
			if isOptionalType(f.Type) {
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				tag = reflect.StructTag(`json:"` + name + `,omitempty"`)
			}
			fields[i] = reflect.StructField{Name: f.Name, Type: ft, Tag: tag}
		}
		if changed {
			return reflect.StructOf(fields), true
		}
	}
	return rt, false
}

// legacyType is the Go type a struct had before its Optional fields were
// *T for a scalar or struct, T for an interface, slice or map, as the generator
// emitted them for update_media_buy.
func legacyType(rt reflect.Type) (reflect.Type, bool) {
	return rebuildOptionals(rt, func(elem reflect.Type) reflect.Type {
		switch elem.Kind() {
		case reflect.Interface, reflect.Slice, reflect.Map:
			return elem
		}
		return reflect.PointerTo(elem)
	})
}

// anyLegacyType is the type control_media_buy's request had before its Optional
// fields: every one of them was `any`.
func anyLegacyType(rt reflect.Type) (reflect.Type, bool) {
	return rebuildOptionals(rt, func(reflect.Type) reflect.Type { return reflect.TypeFor[any]() })
}

// The advertised input schema of a tool does not change when a field moves
// from *T (or any) to Optional[T]: tools/list for update_media_buy is what it
// was. The comparison type is the same struct built with the pre-swap field
// types. Optional fields that were `any` stay unadvertised, as `any` always was.
func TestUpdateMediaBuyAdvertisedSchemaIsUnchangedBySwap(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeFor[UpdateMediaBuyRequest](),
		reflect.TypeFor[PackageUpdate](),
	} {
		t.Run(rt.Name(), func(t *testing.T) {
			legacy, changed := legacyType(rt)
			require.True(t, changed, "the type has Optional fields")
			require.NotEqual(t, rt, legacy)

			got := schemaJSON(t, permissiveSchemaForType(rt))
			want := schemaJSON(t, permissiveSchemaForType(legacy))
			assert.Equal(t, want, got)

			// Not vacuous: the swapped properties are advertised, typed as before.
			assert.NotEqual(t, fallbackSchemaJSON(t), got, "the schema is the typed one, not the fallback")
			assert.Contains(t, got, `"daily_budget_cap":{"type":["null","number"]`)
		})
	}

	// The same through tools/list.
	got := listedInputSchema[UpdateMediaBuyRequest](t)
	legacy, _ := legacyType(reflect.TypeFor[UpdateMediaBuyRequest]())
	assert.JSONEq(t, schemaJSON(t, permissiveSchemaForType(legacy)), got)
	assert.Contains(t, got, `"budget_cap_timezone":{"type":["null","string"]`)
	assert.Contains(t, got, `"min_spend_target":{"type":["null","number"]`)
}

// The generator emits omitzero, never omitempty, for every Optional field
// (generate.py, schema_to_struct). The check reads types_gen.go, so it covers
// every generated struct, and then the reachable types by reflection.
func TestGeneratedOptionalFieldsAreTaggedOmitZero(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "types_gen.go", nil, 0)
	require.NoError(t, err)

	var found int
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				idx, ok := field.Type.(*ast.IndexExpr)
				if !ok {
					continue
				}
				if x, ok := idx.X.(*ast.Ident); !ok || x.Name != "Optional" {
					continue
				}
				found++
				name := ts.Name.Name + "." + field.Names[0].Name
				require.NotNil(t, field.Tag, name)
				tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
				_, opts, _ := strings.Cut(tag, ",")
				assert.Equal(t, "omitzero", opts, "%s is tagged %q: an Optional field takes omitzero only", name, tag)
			}
		}
	}
	assert.Equal(t, len(optionalRequestFields()), found, "each Optional field of a generated struct is in optionalRequestFields")

	for _, v := range []any{UpdateMediaBuyRequest{}, ControlMediaBuyRequest{}, PackageUpdate{}, PackageControl{}} {
		requireOptionalOmitZero(t, v)
	}
}

// The control_media_buy request type changes its advertised schema on purpose:
// its nullable numeric and string controls were `any`, which AddTool leaves out
// of tools/list, and are Optional[T], advertised as the *T they stand for.
// This pins the new entries exactly and requires the rest of the tool's schema
// to be what it was.
func TestControlMediaBuyAdvertisedSchemaChangesOnlyInTheFiveTypedControls(t *testing.T) {
	rt := reflect.TypeFor[ControlMediaBuyRequest]()
	legacy, changed := anyLegacyType(rt)
	require.True(t, changed)

	toMap := func(raw string) map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &m))
		return m
	}
	// The same through the real tools/list path as the schema AddTool builds.
	listed := toMap(listedInputSchema[ControlMediaBuyRequest](t))
	assert.Equal(t, toMap(schemaJSON(t, permissiveSchemaForType(rt))), listed)

	props := func(schema map[string]any) map[string]any {
		return schema["properties"].(map[string]any)
	}
	packageProps := func(schema map[string]any) map[string]any {
		return props(props(schema)["packages"].(map[string]any)["items"].(map[string]any))
	}

	number := map[string]any{"type": []any{"null", "number"}}
	text := map[string]any{"type": []any{"null", "string"}}
	// Exactly these five gain an entry.
	assert.Equal(t, number, props(listed)["daily_budget_cap"])
	assert.Equal(t, text, props(listed)["budget_cap_timezone"])
	assert.Equal(t, number, packageProps(listed)["budget"])
	assert.Equal(t, number, packageProps(listed)["min_spend_target"])
	assert.Equal(t, number, packageProps(listed)["daily_budget_cap"])
	// An Optional[any] stays unadvertised, as the any it replaces always was.
	for _, name := range []string{"bidding", "frequency_cap"} {
		assert.NotContains(t, props(listed), name)
		assert.NotContains(t, packageProps(listed), name)
	}

	// Without those five entries the schema is the legacy one, byte for byte
	// once the JSON is normalised.
	delete(props(listed), "daily_budget_cap")
	delete(props(listed), "budget_cap_timezone")
	for _, name := range []string{"budget", "min_spend_target", "daily_budget_cap"} {
		delete(packageProps(listed), name)
	}
	assert.Equal(t, toMap(schemaJSON(t, permissiveSchemaForType(legacy))), listed)
}

// A control_media_buy call over MCP: null reaches the handler as IsNull at the
// media-buy and package level, omission as IsZero, and 0 as a value.
func TestControlMediaBuyThroughTheHandlerPathKeepsNull(t *testing.T) {
	// Each call carries its own key: a key is fenced once used, and a repeat
	// with other arguments is a conflict.
	var calls int
	base := func(extra map[string]any) map[string]any {
		calls++
		args := map[string]any{
			"idempotency_key": fmt.Sprintf("control-mb-%07d", calls), "account": map[string]any{"account_id": "acc_1"},
			"media_buy_id": "mb_1", "revision": 3,
		}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}

	run := func(t *testing.T, cs *mcp.ClientSession, got *ControlMediaBuyRequest) {
		t.Helper()
		call := func(args map[string]any) ControlMediaBuyRequest {
			t.Helper()
			*got = ControlMediaBuyRequest{}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "control_media_buy", Arguments: args})
			require.NoError(t, err)
			require.False(t, res.IsError, "%+v", res.Content)
			return *got
		}

		omitted := call(base(map[string]any{"paused": true}))
		assert.True(t, omitted.DailyBudgetCap.IsZero())
		assert.True(t, omitted.BudgetCapTimezone.IsZero())
		assert.True(t, omitted.FrequencyCap.IsZero())
		assert.True(t, omitted.Bidding.IsZero())

		cleared := call(base(map[string]any{
			"daily_budget_cap": nil, "budget_cap_timezone": nil, "frequency_cap": nil, "bidding": nil,
			"packages": []any{map[string]any{"package_id": "pkg_1", "budget": nil, "min_spend_target": nil, "daily_budget_cap": nil, "bidding": nil}},
		}))
		assert.True(t, cleared.DailyBudgetCap.IsNull())
		assert.True(t, cleared.BudgetCapTimezone.IsNull())
		assert.True(t, cleared.FrequencyCap.IsNull())
		assert.True(t, cleared.Bidding.IsNull())
		require.Len(t, cleared.Packages, 1)
		pkg := cleared.Packages[0]
		assert.True(t, pkg.Budget.IsNull())
		assert.True(t, pkg.MinSpendTarget.IsNull())
		assert.True(t, pkg.DailyBudgetCap.IsNull())
		assert.True(t, pkg.Bidding.IsNull())

		valued := call(base(map[string]any{
			"daily_budget_cap": 0, "budget_cap_timezone": "UTC",
			"packages": []any{map[string]any{"package_id": "pkg_1", "budget": 0, "min_spend_target": 0, "daily_budget_cap": 0, "bidding": map[string]any{"automatic": true}}},
		}))
		capValue, ok := valued.DailyBudgetCap.Get()
		assert.True(t, ok)
		assert.Equal(t, 0.0, capValue)
		timezone, ok := valued.BudgetCapTimezone.Get()
		assert.True(t, ok)
		assert.Equal(t, "UTC", timezone)
		require.Len(t, valued.Packages, 1)
		for name, field := range map[string]Optional[float64]{
			"budget": valued.Packages[0].Budget, "min_spend_target": valued.Packages[0].MinSpendTarget,
			"daily_budget_cap": valued.Packages[0].DailyBudgetCap,
		} {
			v, ok := field.Get()
			assert.True(t, ok, name)
			assert.Equal(t, 0.0, v, name)
		}
		bidding, ok := valued.Packages[0].Bidding.Get()
		assert.True(t, ok)
		assert.Equal(t, map[string]any{"automatic": true}, bidding)
		assert.True(t, valued.Bidding.IsZero())
	}

	t.Run("AddTool", func(t *testing.T) {
		server := mcp.NewServer(&mcp.Implementation{Name: "control-test", Version: "v0.0.1"}, nil)
		var got ControlMediaBuyRequest
		AddTool(server, "control_media_buy", "control", func(_ context.Context, _ *mcp.CallToolRequest, in ControlMediaBuyRequest) (*mcp.CallToolResult, any, error) {
			got = in
			return &mcp.CallToolResult{}, nil, nil
		})
		run(t, connectInMemory(t, server), &got)
	})

	t.Run("WithIdempotency", func(t *testing.T) {
		store := idempotency.New(idempotency.Options{Backend: idempotency.NewMemoryBackend(0), TTL: 24 * time.Hour})
		server := mcp.NewServer(&mcp.Implementation{Name: "control-test", Version: "v0.0.1"}, nil)
		server.AddReceivingMiddleware(withTestPrincipal("test-buyer"))
		var got ControlMediaBuyRequest
		AddTool(server, "control_media_buy", "control", WithIdempotency(store, func(_ context.Context, _ *mcp.CallToolRequest, in ControlMediaBuyRequest) (*mcp.CallToolResult, any, error) {
			got = in
			return &mcp.CallToolResult{}, map[string]any{}, nil
		}))
		run(t, connectInMemory(t, server), &got)
	})
}
