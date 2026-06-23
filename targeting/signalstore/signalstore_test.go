package signalstore

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestKeyFormat(t *testing.T) {
	got := Key("42", []KeyType{KeyTypeURLHash}, []string{"abc"})
	want := "signal:42:url_hash:abc"
	if got != want {
		t.Fatalf("Key single = %q, want %q", got, want)
	}

	got = Key("123", []KeyType{KeyTypeURLHash, KeyTypeCountry}, []string{"h1", "US"})
	want = "signal:123:url_hash,country:h1,US"
	if got != want {
		t.Fatalf("Key combo = %q, want %q", got, want)
	}
}

func TestKey_EmptyOrMismatchedReturnsEmpty(t *testing.T) {
	if got := Key("1", nil, nil); got != "" {
		t.Fatalf("Key(empty) = %q, want empty", got)
	}
	if got := Key("1", []KeyType{KeyTypeURLHash}, []string{"a", "b"}); got != "" {
		t.Fatalf("Key(mismatched) = %q, want empty", got)
	}
}

func TestCfgValidate_RejectsIdentityKeyType(t *testing.T) {
	cfg := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyType("eid")},
		SignalID:      "seg-1",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for identity key type")
	}
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe wrap, got %v", err)
	}
	if !strings.Contains(err.Error(), `"eid"`) {
		t.Fatalf("expected error to name the rejected key type, got %v", err)
	}
}

func TestCfgValidate_RejectsRawURL(t *testing.T) {
	// `url` is intentionally NOT in AllowedKeyTypes — raw URLs collide
	// with the Valkey key delimiter, so the writer must only key on
	// url_hash. This test pins that decision.
	cfg := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyType("url")},
		SignalID:      "seg-1",
	}
	if err := cfg.Validate(); err == nil || !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe for raw url, got %v", err)
	}
}

func TestCfgValidate_RejectsEmpty(t *testing.T) {
	tests := []struct {
		name string
		cfg  Cfg
	}{
		{"missing signal id", Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeURLHash}}},
		{"empty key types", Cfg{SignalOwnerID: "1", SignalID: "seg-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestProfileValidate_LocatesBadEntry(t *testing.T) {
	p := Profile{
		AnyOf: []Cfg{
			{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: "ok"},
			{SignalOwnerID: "2", KeyTypes: []KeyType{KeyType("email")}, SignalID: "bad"},
		},
	}
	err := p.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "any_of[1]") {
		t.Fatalf("expected location prefix in error, got %v", err)
	}
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe wrap, got %v", err)
	}
}

func TestExpandKeys_CartesianProduct(t *testing.T) {
	cfg := Cfg{
		SignalOwnerID: "7",
		KeyTypes:      []KeyType{KeyTypeURLHash, KeyTypeCountry},
		SignalID:      "premium",
	}
	data := LookupData{
		KeyTypeURLHash: {"h1", "h2"},
		KeyTypeCountry: {"US", "CA"},
	}
	keys, err := cfg.ExpandKeys(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"signal:7:url_hash,country:h1,US",
		"signal:7:url_hash,country:h1,CA",
		"signal:7:url_hash,country:h2,US",
		"signal:7:url_hash,country:h2,CA",
	}
	slices.Sort(keys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("ExpandKeys mismatch.\n got: %v\nwant: %v", keys, want)
	}
}

func TestExpandKeys_MissingKeyTypeYieldsNilNilNoError(t *testing.T) {
	cfg := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyTypeURLHash, KeyTypeCountry},
		SignalID:      "x",
	}
	keys, err := cfg.ExpandKeys(LookupData{KeyTypeURLHash: {"h1"}})
	if err != nil {
		t.Fatalf("missing dimension must NOT error: %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil keys for missing dimension, got %v", keys)
	}
}

func TestExpandKeys_DisallowedKeyTypeIsErrCfgUnsafe(t *testing.T) {
	cfg := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyType("eid")}, // identity
		SignalID:      "x",
	}
	_, err := cfg.ExpandKeys(LookupData{KeyType("eid"): {"abc"}})
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe, got %v", err)
	}
}

