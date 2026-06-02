package topicstore_test

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaxonomy_String(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	assert.Equal(t, "iab:7", tax.String())
}

func TestTaxonomy_Validate(t *testing.T) {
	cases := []struct {
		name string
		tax  topicstore.Taxonomy
		ok   bool
	}{
		{"valid", topicstore.Taxonomy{Source: "iab", ID: 7}, true},
		{"empty-source", topicstore.Taxonomy{Source: "", ID: 1}, false},
		{"negative-id", topicstore.Taxonomy{Source: "iab", ID: -1}, false},
		{"colon-in-source", topicstore.Taxonomy{Source: "ia:b", ID: 1}, false},
		{"slash-in-source", topicstore.Taxonomy{Source: "ia/b", ID: 1}, false},
		{"whitespace-in-source", topicstore.Taxonomy{Source: "iab v3", ID: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tax.Validate()
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestKeyShapes(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	assert.Equal(t, "topics:artifact:iab:7:article:pasta", topicstore.ArtifactKey(tax, "article:pasta"))
	assert.Equal(t, "topics:package:iab:7:pkg-food", topicstore.PackageKey(tax, "pkg-food"))
	assert.Equal(t, "iab:7:632", topicstore.NamespaceTopic(tax, "632"))
}

// TestKeyShapes_DistinctAcrossTaxonomies guards the core invariant the
// package exists to enforce: the same raw topic id in two taxonomies
// serializes to two different namespaced strings — and to two different
// Valkey keys — so a cross-taxonomy join cannot silently produce a hit.
func TestKeyShapes_DistinctAcrossTaxonomies(t *testing.T) {
	a := topicstore.Taxonomy{Source: "iab", ID: 7}
	b := topicstore.Taxonomy{Source: "custom", ID: 1}

	assert.NotEqual(t, topicstore.NamespaceTopic(a, "632"), topicstore.NamespaceTopic(b, "632"))
	assert.NotEqual(t, topicstore.ArtifactKey(a, "x"), topicstore.ArtifactKey(b, "x"))
	assert.NotEqual(t, topicstore.PackageKey(a, "p"), topicstore.PackageKey(b, "p"))
}

func TestWriter_SetArtifactTopics_Roundtrip(t *testing.T) {
	store := targeting.NewMockStore()
	w, err := topicstore.NewWriter(store)
	require.NoError(t, err)
	r, err := topicstore.NewReader(store)
	require.NoError(t, err)

	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	ctx := context.Background()

	require.NoError(t, w.SetArtifactTopics(ctx, tax, "article:pasta", []string{"632", "640"}))
	topics, err := r.ArtifactTopics(ctx, tax, "article:pasta")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"632", "640"}, topics)
}

func TestWriter_SetArtifactTopics_ReplacesPrevious(t *testing.T) {
	store := targeting.NewMockStore()
	w, _ := topicstore.NewWriter(store)
	r, _ := topicstore.NewReader(store)
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	ctx := context.Background()

	require.NoError(t, w.SetArtifactTopics(ctx, tax, "article:x", []string{"100", "200"}))
	require.NoError(t, w.SetArtifactTopics(ctx, tax, "article:x", []string{"300"}))

	topics, _ := r.ArtifactTopics(ctx, tax, "article:x")
	assert.ElementsMatch(t, []string{"300"}, topics, "second SetArtifactTopics call must replace, not add")
}

func TestWriter_AddRemovePackageTopics(t *testing.T) {
	store := targeting.NewMockStore()
	w, _ := topicstore.NewWriter(store)
	r, _ := topicstore.NewReader(store)
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	ctx := context.Background()

	require.NoError(t, w.AddPackageTopics(ctx, tax, "pkg-1", []string{"a", "b"}))
	require.NoError(t, w.AddPackageTopics(ctx, tax, "pkg-1", []string{"c"}))
	topics, _ := r.PackageTopics(ctx, tax, "pkg-1")
	assert.ElementsMatch(t, []string{"a", "b", "c"}, topics)

	require.NoError(t, w.RemovePackageTopics(ctx, tax, "pkg-1", []string{"b"}))
	topics, _ = r.PackageTopics(ctx, tax, "pkg-1")
	assert.ElementsMatch(t, []string{"a", "c"}, topics)

	require.NoError(t, w.RemovePackage(ctx, tax, "pkg-1"))
	topics, _ = r.PackageTopics(ctx, tax, "pkg-1")
	assert.Empty(t, topics)
}

func TestReader_IntersectArtifactPackage(t *testing.T) {
	store := targeting.NewMockStore()
	w, _ := topicstore.NewWriter(store)
	r, _ := topicstore.NewReader(store)
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	ctx := context.Background()

	require.NoError(t, w.SetPackageTopics(ctx, tax, "pkg-food", []string{"632", "640"}))
	require.NoError(t, w.SetArtifactTopics(ctx, tax, "article:pasta", []string{"632", "999"}))

	inter, err := r.IntersectArtifactPackage(ctx, tax, "pkg-food", "article:pasta")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"632"}, inter)
}

// TestReader_IntersectArtifactPackage_CrossTaxonomyDisjoint verifies that
// the same topic id written under two different taxonomies does not
// produce an intersection — the key-shape distinction enforces taxonomy
// isolation at the storage layer.
func TestReader_IntersectArtifactPackage_CrossTaxonomyDisjoint(t *testing.T) {
	store := targeting.NewMockStore()
	w, _ := topicstore.NewWriter(store)
	r, _ := topicstore.NewReader(store)
	ctx := context.Background()

	a := topicstore.Taxonomy{Source: "iab", ID: 7}
	b := topicstore.Taxonomy{Source: "custom", ID: 1}

	require.NoError(t, w.SetPackageTopics(ctx, a, "pkg-food", []string{"632"}))
	require.NoError(t, w.SetArtifactTopics(ctx, b, "article:pasta", []string{"632"}))

	// Intersect under taxonomy a sees the package set but no artifact set.
	inter, err := r.IntersectArtifactPackage(ctx, a, "pkg-food", "article:pasta")
	require.NoError(t, err)
	assert.Empty(t, inter)
}

func TestNamespaceTopics_PreservesOrderAndPrefixes(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	got := topicstore.NamespaceTopics(tax, []string{"632", "640", "999"})
	assert.Equal(t, []string{"iab:7:632", "iab:7:640", "iab:7:999"}, got)

	assert.Nil(t, topicstore.NamespaceTopics(tax, nil))
	assert.Nil(t, topicstore.NamespaceTopics(tax, []string{}))
}

func TestWriter_NilStoreRejected(t *testing.T) {
	_, err := topicstore.NewWriter(nil)
	assert.Error(t, err)
	_, err = topicstore.NewReader(nil)
	assert.Error(t, err)
}

func TestWriter_InvalidInputs(t *testing.T) {
	store := targeting.NewMockStore()
	w, _ := topicstore.NewWriter(store)
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	bad := topicstore.Taxonomy{}
	ctx := context.Background()

	assert.Error(t, w.SetArtifactTopics(ctx, bad, "x", []string{"a"}), "invalid taxonomy must error")
	assert.Error(t, w.SetArtifactTopics(ctx, tax, "", []string{"a"}), "empty ref must error")
	assert.Error(t, w.SetPackageTopics(ctx, tax, "", []string{"a"}), "empty pkg must error")
}
