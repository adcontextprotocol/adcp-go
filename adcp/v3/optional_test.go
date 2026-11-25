package adcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type optionalInner struct {
	A string `json:"a"`
}

// optionalFixture carries one field per kind of T that a request field can be.
type optionalFixture struct {
	Cap  Optional[float64]        `json:"cap,omitzero"`
	Name Optional[string]         `json:"name,omitzero"`
	Flag Optional[bool]           `json:"flag,omitzero"`
	Tags Optional[[]string]       `json:"tags,omitzero"`
	Meta Optional[map[string]int] `json:"meta,omitzero"`
	Obj  Optional[optionalInner]  `json:"obj,omitzero"`
}

// optionalState names the state of an Optional for failure messages.
func optionalStateOf[T any](o Optional[T]) string {
	if o.IsNull() {
		return "null"
	}
	if v, ok := o.Get(); ok {
		return fmt.Sprintf("value:%v", v)
	}
	return "omitted"
}

func TestOptionalRoundTripKeepsThreeStates(t *testing.T) {
	cases := []struct {
		name  string
		wire  string
		state func(optionalFixture) string
		want  string
	}{
		{"cap omitted", `{}`, func(f optionalFixture) string { return optionalStateOf(f.Cap) }, "omitted"},
		{"cap null", `{"cap":null}`, func(f optionalFixture) string { return optionalStateOf(f.Cap) }, "null"},
		{"cap value", `{"cap":250.5}`, func(f optionalFixture) string { return optionalStateOf(f.Cap) }, "value:250.5"},
		{"cap zero", `{"cap":0}`, func(f optionalFixture) string { return optionalStateOf(f.Cap) }, "value:0"},
		{"name null", `{"name":null}`, func(f optionalFixture) string { return optionalStateOf(f.Name) }, "null"},
		{"name empty string", `{"name":""}`, func(f optionalFixture) string { return optionalStateOf(f.Name) }, "value:"},
		{"flag false", `{"flag":false}`, func(f optionalFixture) string { return optionalStateOf(f.Flag) }, "value:false"},
		{"flag null", `{"flag":null}`, func(f optionalFixture) string { return optionalStateOf(f.Flag) }, "null"},
		{"tags null", `{"tags":null}`, func(f optionalFixture) string { return optionalStateOf(f.Tags) }, "null"},
		{"tags empty list", `{"tags":[]}`, func(f optionalFixture) string { return optionalStateOf(f.Tags) }, "value:[]"},
		{"tags list", `{"tags":["a","b"]}`, func(f optionalFixture) string { return optionalStateOf(f.Tags) }, "value:[a b]"},
		{"meta null", `{"meta":null}`, func(f optionalFixture) string { return optionalStateOf(f.Meta) }, "null"},
		{"meta empty object", `{"meta":{}}`, func(f optionalFixture) string { return optionalStateOf(f.Meta) }, "value:map[]"},
		{"meta object", `{"meta":{"k":0}}`, func(f optionalFixture) string { return optionalStateOf(f.Meta) }, "value:map[k:0]"},
		{"obj null", `{"obj":null}`, func(f optionalFixture) string { return optionalStateOf(f.Obj) }, "null"},
		{"obj zero struct", `{"obj":{"a":""}}`, func(f optionalFixture) string { return optionalStateOf(f.Obj) }, "value:{}"},
		{"obj value", `{"obj":{"a":"x"}}`, func(f optionalFixture) string { return optionalStateOf(f.Obj) }, "value:{x}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded optionalFixture
			require.NoError(t, json.Unmarshal([]byte(tc.wire), &decoded))
			assert.Equal(t, tc.want, tc.state(decoded), "decode")

			// Encoding gives the same JSON back: nothing is dropped, nothing invented.
			encoded, err := json.Marshal(decoded)
			require.NoError(t, err)
			assert.Equal(t, tc.wire, string(encoded), "encode")

			var again optionalFixture
			require.NoError(t, json.Unmarshal(encoded, &again))
			assert.Equal(t, decoded, again, "second decode")
		})
	}
}

