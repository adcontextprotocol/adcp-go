package prommetrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsInterface(t *testing.T) {
	m := New()

	m.ContextEvaluated("pkg-1", "property_bitmap", true)
	m.ContextEvaluated("pkg-1", "property_bitmap", false)
	m.ContextEvaluated("pkg-2", "topic_match", true)
	m.IdentityEvaluated("pkg-2", "campaign_freq", false)
	m.ExposureRecorded("pkg-1")
	m.StoreError("suppression", errors.New("timeout"))
	m.Latency("property_bitmap", 50*time.Microsecond)
	m.Latency("property_bitmap", 100*time.Microsecond)

	rec := httptest.NewRecorder()
	m.Registry.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	for _, want := range []string{
		"# TYPE targeting_context_evaluated_total counter",
		"targeting_context_evaluated_total{stage=\"property_bitmap\",passed=\"true\"} 1",
		"targeting_context_evaluated_total{stage=\"property_bitmap\",passed=\"false\"} 1",
		"# TYPE targeting_identity_evaluated_total counter",
		"targeting_identity_evaluated_total{stage=\"campaign_freq\",passed=\"false\"} 1",
		"targeting_exposure_recorded_total 1",
		"targeting_store_errors_total{operation=\"suppression\"} 1",
		"# TYPE targeting_stage_duration_seconds histogram",
		"targeting_stage_duration_seconds_bucket{stage=\"property_bitmap\",le=\"+Inf\"} 2",
		"targeting_stage_duration_seconds_count{stage=\"property_bitmap\"} 2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics output missing %q\n\ngot:\n%s", want, text)
		}
	}

	// No Go runtime metrics (no protobuf, no /proc).
	if strings.Contains(text, "go_goroutines") {
		t.Error("should not contain Go runtime metrics")
	}
}

func TestConcurrentAccess(t *testing.T) {
	m := New()
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 1000; j++ {
				m.ContextEvaluated("pkg", "bitmap", true)
				m.Latency("bitmap", time.Microsecond)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	rec := httptest.NewRecorder()
	m.Registry.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body, _ := io.ReadAll(rec.Body)
	text := string(body)
	if !strings.Contains(text, "targeting_context_evaluated_total{stage=\"bitmap\",passed=\"true\"} 10000") {
		t.Errorf("expected 10000 context evaluations, got:\n%s", text)
	}
	if !strings.Contains(text, "targeting_stage_duration_seconds_count{stage=\"bitmap\"} 10000") {
		t.Errorf("expected 10000 histogram observations, got:\n%s", text)
	}
}
