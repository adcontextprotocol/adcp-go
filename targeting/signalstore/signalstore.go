// Package signalstore evaluates context-side signal targeting profiles
// against the signal:* keyspace populated by a signal writer.
//
// Wire shape. Each key is
//
//	signal:{signal_owner_id}:{key_type[,key_type...]}:{value[,value...]}
//
// and the value is a comma-separated list of signal IDs that the
// (owner, key_type-tuple, value-tuple) maps to. Readers and the
// writer share this contract so a single store can serve both
// identity-match (user-identity key types) and context-match
// (context-attribute key types) without overlap.
//
// Scope guard. Context-match per the TMP spec forbids user identity on
// the wire, so AllowedKeyTypes is restricted to context attributes
// (URL hashes, geo segments, IAB topic ids, artifact_ref public
// identifiers). Cfgs whose key_types fall outside that set are
// rejected at three layers — Validate, ExpandKeys, and the
// writer-side Service.Put — so a misconfigured cfg cannot reach the
// identity keyspace from the context-match data path.
package signalstore

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// KeyType labels one dimension of the lookup tuple. Stable strings:
// the writer persists these verbatim into Valkey keys.
type KeyType string

// Context-attribute key types accepted on this endpoint. Identity key
// types (eid, email, coreid, …) are intentionally absent: TMP
// context-match requests carry no user identity, so a cfg whose
// key_types reference identity is treated as a configuration error
// and rejected by Validate / ExpandKeys.
//
// KeyTypeURL is intentionally NOT in the allowed set — raw URLs
// contain `:` and `,` characters that collide with the key delimiter.
// Publishers and writers must agree on url_hash for URL-based
// targeting.
const (
	KeyTypeURLHash   KeyType = "url_hash"
	KeyTypeCountry   KeyType = "country"
	KeyTypeRegion    KeyType = "region"
	KeyTypeMetro     KeyType = "metro"
	KeyTypeTopic     KeyType = "topic"
	KeyTypeEIDR      KeyType = "eidr"
	KeyTypeGracenote KeyType = "gracenote"
	KeyTypeISRC      KeyType = "isrc"
	KeyTypeGTIN      KeyType = "gtin"
	KeyTypeRSSGUID   KeyType = "rss_guid"
	KeyTypeISBN      KeyType = "isbn"
	KeyTypeCustom    KeyType = "custom"
)

// keyPrefix is the namespace every key carries.
const keyPrefix = "signal:"

// ErrCfgUnsafe is returned by ExpandKeys / Matches / MatchProfile /
// PlanLookup when a cfg cannot be evaluated safely — it carries a
// disallowed key_type or trips the per-cfg expansion cap. Callers
// MUST fail-closed (drop the whole package) when this error surfaces;
// silently treating it as "no match" would let a none_of brand-safety
// cfg pass when it shouldn't.
var ErrCfgUnsafe = errors.New("signalstore: cfg cannot be evaluated safely")

var allowedKeyTypes = map[KeyType]struct{}{
	KeyTypeURLHash:   {},
	KeyTypeCountry:   {},
	KeyTypeRegion:    {},
	KeyTypeMetro:     {},
	KeyTypeTopic:     {},
	KeyTypeEIDR:      {},
	KeyTypeGracenote: {},
	KeyTypeISRC:      {},
	KeyTypeGTIN:      {},
	KeyTypeRSSGUID:   {},
	KeyTypeISBN:      {},
	KeyTypeCustom:    {},
}

// AllowedKeyTypes returns the sorted list of key types this package
// accepts. Useful for diagnostics and tests.
func AllowedKeyTypes() []KeyType {
	out := make([]KeyType, 0, len(allowedKeyTypes))
	for kt := range allowedKeyTypes {
		out = append(out, kt)
	}
	slices.SortFunc(out, func(a, b KeyType) int { return strings.Compare(string(a), string(b)) })
	return out
}

// IsAllowed reports whether kt is in the accepted set.
func IsAllowed(kt KeyType) bool {
	_, ok := allowedKeyTypes[kt]
	return ok
}

// Cfg is one signal-targeting entry attached to a package.
// SignalOwnerID is the public identifier of the entity that owns the
// signal definitions; the writer encodes it verbatim into the Valkey
// key prefix. Public API never uses any other name for this field.
type Cfg struct {
	SignalOwnerID uint64    `json:"signal_owner_id"`
	KeyTypes      []KeyType `json:"key_types"`
	SignalID      string    `json:"signal_id"`
}

// Validate checks the cfg is well-formed under the context-match
// restriction (at least one key_type, all in AllowedKeyTypes, signal
// id non-empty).
func (c Cfg) Validate() error {
	if c.SignalID == "" {
		return errors.New("signalstore: signal_id is required")
	}
	if len(c.KeyTypes) == 0 {
		return errors.New("signalstore: key_types must not be empty")
	}
	for _, kt := range c.KeyTypes {
		if !IsAllowed(kt) {
			return fmt.Errorf("signalstore: key_type %q is not allowed on context-match: %w", kt, ErrCfgUnsafe)
		}
	}
	return nil
}