func TestOptionalNullAndAbsentAreDistinctOnDecodeAndEncode(t *testing.T) {
	var absent, null optionalFixture
	require.NoError(t, json.Unmarshal([]byte(`{}`), &absent))
	require.NoError(t, json.Unmarshal([]byte(`{"cap":null}`), &null))

	// Decode: a seller can tell clear from leave-unchanged.
	assert.True(t, absent.Cap.IsZero())
	assert.False(t, absent.Cap.IsNull())
	assert.False(t, null.Cap.IsZero())
	assert.True(t, null.Cap.IsNull())
	_, ok := null.Cap.Get()
	assert.False(t, ok, "null holds no value")
	assert.NotEqual(t, absent, null)

	// Encode: a buyer can send either.
	absentWire, err := json.Marshal(optionalFixture{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(absentWire))
	nullWire, err := json.Marshal(optionalFixture{Cap: Null[float64]()})
	require.NoError(t, err)
	assert.JSONEq(t, `{"cap":null}`, string(nullWire))
}

func TestOptionalZeroValueIsOmitted(t *testing.T) {
	var o Optional[float64]
	assert.True(t, o.IsZero())
	assert.False(t, o.IsNull())
	v, ok := o.Get()
	assert.False(t, ok)
	assert.Zero(t, v)

	out, err := json.Marshal(optionalFixture{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(out))
}

func TestOptionalExplicitZeroValuesAreValues(t *testing.T) {
	out, err := json.Marshal(optionalFixture{
		Cap:  Some(0.0),
		Name: Some(""),
		Flag: Some(false),
		Tags: Some([]string{}),
		Meta: Some(map[string]int{}),
		Obj:  Some(optionalInner{}),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"cap":0,"name":"","flag":false,"tags":[],"meta":{},"obj":{"a":""}}`, string(out))

	assert.False(t, Some(0.0).IsZero())
	assert.False(t, Some(0.0).IsNull())
}

func TestOptionalSomeIntInfersInt(t *testing.T) {
	assert.Equal(t, "adcp.Optional[float64]", fmt.Sprintf("%T", Some(250.0)))
	assert.Equal(t, "adcp.Optional[int]", fmt.Sprintf("%T", Some(250)), "Some(250) is Optional[int]; write Some(250.0) for a number field")
}

func TestOptionalInSlicesAndMaps(t *testing.T) {
	type containers struct {
		List []Optional[float64]          `json:"list"`
		Map  map[string]Optional[float64] `json:"map"`
	}
	var c containers
	require.NoError(t, json.Unmarshal([]byte(`{"list":[1,null,0],"map":{"a":1,"b":null,"c":0}}`), &c))
	require.Len(t, c.List, 3)
	assert.Equal(t, []string{"value:1", "null", "value:0"},
		[]string{optionalStateOf(c.List[0]), optionalStateOf(c.List[1]), optionalStateOf(c.List[2])})
	assert.Equal(t, "value:1", optionalStateOf(c.Map["a"]))
	assert.Equal(t, "null", optionalStateOf(c.Map["b"]))
	assert.Equal(t, "value:0", optionalStateOf(c.Map["c"]))

	out, err := json.Marshal(c)
	require.NoError(t, err)
	assert.JSONEq(t, `{"list":[1,null,0],"map":{"a":1,"b":null,"c":0}}`, string(out))

	// An element has no omitzero to drop it, so an omitted one is an error.
	_, err = json.Marshal(containers{List: []Optional[float64]{Some(1.0), {}}})
	assert.ErrorIs(t, err, ErrOptionalUnset)
	_, err = json.Marshal(containers{Map: map[string]Optional[float64]{"k": {}}})
	assert.ErrorIs(t, err, ErrOptionalUnset)
}

func TestOptionalMarshalFailsLoudlyOnAnUnsetValue(t *testing.T) {
	// A field without omitzero: the zero value is encoded, and refused.
	type noTag struct {
		Cap Optional[float64] `json:"cap"`
	}
	_, err := json.Marshal(noTag{})
	assert.ErrorIs(t, err, ErrOptionalUnset)

	// omitempty does not drop a struct, so it refuses as well.
	type emptyTag struct {
		Cap Optional[float64] `json:"cap,omitempty"`
	}
	_, err = json.Marshal(emptyTag{})
	assert.ErrorIs(t, err, ErrOptionalUnset)

	_, err = json.Marshal(Optional[string]{})
	assert.ErrorIs(t, err, ErrOptionalUnset)
}

func TestOptionalMarshalRefusesSomeOfNil(t *testing.T) {
	var nilPtr *float64
	zero := 0.0
	for name, v := range map[string]any{
		"nil slice":       optionalFixture{Tags: Some[[]string](nil)},
		"nil map":         optionalFixture{Meta: Some[map[string]int](nil)},
		"nil pointer":     struct{ P Optional[*float64] }{Some(nilPtr)},
		"nil interface":   struct{ A Optional[any] }{Some[any](nil)},
		"nil raw message": struct{ R Optional[json.RawMessage] }{Some(json.RawMessage(nil))},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := json.Marshal(v)
			assert.ErrorIs(t, err, ErrOptionalNilValue)
		})
	}

	// The values that look close are fine.
	out, err := json.Marshal(struct {
		P Optional[*float64] `json:"p"`
		A Optional[any]      `json:"a"`
	}{Some(&zero), Some[any](0)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"p":0,"a":0}`, string(out))
}

func TestOptionalUnmarshalReportsTypeMismatch(t *testing.T) {
	var f optionalFixture
	assert.Error(t, json.Unmarshal([]byte(`{"cap":"x"}`), &f))
	assert.Error(t, json.Unmarshal([]byte(`{"tags":{"a":1}}`), &f))
}

func TestOptionalDecodeReplacesTheHeldValueInsteadOfMerging(t *testing.T) {
	f := optionalFixture{Obj: Some(optionalInner{A: "x"}), Meta: Some(map[string]int{"keep": 1})}
	require.NoError(t, json.Unmarshal([]byte(`{"obj":{},"meta":{"new":2}}`), &f))
	obj, ok := f.Obj.Get()
	assert.True(t, ok)
	assert.Equal(t, optionalInner{}, obj)
	meta, _ := f.Meta.Get()
	assert.Equal(t, map[string]int{"new": 2}, meta)
}

func TestOptionalDecodeReplacesAnEarlierState(t *testing.T) {
	f := optionalFixture{Cap: Some(5.0)}
	require.NoError(t, json.Unmarshal([]byte(`{"cap":null}`), &f))
	assert.True(t, f.Cap.IsNull())
	require.NoError(t, json.Unmarshal([]byte(`{"cap":7}`), &f))
	v, ok := f.Cap.Get()
	assert.True(t, ok)
	assert.Equal(t, 7.0, v)
}

// A pointer to an Optional gives back the trap Optional exists to remove:
// encoding/json sets the pointer to nil for null without calling UnmarshalJSON.
// The godoc says not to use it; this pins the reason.
func TestPointerToOptionalLosesNullOnDecode(t *testing.T) {
	var p struct {
		Cap *Optional[float64] `json:"cap,omitzero"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"cap":null}`), &p))
	assert.Nil(t, p.Cap)
}

