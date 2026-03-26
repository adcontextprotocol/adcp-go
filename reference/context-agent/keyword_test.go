package main

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestKeywordMatch_Overlap(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("keywords:package:pkg-1", "organic", "sustainable", "local")
	v.SAdd("keywords:artifact:article:farm", "organic", "farming", "local")

	mod := NewKeywordMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts:     []string{"article:farm"},
		AvailablePkgs: []tmp.AvailablePackage{{PackageID: "pkg-1"}},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("should activate with keyword overlap")
	}
}

func TestKeywordMatch_NoOverlap(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("keywords:package:pkg-1", "luxury", "fashion")
	v.SAdd("keywords:artifact:article:farm", "organic", "farming")

	mod := NewKeywordMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts:     []string{"article:farm"},
		AvailablePkgs: []tmp.AvailablePackage{{PackageID: "pkg-1"}},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("should not activate without keyword overlap")
	}
}

func TestKeywordMatch_MinMatch(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("keywords:package:pkg-1", "organic", "sustainable", "local")
	v.SAdd("keywords:artifact:article:farm", "organic", "other")

	mod := NewKeywordMatchModule(v)
	mod.MinMatch = 2 // Require 2 keywords to match
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts:     []string{"article:farm"},
		AvailablePkgs: []tmp.AvailablePackage{{PackageID: "pkg-1"}},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("should not activate with only 1 overlap when MinMatch=2")
	}
}

func TestKeywordMatch_NoArtifacts(t *testing.T) {
	v := NewMockValkeyClient()
	mod := NewKeywordMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		AvailablePkgs: []tmp.AvailablePackage{{PackageID: "pkg-1"}},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("no artifacts should pass through")
	}
}