// Profile is the union of inclusive (any_of) and exclusive (none_of)
// signal targets attached to one package's context config.
type Profile struct {
	AnyOf  []Cfg `json:"any_of,omitempty"`
	NoneOf []Cfg `json:"none_of,omitempty"`
}

// maxCfgsPerProfile bounds how many cfgs one package's profile may
// carry across any_of + none_of combined. A profile is operator/
// writer-supplied config, not request data, so the ceiling is generous;
// it exists so a single malformed config cannot drive an unbounded
// cartesian at request time.
const maxCfgsPerProfile = 256

// Validate runs Cfg.Validate over every entry so a single misconfigured
// cfg surfaces with location context, and rejects profiles that exceed
// maxCfgsPerProfile.
func (p *Profile) Validate() error {
	if p == nil {
		return nil
	}
	if n := len(p.AnyOf) + len(p.NoneOf); n > maxCfgsPerProfile {
		return fmt.Errorf("profile has %d cfgs, exceeds %d: %w", n, maxCfgsPerProfile, ErrCfgUnsafe)
	}
	for i, c := range p.AnyOf {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("any_of[%d]: %w", i, err)
		}
	}
	for i, c := range p.NoneOf {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("none_of[%d]: %w", i, err)
		}
	}
	return nil
}

// IsEmpty reports whether the profile has any gating cfgs at all.
// Used by callers that want to short-circuit before key planning.
func (p *Profile) IsEmpty() bool {
	return p == nil || (len(p.AnyOf) == 0 && len(p.NoneOf) == 0)
}

// LookupData maps each available context attribute to its values for
// one ContextMatchRequest. Built once per request by the engine; passed
// to ExpandKeys / Match.
type LookupData map[KeyType][]string

// Key composes the Valkey key for one (owner, keyTypes, values) tuple.
// keyTypes and values MUST be the same length and non-empty; callers
// that hand in mismatched or empty slices get an empty string back so
// a downstream MGet skips the malformed key instead of fetching the
// degenerate `signal:0::` shape.
func Key(ownerID uint64, keyTypes []KeyType, values []string) string {
	if len(keyTypes) == 0 || len(keyTypes) != len(values) {
		return ""
	}
	var b strings.Builder
	b.Grow(len(keyPrefix) + 24 + len(keyTypes)*8 + len(values)*16)
	b.WriteString(keyPrefix)
	b.WriteString(strconv.FormatUint(ownerID, 10))
	b.WriteByte(':')
	for i, kt := range keyTypes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(kt))
	}
	b.WriteByte(':')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(v)
	}
	return b.String()
}

// ExpandKeys returns the cartesian product of keys the cfg queries
// against given the supplied lookup data.
//
// Three return shapes:
//
//   - (nil, nil) — cfg references a key_type with no values in data,
//     so cannot possibly match. Callers SHOULD treat the cfg as "did
//     not match" and continue.
//   - (keys, nil) — normal path.
//   - (nil, ErrCfgUnsafe) — cfg carries a disallowed key_type, or
//     would expand past the per-cfg cap. Callers MUST fail-closed for
//     the whole package: silently dropping is only safe for any_of,
//     not for none_of, so the error path forces the engine to treat
//     the entire profile as unevaluable.
func (c Cfg) ExpandKeys(data LookupData) ([]string, error) {
	if len(c.KeyTypes) == 0 {
		return nil, fmt.Errorf("empty key_types: %w", ErrCfgUnsafe)
	}
	sets := make([][]string, len(c.KeyTypes))
	total := 1
	for i, kt := range c.KeyTypes {
		if !IsAllowed(kt) {
			return nil, fmt.Errorf("key_type %q is not allowed: %w", kt, ErrCfgUnsafe)
		}
		vals := data[kt]
		if len(vals) == 0 {
			return nil, nil
		}
		sets[i] = vals
		total *= len(vals)
		if total > maxKeysPerCfg {
			return nil, fmt.Errorf("expansion exceeds %d keys: %w", maxKeysPerCfg, ErrCfgUnsafe)
		}
	}
	combo := make([]string, len(c.KeyTypes))
	keys := make([]string, 0, total)
	indices := make([]int, len(sets))
	for {
		for i := range indices {
			combo[i] = sets[i][indices[i]]
		}
		keys = append(keys, Key(c.SignalOwnerID, c.KeyTypes, combo))
		pos := len(indices) - 1
		for pos >= 0 {
			indices[pos]++
			if indices[pos] < len(sets[pos]) {
				break
			}
			indices[pos] = 0
			pos--
		}
		if pos < 0 {
			break
		}
	}
	return keys, nil
}