func TestExpandKeys_CapTripIsErrCfgUnsafe(t *testing.T) {
	// 2 key types × N values each: 65×65 = 4225 > maxKeysPerCfg(4096)
	vals := make([]string, 65)
	for i := range vals {
		vals[i] = fmt.Sprintf("v%d", i)
	}
	cfg := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyTypeURLHash, KeyTypeCountry},
		SignalID:      "x",
	}
	data := LookupData{KeyTypeURLHash: vals, KeyTypeCountry: vals}
	_, err := cfg.ExpandKeys(data)
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe on cap trip, got %v", err)
	}
}

func TestMatchProfile_AnyOfAndNoneOf(t *testing.T) {
	any1 := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: "sports"}
	any2 := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: "news"}
	none1 := Cfg{SignalOwnerID: "2", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: "blocked"}

	data := LookupData{KeyTypeURLHash: {"hash-a"}}

	tests := []struct {
		name    string
		profile Profile
		fetched map[string][]string
		wantOK  bool
	}{
		{
			name:    "empty profile passes",
			profile: Profile{},
			fetched: map[string][]string{},
			wantOK:  true,
		},
		{
			name:    "any_of with one match passes",
			profile: Profile{AnyOf: []Cfg{any1, any2}},
			fetched: map[string][]string{
				Key("1", []KeyType{KeyTypeURLHash}, []string{"hash-a"}): {"sports"},
			},
			wantOK: true,
		},
		{
			name:    "any_of with no match fails",
			profile: Profile{AnyOf: []Cfg{any1, any2}},
			fetched: map[string][]string{},
			wantOK:  false,
		},
		{
			name:    "none_of match rejects even when any_of passes",
			profile: Profile{AnyOf: []Cfg{any1}, NoneOf: []Cfg{none1}},
			fetched: map[string][]string{
				Key("1", []KeyType{KeyTypeURLHash}, []string{"hash-a"}): {"sports"},
				Key("2", []KeyType{KeyTypeURLHash}, []string{"hash-a"}): {"blocked"},
			},
			wantOK: false,
		},
		{
			name:    "only none_of, no match passes",
			profile: Profile{NoneOf: []Cfg{none1}},
			fetched: map[string][]string{},
			wantOK:  true,
		},
		{
			name:    "only none_of, match rejects",
			profile: Profile{NoneOf: []Cfg{none1}},
			fetched: map[string][]string{
				Key("2", []KeyType{KeyTypeURLHash}, []string{"hash-a"}): {"blocked"},
			},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.profile.MatchProfile(data, tc.fetched)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantOK {
				t.Fatalf("MatchProfile = %v, want %v", got, tc.wantOK)
			}
		})
	}
}

func TestMatchProfile_NoneOfCapTripFailsClosed(t *testing.T) {
	// A blocklist cfg that trips the cap MUST propagate ErrCfgUnsafe
	// so the engine fails-closed for the whole package, even if the
	// any_of side would otherwise match. The wrong-direction failure
	// (none_of silently passing on a cap trip) was the original bug.
	vals := make([]string, 65)
	for i := range vals {
		vals[i] = fmt.Sprintf("v%d", i)
	}
	none := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyTypeURLHash, KeyTypeCountry},
		SignalID:      "brand-unsafe",
	}
	any1 := Cfg{
		SignalOwnerID: "1",
		KeyTypes:      []KeyType{KeyTypeURLHash},
		SignalID:      "sports",
	}
	data := LookupData{KeyTypeURLHash: vals, KeyTypeCountry: vals}
	profile := Profile{
		AnyOf:  []Cfg{any1},
		NoneOf: []Cfg{none}, // would trip cap
	}
	// Pretend any_of matches.
	fetched := map[string][]string{
		Key("1", []KeyType{KeyTypeURLHash}, []string{"v0"}): {"sports"},
	}
	pass, err := profile.MatchProfile(data, fetched)
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe to propagate, got pass=%v err=%v", pass, err)
	}
	if pass {
		t.Fatalf("expected fail-closed (pass=false) on none_of cap trip, got pass=true")
	}
}

