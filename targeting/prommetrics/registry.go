package prommetrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry collects counters, gauges, and histograms, and serves them in
// Prometheus text exposition format (v0.0.4).
type Registry struct {
	mu            sync.RWMutex
	counterDefs   map[string]*counterDef
	gaugeDefs     map[string]*gaugeDef
	histogramDefs map[string]*histogramDef
	order         []string // insertion order for stable output

	counters   sync.Map // "name\x00label1\x00label2..." -> *atomic.Int64
	gauges     sync.Map // "name\x00label1\x00label2..." -> *atomic.Int64 (value * 1e9)
	histograms sync.Map // "name\x00label1\x00label2..." -> *histogram
}

type counterDef struct {
	help   string
	labels []string
}

type gaugeDef struct {
	help   string
	labels []string
}

type histogramDef struct {
	help    string
	labels  []string
	buckets []float64
}

type histogram struct {
	buckets []float64
	counts  []atomic.Int64 // one per bucket + 1 for overflow into +Inf
	sum     atomic.Int64   // stored as nanoseconds (value * 1e9) to avoid float atomics
	count   atomic.Int64
}

// NewRegistry creates an empty metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counterDefs:   make(map[string]*counterDef),
		gaugeDefs:     make(map[string]*gaugeDef),
		histogramDefs: make(map[string]*histogramDef),
	}
}

// DefineGauge registers a gauge metric. Call once at init time.
func (r *Registry) DefineGauge(name, help string, labels []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gaugeDefs[name] = &gaugeDef{help: help, labels: labels}
	r.order = append(r.order, name)
}

// GaugeSet sets a gauge to a specific value. Label values must match
// the order of labels passed to DefineGauge.
func (r *Registry) GaugeSet(name string, value float64, labelValues ...string) {
	key := buildKey(name, labelValues)
	encoded := int64(value * 1e9)
	if v, ok := r.gauges.Load(key); ok {
		v.(*atomic.Int64).Store(encoded)
		return
	}
	v, _ := r.gauges.LoadOrStore(key, &atomic.Int64{})
	v.(*atomic.Int64).Store(encoded)
}

// DefineCounter registers a counter metric. Call once at init time.
func (r *Registry) DefineCounter(name, help string, labels []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counterDefs[name] = &counterDef{help: help, labels: labels}
	r.order = append(r.order, name)
}

// DefineHistogram registers a histogram metric. Call once at init time.
func (r *Registry) DefineHistogram(name, help string, labels []string, buckets []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.histogramDefs[name] = &histogramDef{help: help, labels: labels, buckets: buckets}
	r.order = append(r.order, name)
}

// CounterInc increments a counter. Label values must match the order
// of labels passed to DefineCounter.
func (r *Registry) CounterInc(name string, labelValues ...string) {
	r.CounterAdd(name, 1, labelValues...)
}

// CounterAdd adds delta to a counter. Negative deltas are silently dropped
// since counters MUST be monotonic per Prometheus conventions. Label values
// must match the order of labels passed to DefineCounter.
func (r *Registry) CounterAdd(name string, delta int64, labelValues ...string) {
	if delta <= 0 {
		return
	}
	key := buildKey(name, labelValues)
	if v, ok := r.counters.Load(key); ok {
		v.(*atomic.Int64).Add(delta)
		return
	}
	v, _ := r.counters.LoadOrStore(key, &atomic.Int64{})
	v.(*atomic.Int64).Add(delta)
}

// HistogramObserve records a value in a histogram. Label values must match
// the order of labels passed to DefineHistogram.
func (r *Registry) HistogramObserve(name string, value float64, labelValues ...string) {
	key := buildKey(name, labelValues)
	var h *histogram
	if v, ok := r.histograms.Load(key); ok {
		h = v.(*histogram)
	} else {
		r.mu.RLock()
		def := r.histogramDefs[name]
		r.mu.RUnlock()
		buckets := []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
		if def != nil {
			buckets = def.buckets
		}
		h = newHistogram(buckets)
		if v, loaded := r.histograms.LoadOrStore(key, h); loaded {
			h = v.(*histogram)
		}
	}
	h.observe(value)
}

// Handler returns an http.Handler serving Prometheus text exposition format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		r.writeTo(&b)
		_, _ = fmt.Fprint(w, b.String())
	})
}

func (r *Registry) writeTo(b *strings.Builder) {
	r.mu.RLock()
	order := make([]string, len(r.order))
	copy(order, r.order)
	r.mu.RUnlock()

	// Deduplicate (a name may appear once).
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true

		r.mu.RLock()
		cDef := r.counterDefs[name]
		gDef := r.gaugeDefs[name]
		hDef := r.histogramDefs[name]
		r.mu.RUnlock()

		if cDef != nil {
			r.writeCounter(b, name, cDef)
		} else if gDef != nil {
			r.writeGauge(b, name, gDef)
		} else if hDef != nil {
			r.writeHistogram(b, name, hDef)
		}
	}
}

