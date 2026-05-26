package tmproto

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestJCSPrimitives(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"null", nil, "null"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"int", 42, "42"},
		{"int64", int64(-1234567890123), "-1234567890123"},
		{"zero", 0, "0"},
		{"empty string", "", `""`},
		{"simple string", "hello", `"hello"`},
		{"backslash", "a\\b", `"a\\b"`},
		{"quote", `a"b`, `"a\"b"`},
		{"newline", "a\nb", `"a\nb"`},
		{"tab", "a\tb", `"a\tb"`},
		{"control 0x01", "\x01", "\"\\u0001\""},
		{"control 0x1f", "\x1f", "\"\\u001f\""},
		{"empty array", []any{}, "[]"},
		{"empty object", map[string]any{}, "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jcsMarshal(tc.in)
			if err != nil {
				t.Fatalf("jcsMarshal(%v) err = %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Errorf("jcsMarshal(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestJCSObjectKeysSorted(t *testing.T) {
	in := map[string]any{
		"z": 1,
		"a": 2,
		"m": 3,
	}
	got, err := jcsMarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":2,"m":3,"z":1}`
	if string(got) != want {
		t.Errorf("jcsMarshal keys = %q, want %q", got, want)
	}
}

func TestJCSNestedDeterministic(t *testing.T) {
	// Same logical shape, different insertion orders → identical bytes.
	a := map[string]any{
		"inner": map[string]any{"y": []any{1, 2, 3}, "x": "v"},
		"outer": []any{map[string]any{"b": 2, "a": 1}},
	}
	b := map[string]any{
		"outer": []any{map[string]any{"a": 1, "b": 2}},
		"inner": map[string]any{"x": "v", "y": []any{1, 2, 3}},
	}
	ga, err := jcsMarshal(a)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := jcsMarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ga) != string(gb) {
		t.Errorf("non-deterministic output: %q vs %q", ga, gb)
	}
	want := `{"inner":{"x":"v","y":[1,2,3]},"outer":[{"a":1,"b":2}]}`
	if string(ga) != want {
		t.Errorf("got %q, want %q", ga, want)
	}
}

func TestJCSStringEscapeLowercaseHex(t *testing.T) {
	// RFC 8785 §3.2.2.2: the hexadecimal alphabet uses lower-case letters.
	got, err := jcsMarshal("\x1f")
	if err != nil {
		t.Fatal(err)
	}
	want := "\"\\u001f\""
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestJCSArrayPreservesOrder(t *testing.T) {
	got, err := jcsMarshal([]any{"c", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := `["c","a","b"]`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestJCSJSONNumberInteger(t *testing.T) {
	got, err := jcsMarshal(json.Number("12345"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "12345" {
		t.Errorf("got %q, want 12345", got)
	}
}

func TestJCSStringSlice(t *testing.T) {
	got, err := jcsMarshal([]string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `["a","b","c"]` {
		t.Errorf("got %q", got)
	}
}

func TestJCSRejectsNonIntegerFloats(t *testing.T) {
	if _, err := jcsMarshal(1.5); err == nil {
		t.Fatal("non-integer float must be rejected until ECMA-262 number canonicalization is implemented")
	} else if strings.Contains(err.Error(), "1.5") {
		t.Fatalf("non-integer float error echoed rejected value: %q", err.Error())
	}
}

func TestJCSRejectsNonFiniteFloatsWithoutEcho(t *testing.T) {
	if _, err := jcsMarshal(math.Inf(1)); err == nil {
		t.Fatal("non-finite float must be rejected")
	} else if strings.Contains(err.Error(), "+Inf") || strings.Contains(err.Error(), "Inf") {
		t.Fatalf("non-finite float error echoed rejected value: %q", err.Error())
	}
}

func TestJCSRejectsInvalidJSONNumberWithoutEcho(t *testing.T) {
	if _, err := jcsMarshal(json.Number("leaky-number")); err == nil {
		t.Fatal("invalid json.Number must be rejected")
	} else if strings.Contains(err.Error(), "leaky-number") {
		t.Fatalf("json.Number error echoed rejected value: %q", err.Error())
	}
}

func TestJCSAcceptsIntegerFloats(t *testing.T) {
	got, err := jcsMarshal(42.0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "42" {
		t.Errorf("got %q, want 42", got)
	}
}

func TestJCSObjectKeySort(t *testing.T) {
	// JCS sorts object keys; our identity-match canonical object includes
	// "type", "request_id", "identities_hash", "consent", "package_ids",
	// "provider_endpoint_url", "daily_epoch" — verify the sort yields
	// alphabetic order on those keys.
	in := map[string]any{
		"type":                  "identity_match_request",
		"request_id":            "r1",
		"identities_hash":       "h",
		"consent":               nil,
		"package_ids":           []string{"a"},
		"provider_endpoint_url": "https://example.com",
		"daily_epoch":           int64(20000),
	}
	got, err := jcsMarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"consent":null,"daily_epoch":20000,"identities_hash":"h","package_ids":["a"],"provider_endpoint_url":"https://example.com","request_id":"r1","type":"identity_match_request"}`
	if string(got) != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
