package strictjson

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		ok   bool
	}{
		{"object", `{"a":1,"b":[1,2,{"c":true}]}`, true},
		{"array", `[{"a":1},{"a":2}]`, true},
		{"same key in sibling objects", `{"x":{"a":1},"y":{"a":2}}`, true},
		{"scalar", `"s"`, true},
		{"duplicate top-level key", `{"a":1,"a":2}`, false},
		{"duplicate nested key", `{"x":{"a":1,"a":1}}`, false},
		{"duplicate after unescape", `{"a":1,"a":2}`, false},
		{"duplicate in array element", `[{"k":1,"k":2}]`, false},
		{"case-folded duplicate", `{"name":"a","Name":"b"}`, false},
		{"nested case-folded duplicate", `{"params":{"name":"a","NAME":"b"}}`, false},
		{"Kelvin sign folds to k", "{\"k\":1,\"\u212a\":2}", false},
		{"long s folds to s", "{\"s\":1,\"\u017f\":2}", false},
		{"distinct keys", `{"name":"a","names":"b"}`, true},
		{"trailing value", `{"a":1} {"b":2}`, false},
		{"not json", `{a:1}`, false},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate([]byte(tc.doc), 0)
			if (err == nil) != tc.ok {
				t.Fatalf("Validate(%q) error = %v, want ok=%v", tc.doc, err, tc.ok)
			}
		})
	}
}

func TestValidateDepth(t *testing.T) {
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	if err := Validate([]byte(deep(32)), 32); err != nil {
		t.Fatalf("depth 32: %v", err)
	}
	if err := Validate([]byte(deep(33)), 32); err == nil {
		t.Fatal("depth 33 accepted")
	}
	obj := `{"a":` + deep(31) + `}`
	if err := Validate([]byte(obj), 32); err != nil {
		t.Fatalf("object depth 32: %v", err)
	}
}

func TestUnmarshal(t *testing.T) {
	var v struct{ A int }
	if err := Unmarshal([]byte(`{"A":1}`), 0, &v); err != nil || v.A != 1 {
		t.Fatalf("got %v %v", v, err)
	}
	if err := Unmarshal([]byte(`{"A":1,"A":2}`), 0, &v); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestFoldMatchesEqualFold(t *testing.T) {
	pairs := [][2]string{{"name", "NAME"}, {"k", "\u212a"}, {"s", "\u017f"}, {"straße", "STRASSE"}, {"Σ", "ς"}, {"a", "b"}}
	for _, p := range pairs {
		if (Fold(p[0]) == Fold(p[1])) != strings.EqualFold(p[0], p[1]) {
			t.Errorf("Fold disagrees with EqualFold on %q %q", p[0], p[1])
		}
	}
}