// ---- AddTool: Optional[T] is advertised exactly as *T ----

type optionalSchemaOptional struct {
	Cap   Optional[float64]             `json:"cap,omitzero"`
	Count Optional[int]                 `json:"count,omitzero"`
	Name  Optional[string]              `json:"name,omitzero"`
	Flag  Optional[bool]                `json:"flag,omitzero"`
	Tags  Optional[[]string]            `json:"tags,omitzero"`
	Meta  Optional[map[string]int]      `json:"meta,omitzero"`
	Obj   Optional[optionalSchemaObj]   `json:"obj,omitzero"`
	Many  Optional[[]optionalSchemaObj] `json:"many,omitzero"`
	Req   string                        `json:"req"`
}

// The Optional inside has a T that no other field of the request type uses,
// so it is advertised only if the walker reaches it through the outer Optional.
type optionalSchemaObj struct {
	Inner Optional[uint16] `json:"inner,omitzero"`
	Label string           `json:"label"`
}

type optionalSchemaPointer struct {
	Cap   *float64                    `json:"cap,omitempty"`
	Count *int                        `json:"count,omitempty"`
	Name  *string                     `json:"name,omitempty"`
	Flag  *bool                       `json:"flag,omitempty"`
	Tags  *[]string                   `json:"tags,omitempty"`
	Meta  *map[string]int             `json:"meta,omitempty"`
	Obj   *optionalSchemaPointerObj   `json:"obj,omitempty"`
	Many  *[]optionalSchemaPointerObj `json:"many,omitempty"`
	Req   string                      `json:"req"`
}

