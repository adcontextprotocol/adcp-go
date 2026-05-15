package targeting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSegmentRule_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		rule *SegmentRule
		want bool
	}{
		{"nil rule", nil, true},
		{"zero-value rule", &SegmentRule{}, true},
		{"AllOf only", &SegmentRule{AllOf: []string{"x"}}, false},
		{"AnyOf only", &SegmentRule{AnyOf: []string{"x"}}, false},
		{"NoneOf only", &SegmentRule{NoneOf: []string{"x"}}, false},
		{"empty slices", &SegmentRule{AllOf: []string{}, AnyOf: []string{}, NoneOf: []string{}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.rule.IsEmpty())
		})
	}
}

func TestSegmentRule_Matches(t *testing.T) {
	userIn := func(segs ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(segs))
		for _, s := range segs {
			out[s] = struct{}{}
		}
		return out
	}

	cases := []struct {
		name string
		rule *SegmentRule
		user map[string]struct{}
		want bool
	}{
		{"nil rule matches everyone", nil, nil, true},
		{"empty rule matches everyone", &SegmentRule{}, nil, true},
		{"AllOf met", &SegmentRule{AllOf: []string{"a", "b"}}, userIn("a", "b", "c"), true},
		{"AllOf missing one", &SegmentRule{AllOf: []string{"a", "b"}}, userIn("a"), false},
		{"AnyOf met", &SegmentRule{AnyOf: []string{"a", "b"}}, userIn("b"), true},
		{"AnyOf no overlap", &SegmentRule{AnyOf: []string{"a", "b"}}, userIn("c"), false},
		{"NoneOf clean", &SegmentRule{NoneOf: []string{"a"}}, userIn("b"), true},
		{"NoneOf hit", &SegmentRule{NoneOf: []string{"a"}}, userIn("a"), false},
		{"AllOf+AnyOf+NoneOf all pass", &SegmentRule{AllOf: []string{"a"}, AnyOf: []string{"b", "c"}, NoneOf: []string{"d"}}, userIn("a", "b"), true},
		{"AllOf passes but NoneOf hits", &SegmentRule{AllOf: []string{"a"}, NoneOf: []string{"d"}}, userIn("a", "d"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.rule.Matches(c.user))
		})
	}
}

func TestSegmentRule_Clone(t *testing.T) {
	assert.Nil(t, (*SegmentRule)(nil).Clone())

	src := &SegmentRule{
		AllOf:  []string{"a", "b"},
		AnyOf:  []string{"c"},
		NoneOf: []string{"d", "e"},
	}
	dst := src.Clone()

	// Equal contents.
	assert.Equal(t, src.AllOf, dst.AllOf)
	assert.Equal(t, src.AnyOf, dst.AnyOf)
	assert.Equal(t, src.NoneOf, dst.NoneOf)

	// Independent backing arrays — appending to one must not affect the other.
	dst.AllOf = append(dst.AllOf, "extra")
	dst.AnyOf = append(dst.AnyOf, "extra")
	dst.NoneOf = append(dst.NoneOf, "extra")
	assert.Equal(t, []string{"a", "b"}, src.AllOf, "source AllOf must not be affected by clone mutation")
	assert.Equal(t, []string{"c"}, src.AnyOf)
	assert.Equal(t, []string{"d", "e"}, src.NoneOf)
}

func TestSegmentRule_CloneEmptyPreserveNilSlices(t *testing.T) {
	// An empty rule clones to another empty rule with nil slices —
	// don't waste allocation on empty clauses.
	dst := (&SegmentRule{}).Clone()
	require.NotNil(t, dst)
	assert.Nil(t, dst.AllOf)
	assert.Nil(t, dst.AnyOf)
	assert.Nil(t, dst.NoneOf)
}

func TestSegmentRule_Segments(t *testing.T) {
	assert.Nil(t, (*SegmentRule)(nil).Segments())
	assert.Nil(t, (&SegmentRule{}).Segments())

	got := (&SegmentRule{
		AllOf:  []string{"a", "b"},
		AnyOf:  []string{"b", "c"}, // b duplicated across clauses
		NoneOf: []string{"d"},
	}).Segments()
	assert.ElementsMatch(t, []string{"a", "b", "c", "d"}, got)
}
