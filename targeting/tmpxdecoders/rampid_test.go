package tmpxdecoders

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting/internal/liveramp"
)

type stubLiveRampClient struct {
	mapped string
	err    error
}

func (s stubLiveRampClient) MappedID(_ context.Context, _ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.mapped, nil
}

func TestRampID_PassesMappedValueThrough(t *testing.T) {
	const mapped = "abcdef0123456789-mapped-rampid"
	got, err := RampID{Client: stubLiveRampClient{mapped: mapped}}.Decode(t.Context(), "any-env")
	require.NoError(t, err)
	assert.Equal(t, []byte(mapped), got,
		"decoder must return the sidecar's mapped value verbatim — size enforcement is selectEntries' job")
}

func TestRampID_NoMappingDropsIdentity(t *testing.T) {
	client := stubLiveRampClient{err: ErrLiveRampNoMapping}
	_, err := RampID{Client: client}.Decode(t.Context(), "miss")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDropFromSeal, "miss must produce drop sentinel")
}

func TestRampID_EmptyMappedValueDrops(t *testing.T) {
	client := stubLiveRampClient{mapped: ""}
	_, err := RampID{Client: client}.Decode(t.Context(), "env")
	require.ErrorIs(t, err, ErrDropFromSeal)
}

func TestRampID_TransportErrorBubbles(t *testing.T) {
	boom := errors.New("connection refused")
	client := stubLiveRampClient{err: boom}
	_, err := RampID{Client: client}.Decode(t.Context(), "env")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "connection refused"))
	assert.NotErrorIs(t, err, ErrDropFromSeal)
}

func TestRampID_NilClientErrors(t *testing.T) {
	_, err := RampID{}.Decode(t.Context(), "env")
	require.Error(t, err)
}

func TestRampIDDerived_PassesMappedValueThrough(t *testing.T) {
	const mapped = "rampid-derived-mapped-value"
	got, err := RampIDDerived{Client: stubLiveRampClient{mapped: mapped}}.Decode(t.Context(), "env")
	require.NoError(t, err)
	assert.Equal(t, []byte(mapped), got)
}

// TestRampID_EndToEndAgainstRealLiveRampClient drives RampID.Decode through
// the production liveramp.Client wired to an httptest.Server. This catches
// regressions the in-package stubs can't: JSON shape, sidecar URL
// construction, header handling, body parsing, and the contract that the
// decoded bytes equal whatever the sidecar published. Renaming a JSON
// field or changing the response shape would fail here even if the unit
// tests above all pass.
func TestRampID_EndToEndAgainstRealLiveRampClient(t *testing.T) {
	cases := []struct {
		name       string
		mapped     string
		wantDecode []byte
	}{
		{
			name:       "hex-32-byte-string",
			mapped:     "deadbeefcafebabe0011223344556677deadbeefcafebabe0011223344556677",
			wantDecode: []byte("deadbeefcafebabe0011223344556677deadbeefcafebabe0011223344556677"),
		},
		{
			name:       "opaque-ascii-string",
			mapped:     "scope3-rampid-A6B7C8D9-EF01-2345-6789-ABCDEF012345",
			wantDecode: []byte("scope3-rampid-A6B7C8D9-EF01-2345-6789-ABCDEF012345"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v2/map", r.URL.Path)
				assert.Equal(t, "some-rampid-env", r.URL.Query().Get("env"))
				_ = json.NewEncoder(w).Encode([]struct {
					Source  string            `json:"source"`
					Mapping map[string]string `json:"mapping"`
				}{
					{Source: "liveramp.com", Mapping: map[string]string{"Scope3": tc.mapped}},
				})
			}))
			defer srv.Close()

			client, err := liveramp.NewClient(liveramp.Config{
				URL:        srv.URL + "/v2/map",
				HTTPClient: srv.Client(),
			})
			require.NoError(t, err)

			got, err := RampID{Client: client}.Decode(t.Context(), "some-rampid-env")
			require.NoError(t, err)
			assert.Equal(t, tc.wantDecode, got,
				"RampID.Decode must return the sidecar's mapped value verbatim")
		})
	}
}

func TestRampID_EndToEndNoMappingDrops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := liveramp.NewClient(liveramp.Config{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
	})
	require.NoError(t, err)

	_, err = RampID{Client: client}.Decode(t.Context(), "missing-env")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDropFromSeal,
		"sidecar miss must surface as drop sentinel so selectEntries skips the identity")
}