type optionalSchemaPointerObj struct {
	Inner *uint16 `json:"inner,omitempty"`
	Label string  `json:"label"`
}

func schemaJSON(t *testing.T, s *jsonschema.Schema) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

func TestPermissiveSchemaForAdvertisesOptionalAsPointer(t *testing.T) {
	got := permissiveSchemaFor[optionalSchemaOptional]()
	want := permissiveSchemaFor[optionalSchemaPointer]()
	assert.Equal(t, schemaJSON(t, want), schemaJSON(t, got))

	// The comparison would pass vacuously if the fallback had fired for both.
	require.Contains(t, got.Properties, "cap")
	assert.Equal(t, []string{"null", "number"}, got.Properties["cap"].Types)
	assert.Equal(t, []string{"req"}, got.Required, "an Optional field is not required")
	require.Contains(t, got.Properties, "obj")
	assert.Contains(t, schemaJSON(t, got.Properties["obj"]), `"inner":{"type":["null","integer"]`, "nested Optional reached inside an Optional struct")
}

// Each container holds an Optional whose T no other field in the type uses, so
// the Optional is advertised only if the walker looks inside that container.
func TestPermissiveSchemaForOptionalInsideContainers(t *testing.T) {
	type (
		sliceElem struct {
			X Optional[int8] `json:"x,omitzero"`
		}
		mapElem struct {
			X Optional[int16] `json:"x,omitzero"`
		}
		arrayElem struct {
			X Optional[int32] `json:"x,omitzero"`
		}
		pointerElem struct {
			X Optional[uint8] `json:"x,omitzero"`
		}

		sliceElemPointer struct {
			X *int8 `json:"x,omitempty"`
		}
		mapElemPointer struct {
			X *int16 `json:"x,omitempty"`
		}
		arrayElemPointer struct {
			X *int32 `json:"x,omitempty"`
		}
		pointerElemPointer struct {
			X *uint8 `json:"x,omitempty"`
		}
	)
	check := func(t *testing.T, got, want *jsonschema.Schema) {
		t.Helper()
		assert.Equal(t, schemaJSON(t, want), schemaJSON(t, got))
		assert.NotEqual(t, fallbackSchemaJSON(t), schemaJSON(t, got))
		assert.Contains(t, schemaJSON(t, got), `"x":{"type":["null","integer"]`)
	}

	t.Run("slice", func(t *testing.T) {
		type got struct {
			L []sliceElem `json:"l"`
		}
		type want struct {
			L []sliceElemPointer `json:"l"`
		}
		check(t, permissiveSchemaFor[got](), permissiveSchemaFor[want]())
	})
	t.Run("map", func(t *testing.T) {
		type got struct {
			M map[string]mapElem `json:"m"`
		}
		type want struct {
			M map[string]mapElemPointer `json:"m"`
		}
		check(t, permissiveSchemaFor[got](), permissiveSchemaFor[want]())
	})
	t.Run("array", func(t *testing.T) {
		type got struct {
			A [2]arrayElem `json:"a"`
		}
		type want struct {
			A [2]arrayElemPointer `json:"a"`
		}
		check(t, permissiveSchemaFor[got](), permissiveSchemaFor[want]())
	})
	t.Run("pointer", func(t *testing.T) {
		type got struct {
			P *pointerElem `json:"p,omitempty"`
		}
		type want struct {
			P *pointerElemPointer `json:"p,omitempty"`
		}
		check(t, permissiveSchemaFor[got](), permissiveSchemaFor[want]())
	})
	t.Run("the request type itself", func(t *testing.T) {
		check(t, permissiveSchemaFor[pointerElem](), permissiveSchemaFor[pointerElemPointer]())
	})
}

func listedInputSchema[In any](t *testing.T) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "optional-test", Version: "v0.0.1"}, nil)
	AddTool(server, "probe", "probe", func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	cs := connectInMemory(t, server)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Tools, 1)
	b, err := json.Marshal(res.Tools[0].InputSchema)
	require.NoError(t, err)
	return string(b)
}

func TestToolsListAdvertisesOptionalExactlyAsPointer(t *testing.T) {
	got := listedInputSchema[optionalSchemaOptional](t)
	want := listedInputSchema[optionalSchemaPointer](t)
	assert.Equal(t, want, got)
	assert.Contains(t, got, `"cap":{"type":["null","number"]}`)
}

