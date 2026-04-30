package prommetrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsInterface(t *testing.T) {
	m := New()

	m.ContextEvaluated("pkg-1", "property_bitmap", true)
	m.ContextEvaluated("pkg-1", "property_bitmap", false)
	m.ContextEvaluated("pkg-2", "topic_match", true)
	m.IdentityEvaluated("pkg-2", "audience", false)
	m.StoreError("suppression", errors.New("timeout"))
	m.Latency("property_bitmap", 50*time.Microsecond)
	m.Latency("property_bitmap", 100*time.Microsecond)

	rec := httptest.NewRecorder()
	m.Registry.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	expectedStrings := []string{
		"# TYPE targeting_context_evaluated_total counter",
		"targeting_context_evaluated_total{stage=\"property_bitmap\",passed=\"true\"} 1",
		"targeting_context_evaluated_total{stage=\"property_bitmap\",passed=\"false\"} 1",
		"# TYPE targeting_identity_evaluated_total counter",
		"targeting_identity_evaluated_total{stage=\"audience\",passed=\"false\"} 1",
		"targeting_store_errors_total{operation=\"suppression\"} 1",
		"# TYPE targeting_stage_duration_seconds histogram",
		"targeting_stage_duration_seconds_bucket{stage=\"property_bitmap\",le=\"+Inf\"} 2",
		"targeting_stage_duration_seconds_count{stage=\"property_bitmap\"} 2",
	}

	for _, want := range expectedStrings {
		assert.Contains(t, text, want, "metrics output missing expected string")
	}

	// No Go runtime metrics (no protobuf, no /proc).
	assert.NotContains(t, text, "go_goroutines", "should not contain Go runtime metrics")
}

func TestGaugeOutput(t *testing.T) {
	reg := NewRegistry()
	reg.DefineGauge("provider_health", "Health status.", []string{"provider"})
	reg.GaugeSet("provider_health", 1, "acme")
	reg.GaugeSet("provider_health", 0, "broken")

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	for _, want := range []string{
		"# HELP provider_health Health status.",
		"# TYPE provider_health gauge",
		`provider_health{provider="acme"} 1`,
		`provider_health{provider="broken"} 0`,
	} {
		assert.Contains(t, text, want, "gauge output missing expected string")
	}
}

func TestGaugeSet_Overwrite(t *testing.T) {
	reg := NewRegistry()
	reg.DefineGauge("my_gauge", "Test.", []string{"id"})
	reg.GaugeSet("my_gauge", 5, "a")
	reg.GaugeSet("my_gauge", 10, "a") // overwrite

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	assert.Contains(t, text, `my_gauge{id="a"} 10`, "gauge should be overwritten to 10")
	assert.NotContains(t, text, `my_gauge{id="a"} 5`, "old gauge value should not appear")
}

func TestConcurrentAccess(t *testing.T) {
	m := New()
	done := make(chan struct{})
	for range 10 {
		go func() {
			for range 1000 {
				m.ContextEvaluated("pkg", "bitmap", true)
				m.Latency("bitmap", time.Microsecond)
			}
			done <- struct{}{}
		}()
	}
	for range 10 {
		<-done
	}

	rec := httptest.NewRecorder()
	m.Registry.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "targeting_context_evaluated_total{stage=\"bitmap\",passed=\"true\"} 10000", "expected 10000 context evaluations")
	assert.Contains(t, text, "targeting_stage_duration_seconds_count{stage=\"bitmap\"} 10000", "expected 10000 histogram observations")
}
