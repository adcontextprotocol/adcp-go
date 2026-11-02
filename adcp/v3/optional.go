package adcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// optionalState is the state of an [Optional]. The zero value is omitted, so
// the zero value of an Optional field is a field that is not sent.
type optionalState uint8

const (
	optionalOmitted optionalState = iota
	optionalNull
	optionalValue
)

// Optional is a request field that has three states on the wire: omitted,
// explicit JSON null, or a value. Its zero value is omitted.
//
// A pointer cannot hold three states. Go's encoding/json sets a pointer field
// to nil for JSON null without calling UnmarshalJSON, so a seller reading a
// *float64 cannot tell {} from {"daily_budget_cap":null}, and a buyer cannot
// send null at all. Optional keeps the state in the type.
//
// Tag every Optional struct field `json:"name,omitzero"`, and never
// `omitempty`. omitzero calls IsZero and drops an omitted field; omitempty
// does not, and under encoding/json/v2 it also drops null. MarshalJSON returns
// an error for an omitted value, so a field with a missing omitzero fails when
// it is encoded instead of sending the wrong thing.
//
// Do not use *Optional[T]: decoding sets the pointer to nil for JSON null.
//
// Given a request type with a field `DailyBudgetCap Optional[float64]`, a
// seller decides what a request means: omitted preserves the stored value,
// null clears it, a value replaces it.
//
//	if req.DailyBudgetCap.IsNull() {
//		clearCap()
//	} else if v, ok := req.DailyBudgetCap.Get(); ok {
//		setCap(v)
//	}
//
// A buyer builds the same request. Write Some(250.0), because Some(250) infers
// Optional[int]:
//
//	req.DailyBudgetCap = adcp.Null[float64]() // clear
//	req.DailyBudgetCap = adcp.Some(250.0)     // replace
//
// A zero value (0, "", false) or an empty collection ([] or {}) is a value,
// not omission. Some of a nil slice, map, pointer or interface is refused when
// encoded, because it would send null, which means clear: use Null[T]() to
// clear, or Some of an empty collection to replace with an empty one.
//
// A value that encodes as null (a nil slice, map, pointer or interface, a
// json.RawMessage holding null, or a T whose MarshalJSON returns null) is
// refused the same way.
//
// The encoder's escaping and indent settings apply to an Optional as to a *T.
// Decoding uses encoding/json's defaults, whatever options the caller's own
// decoder carries (DisallowUnknownFields, UseNumber), and replaces the held
// value instead of merging into it.
//
// Do not use Optional in a type that contains itself through the Optional
// (through a pointer, slice or map). Each Optional decodes its T with a fresh
// call, so decoding such a type is quadratic in its nesting depth, and encoding
// a cyclic value overflows the stack instead of returning an error. AddTool
// gives such a type the permissive fallback schema.
type Optional[T any] struct {
	v     T
	state optionalState
}

// Some returns an Optional holding v: the field is sent with that value.
func Some[T any](v T) Optional[T] {
	return Optional[T]{v: v, state: optionalValue}
}

// Null returns an Optional that encodes as JSON null: the field is sent as
// null, which asks the seller to clear it.
func Null[T any]() Optional[T] {
	return Optional[T]{state: optionalNull}
}

// IsZero reports whether the field is omitted. It is what `omitzero` calls.
// An explicit null is not zero.
func (o Optional[T]) IsZero() bool {
	return o.state == optionalOmitted
}

// IsNull reports whether the field is an explicit JSON null.
func (o Optional[T]) IsNull() bool {
	return o.state == optionalNull
}

// Get returns the value, and true only when the field holds one. It is false
// for an omitted field and for null.
func (o Optional[T]) Get() (T, bool) {
	return o.v, o.state == optionalValue
}

// ErrOptionalUnset is returned when an omitted Optional is encoded. A struct
// field of type Optional must be tagged `omitzero`.
var ErrOptionalUnset = errors.New("adcp.Optional: omitted value encoded; tag the field `json:\"name,omitzero\"`")

// ErrOptionalNilValue is returned when Some holds a value that encodes as null
// (a nil slice, map, pointer or interface), which would mean clear.
var ErrOptionalNilValue = errors.New("adcp.Optional: a value that encodes as null (Some of a nil slice, map, pointer or interface) means clear; use Null[T]() to clear or Some of an empty value to replace")