// maxKeysPerCfg caps how many candidate keys one cfg can expand into.
// Cfgs whose lookup data multiplies past the cap return ErrCfgUnsafe
// from ExpandKeys; the engine then fails-closed for the package.
const maxKeysPerCfg = 4096

// MaxKeysPerCfg returns the per-cfg expansion limit. Exported so the
// engine can include the cap in diagnostic logs without duplicating
// the constant.
func MaxKeysPerCfg() int { return maxKeysPerCfg }

// Matches reports whether cfg's SignalID appears in the CSV-decoded
// signal-id lists fetched for any of its expanded keys. Returns an
// error wrapping ErrCfgUnsafe when ExpandKeys does — callers MUST
// fail-closed for the whole package in that case.
func (c Cfg) Matches(data LookupData, fetched map[string][]string) (bool, error) {
	keys, err := c.ExpandKeys(data)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		ids, ok := fetched[k]
		if !ok {
			continue
		}
		if slices.Contains(ids, c.SignalID) {
			return true, nil
		}
	}
	return false, nil
}

// MatchProfile applies any_of / none_of semantics:
//
//   - empty profile: passes (vacuous).
//   - non-empty AnyOf: passes iff at least one any_of cfg matches.
//   - non-empty NoneOf: rejects iff any none_of cfg matches.
//
// An ErrCfgUnsafe from any cfg propagates and forces a (false, err)
// return so the caller fails-closed. None_of cfgs are evaluated
// before any_of cfgs so a cap trip on a blocklist surfaces the
// rejection even when the buyer's any_of would otherwise pass.
func (p *Profile) MatchProfile(data LookupData, fetched map[string][]string) (bool, error) {
	if p.IsEmpty() {
		return true, nil
	}
	for _, c := range p.NoneOf {
		matched, err := c.Matches(data, fetched)
		if err != nil {
			return false, err
		}
		if matched {
			return false, nil
		}
	}
	if len(p.AnyOf) == 0 {
		return true, nil
	}
	for _, c := range p.AnyOf {
		matched, err := c.Matches(data, fetched)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

// maxKeysPerPlan bounds the total deduped keys one request's MGet may
// span across every candidate package's profile. maxKeysPerCfg already
// caps a single cfg, but N candidate packages each expanding near the
// per-cfg cap could otherwise plan an unbounded fan-out; this is the
// request-wide backstop. A plan that exceeds it fails closed with
// ErrCfgUnsafe so the engine drops every package with a profile.
const maxKeysPerPlan = 65536

// MaxKeysPerPlan returns the request-wide key-plan limit.
func MaxKeysPerPlan() int { return maxKeysPerPlan }

// PlanLookup walks every profile, expands its cfgs against data, and
// returns the deduped list of keys to MGet. An ErrCfgUnsafe from any
// cfg's ExpandKeys — or a deduped key count exceeding maxKeysPerPlan —
// aborts the plan and returns (nil, err); the engine then fails-closed
// for every candidate with a non-empty profile (matching the engine's
// pattern on Valkey errors elsewhere in the request path).
func PlanLookup(profiles []*Profile, data LookupData) ([]string, error) {
	if len(profiles) == 0 || len(data) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	add := func(cfgs []Cfg) error {
		for _, c := range cfgs {
			keys, err := c.ExpandKeys(data)
			if err != nil {
				return err
			}
			for _, k := range keys {
				if _, dup := seen[k]; dup {
					continue
				}
				seen[k] = struct{}{}
				out = append(out, k)
				if len(out) > maxKeysPerPlan {
					return fmt.Errorf("plan exceeds %d keys: %w", maxKeysPerPlan, ErrCfgUnsafe)
				}
			}
		}
		return nil
	}
	for _, p := range profiles {
		if p.IsEmpty() {
			continue
		}
		if err := add(p.AnyOf); err != nil {
			return nil, err
		}
		if err := add(p.NoneOf); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DecodeValues turns the raw MGet output (one string per requested key,
// empty when the key was missing) into the fetched map MatchProfile
// expects. keys and values MUST be aligned 1:1.
func DecodeValues(keys []string, values []string) map[string][]string {
	if len(keys) != len(values) {
		return nil
	}
	out := make(map[string][]string, len(keys))
	for i, v := range values {
		if v == "" {
			continue
		}
		decoded := splitCSV(v)
		if len(decoded) == 0 {
			continue
		}
		out[keys[i]] = decoded
	}
	return out
}

// splitCSV splits a comma-separated value list, dropping empty
// segments so a malformed writer payload ("a,,b") cannot produce a
// spurious match against an empty signal_id (which Validate already
// rejects on the cfg side, but defense in depth on decode keeps the
// guarantee total).
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			n++
		}
	}
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
