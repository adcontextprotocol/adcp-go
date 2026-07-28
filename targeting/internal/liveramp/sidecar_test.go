package liveramp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_RequiresURL(t *testing.T) {
	_, err := NewClient(Config{})
	require.Error(t, err)
}

func TestNewClient_RejectsBadScheme(t *testing.T) {
	_, err := NewClient(Config{URL: "ftp://example.com/map"})
	require.Error(t, err)
}

func TestMappedID_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/map", r.URL.Path)
		assert.Equal(t, "the-env", r.URL.Query().Get("env"))
		_ = json.NewEncoder(w).Encode([]mapping{
			{Source: liverampSource, Mapping: map[string]string{scope3SeatID: "decoded-value"}},
		})
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL + "/v2/map", HTTPClient: srv.Client()})
	require.NoError(t, err)

	got, err := c.MappedID(t.Context(), "the-env")
	require.NoError(t, err)
	assert.Equal(t, "decoded-value", got)
}

func TestMappedID_EmptyBodyMeansNoMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "no-such-env")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestMappedID_OtherSourcesIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]mapping{
			{Source: "some-other-source.com", Mapping: map[string]string{scope3SeatID: "ignored"}},
		})
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "env")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestMappedID_MissingScope3KeyMeansNoMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]mapping{
			{Source: liverampSource, Mapping: map[string]string{"OtherSeat": "v"}},
		})
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "env")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestMappedID_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "env")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMapping, "500 must not be conflated with miss")
}

func TestMappedID_GoneMeansNoMapping(t *testing.T) {
	// 410 Gone is LiveRamp's signal for a permanently unresolvable envelope
	// (expired / revoked). Semantically a miss — callers must be able to
	// distinguish it from a transport error via errors.Is(err, ErrNoMapping).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "envelope expired", http.StatusGone)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "expired-env")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestMappedID_MalformedJSONIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "env")
	require.Error(t, err)
}

func TestMappedID_EmptyEnvRejectedClientSide(t *testing.T) {
	c, err := NewClient(Config{URL: "http://example.com/map"})
	require.NoError(t, err)
	_, err = c.MappedID(t.Context(), "")
	require.Error(t, err)
}

func TestMappedID_RespectsContextCancellation(t *testing.T) {
	// The handler never writes — the test asserts that a cancelled context
	// surfaces as an error rather than hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.MappedID(ctx, "env")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled"),
		"expected cancellation error, got %v", err)
}