func TestMatchProfile_NoneOfMalformedFailsClosed(t *testing.T) {
	// A persisted none_of cfg with an empty signal_owner_id or signal_id
	// must propagate ErrCfgUnsafe so the package fails closed. Profiles are
	// decoded from stored JSON without Validate, so the read path must catch
	// this — otherwise the blocklist expands to keys that never match and
	// passes vacuously even though the any_of side matches.
	any1 := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: "sports"}
	data := LookupData{KeyTypeURLHash: {"v0"}}
	fetched := map[string][]string{
		Key("1", []KeyType{KeyTypeURLHash}, []string{"v0"}): {"sports"},
	}
	cases := map[string]Cfg{
		"empty owner":     {SignalOwnerID: "", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: "brand-unsafe"},
		"empty signal_id": {SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeURLHash}, SignalID: ""},
	}
	for name, none := range cases {
		t.Run(name, func(t *testing.T) {
			profile := Profile{AnyOf: []Cfg{any1}, NoneOf: []Cfg{none}}
			pass, err := profile.MatchProfile(data, fetched)
			if !errors.Is(err, ErrCfgUnsafe) {
				t.Fatalf("expected ErrCfgUnsafe to propagate, got pass=%v err=%v", pass, err)
			}
			if pass {
				t.Fatalf("expected fail-closed (pass=false) on malformed none_of, got pass=true")
			}
		})
	}
}

func TestPlanLookup_DedupsAcrossPackages(t *testing.T) {
	shared := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeCountry}, SignalID: "us-traffic"}
	other := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeCountry}, SignalID: "ca-only"}
	p1 := &Profile{AnyOf: []Cfg{shared}}
	p2 := &Profile{AnyOf: []Cfg{shared, other}}
	data := LookupData{KeyTypeCountry: {"US"}}

	keys, err := PlanLookup([]*Profile{p1, p2}, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 deduped key, got %d: %v", len(keys), keys)
	}
}

func TestPlanLookup_SkipsUnsafeCfgInsteadOfAborting(t *testing.T) {
	// A bad cfg (disallowed key type) in one package must NOT abort the
	// whole plan — its keys are skipped so the safe cfg's keys still
	// get planned. Per-package fail-closed is handled at match time by
	// MatchProfile, verified at the engine level.
	bad := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyType("eid")}, SignalID: "x"}
	good := Cfg{SignalOwnerID: "2", KeyTypes: []KeyType{KeyTypeCountry}, SignalID: "us"}
	badProfile := &Profile{AnyOf: []Cfg{bad}}
	goodProfile := &Profile{AnyOf: []Cfg{good}}

	keys, err := PlanLookup([]*Profile{badProfile, goodProfile}, LookupData{KeyTypeCountry: {"US"}, KeyType("eid"): {"v"}})
	if err != nil {
		t.Fatalf("a single unsafe cfg must not abort the plan, got %v", err)
	}
	want := Key("2", []KeyType{KeyTypeCountry}, []string{"US"})
	if len(keys) != 1 || keys[0] != want {
		t.Fatalf("expected only the safe cfg's key %q, got %v", want, keys)
	}
}

func TestMatchProfile_UnsafeCfgErrorNamesOwnerAndIndex(t *testing.T) {
	// The match-time error must identify the offending cfg so the engine
	// log pinpoints it. Owner "99" at any_of[1].
	p := &Profile{AnyOf: []Cfg{
		{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeCountry}, SignalID: "ok"},
		{SignalOwnerID: "99", KeyTypes: []KeyType{KeyType("eid")}, SignalID: "bad"},
	}}
	_, err := p.MatchProfile(LookupData{KeyTypeCountry: {"US"}, KeyType("eid"): {"v"}}, map[string][]string{})
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe, got %v", err)
	}
	if !strings.Contains(err.Error(), `any_of[1]`) || !strings.Contains(err.Error(), `owner "99"`) {
		t.Fatalf("error must name the offending index and owner, got %v", err)
	}
}

