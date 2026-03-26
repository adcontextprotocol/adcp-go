package main

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestGeoFilter_Allowed(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("geo:allow:pkg-1", "US", "CA", "GB")

	mod := NewGeoFilterModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Geo: &tmp.Geo{Country: "US"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("US should be allowed")
	}
}

func TestGeoFilter_NotInAllowList(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("geo:allow:pkg-1", "US", "CA")

	mod := NewGeoFilterModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Geo: &tmp.Geo{Country: "DE"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("DE should not be allowed when allow list is US,CA")
	}
}

func TestGeoFilter_Blocked(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("geo:block:pkg-1", "CN", "RU")

	mod := NewGeoFilterModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Geo: &tmp.Geo{Country: "RU"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("RU should be blocked")
	}
}

func TestGeoFilter_NoGeo(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("geo:allow:pkg-1", "US")

	mod := NewGeoFilterModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("no geo means pass through")
	}
}

func TestGeoFilter_NoLists(t *testing.T) {
	v := NewMockValkeyClient()

	mod := NewGeoFilterModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Geo: &tmp.Geo{Country: "US"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("no allow/block lists means allow all")
	}
}
