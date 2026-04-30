package targeting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeUserProfiles(t *testing.T) {
	p1 := &UserProfile{Segments: map[string]float64{"cooking_fans": 0.8, "sports": 0.3}}
	p2 := &UserProfile{Segments: map[string]float64{"cooking_fans": 0.5, "tech": 0.9}}

	merged := MergeUserProfiles(p1, p2)
	assert.Equal(t, 0.8, merged.Segments["cooking_fans"], "expected higher value")
	assert.Equal(t, 0.3, merged.Segments["sports"])
	assert.Equal(t, 0.9, merged.Segments["tech"])
	assert.Len(t, merged.Segments, 3)
}

func TestMergeUserProfiles_WithNil(t *testing.T) {
	p1 := &UserProfile{Segments: map[string]float64{"cooking": 0.5}}
	merged := MergeUserProfiles(nil, p1, nil)
	assert.Len(t, merged.Segments, 1)
}

func TestParseUserProfile_Empty(t *testing.T) {
	assert.Nil(t, ParseUserProfile(""))
}

func TestParseUserProfile_Valid(t *testing.T) {
	p := ParseUserProfile(`{"segments":{"cooking":0.8}}`)
	assert.NotNil(t, p)
	assert.Equal(t, 0.8, p.Segments["cooking"])
}