func TestOptionalTypeSchemasLeavesTypesWithoutOptionalAlone(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeFor[testInput](),
		reflect.TypeFor[EmptyInput](),
		reflect.TypeFor[UpdateMediaBuyRequest](),
		reflect.TypeFor[CreateMediaBuyRequest](),
		reflect.TypeFor[Plan](),
	} {
		schemas, err := optionalTypeSchemas(rt)
		require.NoError(t, err, rt.String())
		assert.Empty(t, schemas, rt.String())
	}
}

// ---- AddTool: errors and recursion fall through to the existing fallback ----

func fallbackSchemaJSON(t *testing.T) string {
	t.Helper()
	return schemaJSON(t, &jsonschema.Schema{Type: "object"})
}

func TestPermissiveSchemaForOptionalOfUnsupportedTypeFallsBack(t *testing.T) {
	type withChan struct {
		Name string             `json:"name"`
		C    Optional[chan int] `json:"c,omitzero"`
	}
	type withChanPointer struct {
		Name string    `json:"name"`
		C    *chan int `json:"c,omitempty"`
	}

	// ForType rejects a chan, and the walker must not hide that by dropping
	// the Optional's schema: the whole tool takes the fallback, exactly as the
	// same field written as a pointer does.
	_, err := optionalTypeSchemas(reflect.TypeFor[withChan]())
	require.Error(t, err)

	assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[withChan]()))
	assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[withChanPointer]()))
}

func TestPermissiveSchemaForOptionalInEmbeddedAndSkippedFields(t *testing.T) {
	type base struct {
		Cap Optional[float64] `json:"cap,omitzero"`
	}
	type basePointer struct {
		Cap *float64 `json:"cap,omitempty"`
	}
	type embedded struct {
		base
		Name string `json:"name"`
	}
	type embeddedPointer struct {
		basePointer
		Name string `json:"name"`
	}
	// An embedded struct's Optional fields are promoted, and advertised.
	assert.Equal(t,
		schemaJSON(t, permissiveSchemaFor[embeddedPointer]()),
		schemaJSON(t, permissiveSchemaFor[embedded]()))
	assert.Contains(t, permissiveSchemaFor[embedded]().Properties, "cap")

	// encoding/json ignores these fields, so an Optional the walker cannot
	// describe there does not send the whole tool to the fallback.
	type skipped struct {
		Name   string             `json:"name"`
		Hidden Optional[chan int] `json:"-"`
		hidden Optional[chan int]
	}
	_ = skipped{}.hidden
	s := permissiveSchemaFor[skipped]()
	assert.NotEqual(t, fallbackSchemaJSON(t), schemaJSON(t, s))
	assert.Contains(t, s.Properties, "name")
}

type optionalNode struct {
	Label string                  `json:"label"`
	Next  Optional[*optionalNode] `json:"next,omitzero"`
}

type plainNode struct {
	Cap  Optional[float64] `json:"cap,omitzero"`
	Next *plainNode        `json:"next,omitempty"`
}

func TestPermissiveSchemaForRecursiveTypes(t *testing.T) {
	t.Run("a type that contains itself through an Optional falls back", func(t *testing.T) {
		_, err := optionalTypeSchemas(reflect.TypeFor[optionalNode]())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "itself")
		assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[optionalNode]()))
	})

	t.Run("a cycle reached through a slice of Optional falls back", func(t *testing.T) {
		type tree struct {
			Kids []Optional[*tree] `json:"kids,omitempty"`
		}
		_, err := optionalTypeSchemas(reflect.TypeFor[tree]())
		require.Error(t, err)
		assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[tree]()))
	})

	t.Run("a plain cycle falls back as it does without Optional, and the walk ends", func(t *testing.T) {
		_, err := optionalTypeSchemas(reflect.TypeFor[plainNode]())
		require.NoError(t, err, "ForType, not the walker, rejects a cycle that passes through no Optional")
		_, err = jsonschema.ForType(reflect.TypeFor[plainNode](), &jsonschema.ForOptions{})
		require.Error(t, err, "ForType rejects the cycle by itself")
		assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[plainNode]()))
	})

	t.Run("the same Optional used twice, and an Optional inside an Optional, are not cycles", func(t *testing.T) {
		type twice struct {
			A Optional[optionalSchemaObj] `json:"a,omitzero"`
			B Optional[optionalSchemaObj] `json:"b,omitzero"`
			C []optionalSchemaObj         `json:"c,omitempty"`
		}
		type twicePointer struct {
			A *optionalSchemaPointerObj  `json:"a,omitempty"`
			B *optionalSchemaPointerObj  `json:"b,omitempty"`
			C []optionalSchemaPointerObj `json:"c,omitempty"`
		}
		require.NotEqual(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[twice]()))
		assert.Equal(t, schemaJSON(t, permissiveSchemaFor[twicePointer]()), schemaJSON(t, permissiveSchemaFor[twice]()))

		type deep struct {
			D Optional[Optional[Optional[float64]]] `json:"d,omitzero"`
		}
		s := permissiveSchemaFor[deep]()
		require.Contains(t, s.Properties, "d")
		assert.Equal(t, []string{"null", "number"}, s.Properties["d"].Types)
	})
}