// MarshalJSON encodes null, or the value. It returns [ErrOptionalUnset] for an
// omitted value and [ErrOptionalNilValue] for a value that encodes as null.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	switch o.state {
	case optionalNull:
		return []byte("null"), nil
	case optionalValue:
		// Marshal &o.v so that a pointer-receiver MarshalJSON on T is used,
		// as it is for a *T field. HTML escaping is left to the outer
		// encoder, which applies its own setting to what we return.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(&o.v); err != nil {
			return nil, err
		}
		b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		if bytes.Equal(b, []byte("null")) {
			return nil, ErrOptionalNilValue
		}
		return b, nil
	}
	return nil, ErrOptionalUnset
}

// UnmarshalJSON decodes null to the null state and anything else to a value.
// A field that is absent from the JSON is never passed here and stays omitted.
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*o = Optional[T]{state: optionalNull}
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*o = Optional[T]{v: v, state: optionalValue}
	return nil
}

// optionalElem lets reflection recognise an Optional[T] and find its T.
func (Optional[T]) optionalElem() reflect.Type {
	return reflect.TypeFor[T]()
}

type optionalMarker interface {
	optionalElem() reflect.Type
}

var (
	optionalMarkerType = reflect.TypeFor[optionalMarker]()
	optionalPkgPath    = reflect.TypeFor[Optional[int]]().PkgPath()
)

// isOptionalType reports whether t is an instantiation of Optional.
func isOptionalType(t reflect.Type) bool {
	// Implements alone also matches a struct that embeds an Optional, through
	// the promoted method.
	return t.Kind() == reflect.Struct && t.PkgPath() == optionalPkgPath &&
		strings.HasPrefix(t.Name(), "Optional[") && t.Implements(optionalMarkerType)
}

// optionalElemType returns T for t = Optional[T].
func optionalElemType(t reflect.Type) reflect.Type {
	return reflect.Zero(t).Interface().(optionalMarker).optionalElem()
}

// jsonFieldSkipped reports whether encoding/json ignores the field.
func jsonFieldSkipped(f reflect.StructField) bool {
	return !f.IsExported() || f.Tag.Get("json") == "-"
}

// errOptionalCycle is returned for a type that contains itself through an
// Optional.
var errOptionalCycle = errors.New("it contains itself through an Optional")

// unwrapElem strips pointers, slices, arrays and maps from t, down to the type
// whose fields encoding/json reaches.
func unwrapElem(t reflect.Type) reflect.Type {
	seen := map[reflect.Type]bool{}
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			if seen[t] {
				return t // a type that is its own element, such as `type T []T`; not a struct
			}
			seen[t] = true
			t = t.Elem()
		default:
			return t
		}
	}
}

// optionalTypeSchemas finds every Optional[T] reachable from rt and returns,
// for each, the schema that *T gets from jsonschema.ForType, so that AddTool
// advertises an Optional[T] field exactly as it advertises a *T field.
// ForType reads struct fields and cannot see through Optional, whose fields
// are unexported.
//
// It returns an error when an Optional[T] contains itself, or when ForType
// cannot describe a T. The caller then falls back as it does when ForType
// fails for the request type itself. A cycle that passes through no Optional
// is not an error here: ForType rejects it later, as it always has.
func optionalTypeSchemas(rt reflect.Type) (map[reflect.Type]*jsonschema.Schema, error) {
	out := map[reflect.Type]*jsonschema.Schema{}
	done := map[reflect.Type]bool{}
	var path []reflect.Type

	var walk func(t reflect.Type) error
	walk = func(t reflect.Type) error {
		t = unwrapElem(t)
		if t.Kind() != reflect.Struct || done[t] {
			return nil
		}

		for i, p := range path {
			if p != t {
				continue
			}
			for _, q := range path[i:] {
				if isOptionalType(q) {
					return fmt.Errorf("adcp: cannot describe %v: %w", t, errOptionalCycle)
				}
			}
			return nil // a plain cycle; ForType reports it
		}

		path = append(path, t)
		defer func() { path = path[:len(path)-1] }()

		if isOptionalType(t) {
			elem := optionalElemType(t)
			if err := walk(elem); err != nil {
				return err
			}
			s, err := jsonschema.ForType(reflect.PointerTo(elem), &jsonschema.ForOptions{TypeSchemas: out})
			if err != nil {
				return fmt.Errorf("adcp: cannot describe %v: %w", t, err)
			}
			out[t] = s
			done[t] = true
			return nil
		}

		for _, f := range reflect.VisibleFields(t) {
			if f.Anonymous || jsonFieldSkipped(f) {
				continue
			}
			if err := walk(f.Type); err != nil {
				return err
			}
		}
		done[t] = true
		return nil
	}

	if err := walk(rt); err != nil {
		return nil, err
	}
	return out, nil
}