func TestDecodeValues_HandlesEmptyAndCSV(t *testing.T) {
	keys := []string{"k1", "k2", "k3", "k4"}
	values := []string{"a,b,c", "", "lone", "a,,b"}
	got := DecodeValues(keys, values)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries (skip empty value), got %v", got)
	}
	if !slices.Equal(got["k1"], []string{"a", "b", "c"}) {
		t.Fatalf("k1 = %v", got["k1"])
	}
	if !slices.Equal(got["k3"], []string{"lone"}) {
		t.Fatalf("k3 = %v", got["k3"])
	}
	// Empty segments in a,,b must be dropped.
	if !slices.Equal(got["k4"], []string{"a", "b"}) {
		t.Fatalf("k4 = %v, want [a b]", got["k4"])
	}
}

func TestDecodeValues_LengthMismatchReturnsNil(t *testing.T) {
	if got := DecodeValues([]string{"k1"}, []string{"a", "b"}); got != nil {
		t.Fatalf("expected nil on length mismatch, got %v", got)
	}
}

func TestAllowedKeyTypes_StableSet(t *testing.T) {
	expected := map[KeyType]struct{}{
		KeyTypeURLHash: {},
		KeyTypeCountry: {}, KeyTypeRegion: {}, KeyTypeMetro: {},
		KeyTypeTopic: {},
		KeyTypeEIDR:  {}, KeyTypeGracenote: {}, KeyTypeISRC: {},
		KeyTypeGTIN: {}, KeyTypeRSSGUID: {}, KeyTypeISBN: {}, KeyTypeCustom: {},
	}
	got := AllowedKeyTypes()
	if len(got) != len(expected) {
		t.Fatalf("expected %d allowed key types, got %d (%v)", len(expected), len(got), got)
	}
	for _, kt := range got {
		if _, ok := expected[kt]; !ok {
			t.Fatalf("unexpected allowed key type %q", kt)
		}
	}
}

func TestMaxKeysPerCfgExposed(t *testing.T) {
	if MaxKeysPerCfg() <= 0 {
		t.Fatalf("MaxKeysPerCfg must be positive, got %d", MaxKeysPerCfg())
	}
}

func TestProfileValidate_RejectsTooManyCfgs(t *testing.T) {
	ok := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeCountry}, SignalID: "x"}
	p := &Profile{}
	for range maxCfgsPerProfile + 1 {
		p.AnyOf = append(p.AnyOf, ok)
	}
	err := p.Validate()
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe for oversized profile, got %v", err)
	}
}

func TestProfileValidate_AllowsMaxCfgs(t *testing.T) {
	ok := Cfg{SignalOwnerID: "1", KeyTypes: []KeyType{KeyTypeCountry}, SignalID: "x"}
	p := &Profile{}
	for range maxCfgsPerProfile {
		p.AnyOf = append(p.AnyOf, ok)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("profile at the cfg limit must validate, got %v", err)
	}
}

func TestPlanLookup_RequestWideCapFailsClosed(t *testing.T) {
	// Each cfg expands to ~32 keys (1 url_hash value would be 1; use a
	// value set per dimension to multiply). Spread enough distinct cfgs
	// across owners so the deduped plan crosses maxKeysPerPlan.
	data := LookupData{KeyTypeURLHash: make([]string, 64), KeyTypeCountry: make([]string, 64)}
	for i := range 64 {
		data[KeyTypeURLHash][i] = "h" + strconv.Itoa(i)
		data[KeyTypeCountry][i] = "c" + strconv.Itoa(i)
	}
	// 64*64 = 4096 keys per cfg (right at maxKeysPerCfg). Distinct owner
	// per cfg keeps keys un-deduped, so 17 cfgs > 65536 keys.
	profiles := make([]*Profile, 0, 32)
	for owner := range 32 {
		profiles = append(profiles, &Profile{AnyOf: []Cfg{{
			SignalOwnerID: strconv.Itoa(owner),
			KeyTypes:      []KeyType{KeyTypeURLHash, KeyTypeCountry},
			SignalID:      "x",
		}}})
	}
	_, err := PlanLookup(profiles, data)
	if !errors.Is(err, ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe when the request-wide plan exceeds the cap, got %v", err)
	}
}

func TestMaxKeysPerPlanExposed(t *testing.T) {
	if MaxKeysPerPlan() < MaxKeysPerCfg() {
		t.Fatalf("MaxKeysPerPlan (%d) should be >= MaxKeysPerCfg (%d)", MaxKeysPerPlan(), MaxKeysPerCfg())
	}
}