// ---- A call through MCP reaches the handler with the right state ----

type optionalToolRequest struct {
	DailyBudgetCap Optional[float64] `json:"daily_budget_cap,omitzero"`
	Timezone       Optional[string]  `json:"budget_cap_timezone,omitzero"`
}

func TestAddToolDeliversNullAbsentAndZeroToTheHandler(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "optional-test", Version: "v0.0.1"}, nil)
	var got optionalToolRequest
	AddTool(server, "update", "update", func(_ context.Context, _ *mcp.CallToolRequest, in optionalToolRequest) (*mcp.CallToolResult, any, error) {
		got = in
		return &mcp.CallToolResult{}, nil, nil
	})
	cs := connectInMemory(t, server)

	call := func(args map[string]any) optionalToolRequest {
		t.Helper()
		got = optionalToolRequest{}
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "update", Arguments: args})
		require.NoError(t, err)
		require.False(t, res.IsError)
		return got
	}

	absent := call(map[string]any{})
	assert.True(t, absent.DailyBudgetCap.IsZero())

	null := call(map[string]any{"daily_budget_cap": nil, "budget_cap_timezone": "Europe/Berlin"})
	assert.True(t, null.DailyBudgetCap.IsNull())
	tz, ok := null.Timezone.Get()
	assert.True(t, ok)
	assert.Equal(t, "Europe/Berlin", tz)

	zero := call(map[string]any{"daily_budget_cap": 0})
	v, ok := zero.DailyBudgetCap.Get()
	assert.True(t, ok)
	assert.Equal(t, 0.0, v)
	assert.True(t, zero.Timezone.IsZero())
}

// ---- The tag check a later change uses on the types it adds ----

// optionalTagProblems lists the Optional struct fields reachable from rt
// (through pointers, slices, maps and the T of an Optional) whose json tag is
// not `omitzero`, or that carry `omitempty`. encoding/json/v2 redefines
// omitempty to drop null, so a field tagged with it loses the null state.
func optionalTagProblems(rt reflect.Type) []string {
	var problems []string
	seen := map[reflect.Type]bool{}

	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		t = unwrapElem(t)
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		if isOptionalType(t) {
			walk(optionalElemType(t))
			return
		}
		for _, f := range reflect.VisibleFields(t) {
			if f.Anonymous || jsonFieldSkipped(f) {
				continue
			}
			if f.Type.Kind() == reflect.Pointer && isOptionalType(f.Type.Elem()) {
				problems = append(problems, fmt.Sprintf("%v.%s: *Optional loses null on decode; use Optional", t, f.Name))
			}
			if isOptionalType(f.Type) {
				var hasZero, hasEmpty bool
				for _, o := range strings.Split(f.Tag.Get("json"), ",")[1:] {
					hasZero = hasZero || o == "omitzero"
					hasEmpty = hasEmpty || o == "omitempty"
				}
				switch {
				case hasEmpty:
					problems = append(problems, fmt.Sprintf("%v.%s: tagged omitempty; use `omitzero` only", t, f.Name))
				case !hasZero:
					problems = append(problems, fmt.Sprintf("%v.%s: not tagged omitzero", t, f.Name))
				}
			}
			walk(f.Type)
		}
	}
	walk(rt)
	return problems
}

