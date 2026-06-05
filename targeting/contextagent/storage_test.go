package contextagent

import (
	"context"
	"testing"
)

func TestStorageSignalMGet_NilStoreFailsClosed(t *testing.T) {
	s := &storage{signals: nil}

	// No keys requested: nothing to fetch, no error.
	vals, err := s.SignalMGet(context.Background())
	if err != nil || vals != nil {
		t.Fatalf("empty keys must return (nil, nil), got (%v, %v)", vals, err)
	}

	// Keys requested but no signal store wired: must error so the
	// engine fails the package closed instead of treating a none_of
	// profile as vacuously passing.
	_, err = s.SignalMGet(context.Background(), "signal:1:country:US")
	if err == nil {
		t.Fatal("expected an error when signal store is nil but keys were requested")
	}
}