func (r *Registry) writeCounter(b *strings.Builder, name string, def *counterDef) {
	// Collect all entries for this counter.
	type entry struct {
		labels []string
		value  int64
	}
	var entries []entry

	prefix := name + "\x00"
	r.counters.Range(func(k, v any) bool {
		key := k.(string)
		if key == name || strings.HasPrefix(key, prefix) {
			labels := parseLabels(key)
			entries = append(entries, entry{labels, v.(*atomic.Int64).Load()})
		}
		return true
	})

	if len(entries) == 0 {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.Join(entries[i].labels, ",") < strings.Join(entries[j].labels, ",")
	})

	fmt.Fprintf(b, "# HELP %s %s\n", name, def.help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)
	for _, e := range entries {
		fmt.Fprintf(b, "%s%s %d\n", name, formatLabelPairs(def.labels, e.labels), e.value)
	}
}

func (r *Registry) writeGauge(b *strings.Builder, name string, def *gaugeDef) {
	type entry struct {
		labels []string
		value  float64
	}
	var entries []entry

	prefix := name + "\x00"
	r.gauges.Range(func(k, v any) bool {
		key := k.(string)
		if key == name || strings.HasPrefix(key, prefix) {
			labels := parseLabels(key)
			val := float64(v.(*atomic.Int64).Load()) / 1e9
			entries = append(entries, entry{labels, val})
		}
		return true
	})

	if len(entries) == 0 {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.Join(entries[i].labels, ",") < strings.Join(entries[j].labels, ",")
	})

	fmt.Fprintf(b, "# HELP %s %s\n", name, def.help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	for _, e := range entries {
		fmt.Fprintf(b, "%s%s %s\n", name, formatLabelPairs(def.labels, e.labels), formatFloat(e.value))
	}
}

func (r *Registry) writeHistogram(b *strings.Builder, name string, def *histogramDef) {
	type histEntry struct {
		labels []string
		h      *histogram
	}
	var entries []histEntry

	prefix := name + "\x00"
	r.histograms.Range(func(k, v any) bool {
		key := k.(string)
		if key == name || strings.HasPrefix(key, prefix) {
			labels := parseLabels(key)
			entries = append(entries, histEntry{labels, v.(*histogram)})
		}
		return true
	})

	if len(entries) == 0 {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.Join(entries[i].labels, ",") < strings.Join(entries[j].labels, ",")
	})

	fmt.Fprintf(b, "# HELP %s %s\n", name, def.help)
	fmt.Fprintf(b, "# TYPE %s histogram\n", name)

	for _, e := range entries {
		labelStr := joinLabelPairs(def.labels, e.labels)
		comma := ""
		if labelStr != "" {
			comma = ","
		}

		cumulative := int64(0)
		for i, bound := range e.h.buckets {
			cumulative += e.h.counts[i].Load()
			fmt.Fprintf(b, "%s_bucket{%s%sle=\"%s\"} %d\n", name, labelStr, comma, formatFloat(bound), cumulative)
		}
		cumulative += e.h.counts[len(e.h.buckets)].Load()
		fmt.Fprintf(b, "%s_bucket{%s%sle=\"+Inf\"} %d\n", name, labelStr, comma, cumulative)
		sumF := float64(e.h.sum.Load()) / 1e9
		fmt.Fprintf(b, "%s_sum{%s} %s\n", name, labelStr, formatFloat(sumF))
		fmt.Fprintf(b, "%s_count{%s} %d\n", name, labelStr, e.h.count.Load())
	}
}

func newHistogram(buckets []float64) *histogram {
	return &histogram{
		buckets: buckets,
		counts:  make([]atomic.Int64, len(buckets)+1),
	}
}

func (h *histogram) observe(v float64) {
	idx := len(h.buckets) // default: overflow bucket
	for i, bound := range h.buckets {
		if v <= bound {
			idx = i
			break
		}
	}
	h.counts[idx].Add(1)
	h.count.Add(1)
	h.sum.Add(int64(v * 1e9))
}

func buildKey(name string, labels []string) string {
	if len(labels) == 0 {
		return name
	}
	return name + "\x00" + strings.Join(labels, "\x00")
}

func parseLabels(key string) []string {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) < 2 || parts[1] == "" {
		return nil
	}
	return strings.Split(parts[1], "\x00")
}

func joinLabelPairs(names, values []string) string {
	if len(values) == 0 {
		return ""
	}
	var parts []string
	for i, v := range values {
		name := "label"
		if i < len(names) {
			name = names[i]
		}
		parts = append(parts, fmt.Sprintf("%s=%q", name, v))
	}
	return strings.Join(parts, ",")
}

func formatLabelPairs(names, values []string) string {
	raw := joinLabelPairs(names, values)
	if raw == "" {
		return ""
	}
	return "{" + raw + "}"
}

func formatFloat(f float64) string {
	if f == math.Inf(1) {
		return "+Inf"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