type tagsGood struct {
	A Optional[float64]         `json:"a,omitzero"`
	B Optional[string]          `json:"b,omitzero"`
	C string                    `json:"c,omitempty"`
	D *tagsGoodChild            `json:"d,omitzero"`
	E []tagsGoodChild           `json:"e,omitempty"`
	F map[string]*tagsGoodChild `json:"f,omitempty"`
	G Optional[float64]         `json:"-"`
	h Optional[float64]         //nolint:unused // unexported: encoding/json skips it
}

type tagsGoodChild struct {
	X Optional[int] `json:"x,omitzero"`
}

type tagsBad struct {
	Empty    Optional[float64] `json:"empty,omitempty"`
	Both     Optional[float64] `json:"both,omitempty,omitzero"`
	NoOption Optional[float64] `json:"no_option"`
	NoTag    Optional[float64]
	Child    []tagsBadChild                  `json:"child,omitempty"`
	InOpt    Optional[tagsBadChild]          `json:"in_opt,omitzero"`
	Ptr      *Optional[float64]              `json:"ptr,omitzero"`
	InOnly   Optional[tagsBadOnlyInOptional] `json:"in_only,omitzero"`
	tagsBadEmbedded
}

// reached only through the T of an Optional
type tagsBadOnlyInOptional struct {
	Z Optional[int8] `json:"z,omitempty"`
}

// reached only as a promoted field of an embedded struct
type tagsBadEmbedded struct {
	W Optional[int16] `json:"w,omitempty"`
}

type tagsBadChild struct {
	Y Optional[int] `json:"y,omitempty"`
}

func TestOptionalTagProblems(t *testing.T) {
	assert.Empty(t, optionalTagProblems(reflect.TypeFor[tagsGood]()))
	assert.Empty(t, optionalTagProblems(reflect.TypeFor[optionalFixture]()))
	assert.Empty(t, optionalTagProblems(reflect.TypeFor[optionalSchemaOptional]()))

	problems := strings.Join(optionalTagProblems(reflect.TypeFor[tagsBad]()), "\n")
	for _, field := range []string{"tagsBad.Empty", "tagsBad.Both", "tagsBad.NoOption", "tagsBad.NoTag", "tagsBad.Ptr", "tagsBadChild.Y", "tagsBadOnlyInOptional.Z", "tagsBad.W"} {
		assert.Contains(t, problems, field)
	}
	assert.Len(t, optionalTagProblems(reflect.TypeFor[tagsBad]()), 8, "InOpt is tagged omitzero and is not a problem")
}

// requireOptionalOmitZero fails t for every Optional field reachable from v
// (a struct, or a pointer, slice or map of one) that is not tagged omitzero,
// or that is tagged omitempty. A change that adds Optional fields to a type
// calls it with a value of that type.
func requireOptionalOmitZero(t *testing.T, v any) {
	t.Helper()
	require.NotNil(t, v)
	checkOptionalOmitZero(t, v)
}

type optionalReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

func checkOptionalOmitZero(t optionalReporter, v any) {
	t.Helper()
	rt := reflect.TypeOf(v)
	if _, err := optionalTypeSchemas(rt); errors.Is(err, errOptionalCycle) {
		t.Errorf("%v: %v (decoding is quadratic and encoding a cyclic value overflows the stack)", rt, err)
	}
	for _, problem := range optionalTagProblems(rt) {
		t.Errorf("%s", problem)
	}
}

type fakeT struct{ errors []string }

func (f *fakeT) Helper() {}

func (f *fakeT) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func TestRequireOptionalOmitZeroAcceptsTheFixtures(t *testing.T) {
	requireOptionalOmitZero(t, optionalFixture{})
	requireOptionalOmitZero(t, &optionalToolRequest{})
	requireOptionalOmitZero(t, []optionalSchemaOptional{})
}

// ---- Review cases: self-referential containers, marshalers, escaping ----

type selfSlice []selfSlice

type selfMap map[string]selfMap

func TestPermissiveSchemaForSelfReferentialContainersTerminates(t *testing.T) {
	type withSlice struct {
		Kids selfSlice         `json:"kids,omitempty"`
		Cap  Optional[float64] `json:"cap,omitzero"`
	}
	type withMap struct {
		Kids selfMap `json:"kids,omitempty"`
	}
	// ForType rejects a type that contains itself, so the tool takes the
	// fallback, as it does on main. The walk must end rather than loop.
	assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[withSlice]()))
	assert.Equal(t, fallbackSchemaJSON(t), schemaJSON(t, permissiveSchemaFor[withMap]()))
	assert.Empty(t, optionalTagProblems(reflect.TypeFor[withSlice]()))
}

