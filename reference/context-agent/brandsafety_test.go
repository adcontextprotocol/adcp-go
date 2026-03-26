package main

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestBrandSafety_Clean(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("safety:artifact:article:cooking", "food", "lifestyle")
	v.SAdd("safety:blocked:pkg-1", "violence", "gambling")

	mod := NewBrandSafetyModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:cooking"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("should activate — content is safe for package")
	}
}

func TestBrandSafety_Blocked(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("safety:artifact:article:betting", "gambling", "sports")
	v.SAdd("safety:blocked:pkg-1", "gambling", "violence")

	mod := NewBrandSafetyModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:betting"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("should not activate — content has blocked category")
	}
}

func TestBrandSafety_NoBlockList(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("safety:artifact:article:anything", "adult")

	mod := NewBrandSafetyModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:anything"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("should activate — no block list means no restrictions")
	}
}

func TestBrandSafety_NoArtifacts(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("safety:blocked:pkg-1", "violence")

	mod := NewBrandSafetyModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("should activate — no artifacts to check")
	}
}