type pointerMarshaler struct{ X int }

func (p *pointerMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"ptr"`), nil }

func TestOptionalEncodesLikeAPointerField(t *testing.T) {
	// A pointer-receiver MarshalJSON on T is used, as for a *T field.
	out, err := json.Marshal(struct {
		O Optional[pointerMarshaler] `json:"o,omitzero"`
		P *pointerMarshaler          `json:"p,omitempty"`
	}{Some(pointerMarshaler{1}), &pointerMarshaler{1}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"o":"ptr","p":"ptr"}`, string(out))

	// The outer encoder's HTML escaping applies to an Optional as to a *T.
	type wire struct {
		O Optional[string] `json:"o,omitzero"`
	}
	type wirePointer struct {
		O *string `json:"o,omitempty"`
	}
	s := "a<b&c"
	for _, escape := range []bool{true, false} {
		encode := func(v any) string {
			var buf strings.Builder
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(escape)
			require.NoError(t, enc.Encode(v))
			return buf.String()
		}
		assert.Equal(t, encode(wirePointer{&s}), encode(wire{Some(s)}), "SetEscapeHTML(%v)", escape)
	}
}

func TestOptionalUnmarshalJSONTrimsWhitespaceAroundNull(t *testing.T) {
	var o Optional[float64]
	require.NoError(t, o.UnmarshalJSON([]byte(" null\n")))
	assert.True(t, o.IsNull())
}

func TestOptionalTypeIsNotAStructThatEmbedsOne(t *testing.T) {
	type embeds struct {
		Optional[int]
		Name string `json:"name"`
	}
	assert.False(t, isOptionalType(reflect.TypeFor[embeds]()))
	assert.True(t, isOptionalType(reflect.TypeFor[Optional[embeds]]()))
}

// Each level doubles the number of paths to the Optional at the bottom, so a
// walk without a memo of finished types visits 2^24 nodes. The fields are
// pointers: held by value, the type would have 2^24 fields and Go 1.27's
// compiler runs out of memory on it.
type diamond[T any] struct{ A, B *T }

type (
	dia1  = diamond[Optional[uint32]]
	dia2  = diamond[dia1]
	dia3  = diamond[dia2]
	dia4  = diamond[dia3]
	dia5  = diamond[dia4]
	dia6  = diamond[dia5]
	dia7  = diamond[dia6]
	dia8  = diamond[dia7]
	dia9  = diamond[dia8]
	dia10 = diamond[dia9]
	dia11 = diamond[dia10]
	dia12 = diamond[dia11]
	dia13 = diamond[dia12]
	dia14 = diamond[dia13]
	dia15 = diamond[dia14]
	dia16 = diamond[dia15]
	dia17 = diamond[dia16]
	dia18 = diamond[dia17]
	dia19 = diamond[dia18]
	dia20 = diamond[dia19]
	dia21 = diamond[dia20]
	dia22 = diamond[dia21]
	dia23 = diamond[dia22]
	dia24 = diamond[dia23]
)

func TestOptionalWalkVisitsEachTypeOnce(t *testing.T) {
	finished := make(chan error, 1)
	go func() {
		schemas, err := optionalTypeSchemas(reflect.TypeFor[dia24]())
		if err == nil && len(schemas) != 1 {
			err = fmt.Errorf("found %d Optional types, want 1", len(schemas))
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the walk is not linear in the number of distinct types")
	}
}

func TestRequireOptionalOmitZeroReportsWhatItShould(t *testing.T) {
	ft := &fakeT{}
	checkOptionalOmitZero(ft, optionalNode{})
	require.Len(t, ft.errors, 1)
	assert.Contains(t, ft.errors[0], "itself")

	ft = &fakeT{}
	checkOptionalOmitZero(ft, tagsBad{})
	assert.Len(t, ft.errors, 8)

	ft = &fakeT{}
	checkOptionalOmitZero(ft, optionalFixture{})
	assert.Empty(t, ft.errors)
}
