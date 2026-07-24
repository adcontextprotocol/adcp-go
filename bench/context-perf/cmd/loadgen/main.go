// loadgen is a closed-loop QPS-paced HTTP load generator targeted at
// the context-agent's /context endpoint. It draws property_rid,
// placement_id, and (optionally) artifact_refs from the seeded pool,
// builds a valid ContextMatchRequest, fires at the configured rate, and
// reports latency percentiles + status/error breakdown + observed
// throughput.
//
// SIGN_REQUESTS=true switches the wire path from unsigned (TMP_ALLOW_UNSIGNED
// on the SUT) to Ed25519-signed via tmproto.Signer, so the sweep exercises
// the same signature-verification path a signed-mode production deployment
// pays on every request. The keypair is read from SIGNER_KEY_PATH — the mock
// tmpregistry service publishes the matching public JWK on its snapshot
// endpoint.
//
// Pacer + reporting semantics mirror bench/identity-perf/cmd/loadgen so the
// two harnesses' reports slot into the same summary CSV shape.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/adcontextprotocol/adcp-go/bench/context-perf/internal/corpus"
	"github.com/adcontextprotocol/adcp-go/bench/context-perf/internal/signkey"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

type sample struct {
	latency time.Duration
	status  int
	err     bool
}

func main() {
	target := flag.String("target", envStr("TARGET", "http://context-agent:8081/context"), "context-agent /context URL")
	sellerAgent := flag.String("seller-agent", envStr("SELLER_AGENT_URL", corpus.SellerAgentURL), "seller_agent_url on requests")
	adcpVersion := flag.String("adcp-version", "3.1", "adcp_version on requests")
	totalPackages := flag.Int("total-packages", envInt("TOTAL_PACKAGES", 200), "package pool size (0 = omit package_ids)")
	packagesPerReq := flag.Int("packages-per-req", envInt("PACKAGES_PER_REQ", 0), "package_ids per request; 0 = omit (evaluate against all active)")
	totalArtifacts := flag.Int("total-artifacts", envInt("TOTAL_ARTIFACTS", 1000), "artifact URL pool the seeder populated")
	artifactRefsPerReq := flag.Int("artifact-refs-per-req", envInt("ARTIFACT_REFS_PER_REQ", 0), "artifact_refs per request; 0 = omit")
	qps := flag.Int("qps", envInt("QPS", 1000), "target requests per second")
	concurrency := flag.Int("concurrency", envInt("CONCURRENCY", 128), "max in-flight requests")
	duration := flag.Duration("duration", envDur("DURATION", 30*time.Second), "test duration")
	warmup := flag.Duration("warmup", envDur("WARMUP", 3*time.Second), "warmup discarded from stats")
	reportPath := flag.String("report", envStr("REPORT", ""), "optional path to write JSON report")
	label := flag.String("label", envStr("LABEL", ""), "label included in the report")
	signRequests := flag.Bool("sign-requests", envBool("SIGN_REQUESTS", false), "when true, sign every request with the shared bench keypair")
	signerKeyPath := flag.String("signer-key-path", envStr("SIGNER_KEY_PATH", "/keys/signer.json"), "path to the loadgen ed25519 keypair (shared with tmpregistry)")
	signerKeyWait := flag.Duration("signer-key-wait", envDur("SIGNER_KEY_WAIT", 30*time.Second), "max time to wait for the signer key file to appear before failing")
	providerEndpoint := flag.String("provider-endpoint-url", envStr("TMP_OWN_ENDPOINT_URL", "http://context-agent:8081/context"), "provider_endpoint_url the SUT verifies against — must match the SUT's TMP_OWN_ENDPOINT_URL")
	flag.Parse()

	propertyRIDs := corpus.PropertyRIDs()
	if len(propertyRIDs) == 0 {
		log.Fatalf("corpus.PropertyRIDs returned empty list — PROPERTY_RIDS misconfigured")
	}

	var signer *tmproto.Signer
	if *signRequests {
		kp, err := signkey.WaitFor(*signerKeyPath, *signerKeyWait, 500*time.Millisecond)
		if err != nil {
			log.Fatalf("load signer keypair: %v", err)
		}
		s, err := tmproto.NewSigner(kp.Kid, kp.PrivateKey)
		if err != nil {
			log.Fatalf("construct signer: %v", err)
		}
		signer = s
		log.Printf("signing enabled: kid=%s provider_endpoint_url=%s", kp.Kid, *providerEndpoint)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			MaxConnsPerHost:     *concurrency * 2,
			IdleConnTimeout:     90 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("interrupt received, stopping")
		cancel()
	}()

	wp := waveParams{
		target: *target, sellerAgent: *sellerAgent, adcpVersion: *adcpVersion,
		propertyRIDs:  propertyRIDs,
		totalPackages: *totalPackages, packagesPerReq: *packagesPerReq,
		totalArtifacts: *totalArtifacts, artifactRefsPerReq: *artifactRefsPerReq,
		qps: *qps, concurrency: *concurrency,
		signer: signer, providerEndpoint: *providerEndpoint,
	}

	log.Printf("warmup %s @ %d qps against %s", *warmup, *qps, *target)
	warmupCtx, warmupCancel := context.WithTimeout(ctx, *warmup)
	runWave(warmupCtx, client, wp)
	warmupCancel()

	log.Printf("measuring %s @ %d qps (concurrency %d)", *duration, *qps, *concurrency)
	samples := make(chan sample, *concurrency*16)
	statsDone := make(chan report)
	go func() { statsDone <- collectStats(samples) }()

	measureCtx, measureCancel := context.WithTimeout(ctx, *duration)
	elapsed := time.Now()
	wp.samples = samples
	runWave(measureCtx, client, wp)
	measureCancel()
	close(samples)
	rep := <-statsDone
	rep.WallSeconds = time.Since(elapsed).Seconds()
	rep.Label = *label
	rep.Target = *target
	rep.TargetQPS = *qps
	rep.Concurrency = *concurrency
	rep.PackagesPerReq = *packagesPerReq
	rep.ArtifactRefsPerReq = *artifactRefsPerReq
	rep.Signed = *signRequests

	rep.print(os.Stdout)
	if *reportPath != "" {
		if err := writeJSON(*reportPath, rep); err != nil {
			log.Fatalf("write report: %v", err)
		}
	}
}

type waveParams struct {
	target             string
	sellerAgent        string
	adcpVersion        string
	propertyRIDs       []string
	totalPackages      int
	packagesPerReq     int
	totalArtifacts     int
	artifactRefsPerReq int
	qps                int
	concurrency        int
	samples            chan<- sample
	signer             *tmproto.Signer
	providerEndpoint   string
}

// runWave — same pacer as bench/identity-perf's loadgen. See that file for the
// rationale on absolute wallclock scheduling instead of time.Ticker.
func runWave(ctx context.Context, client *http.Client, p waveParams) {
	if p.qps <= 0 {
		log.Fatalf("qps must be positive (got %d)", p.qps)
	}
	tickets := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup
	for i := 0; i < p.concurrency; i++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
			buf := &bytes.Buffer{}
			for range tickets {
				fireOne(ctx, client, r, buf, p)
			}
		}(uint64(i) + 1)
	}
	interval := time.Second / time.Duration(p.qps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	start := time.Now()
	dropped := 0
	timer := time.NewTimer(0)
	defer timer.Stop()
loop:
	for i := int64(0); ; i++ {
		next := start.Add(time.Duration(i) * interval)
		delay := time.Until(next)
		if delay > 0 {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
			select {
			case <-ctx.Done():
				break loop
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			break loop
		}
		select {
		case tickets <- struct{}{}:
		default:
			dropped++
			runtime.Gosched()
		}
	}
	close(tickets)
	wg.Wait()
	if dropped > 0 {
		log.Printf("saturated: %d tickets dropped (concurrency=%d could not keep up with %d qps)", dropped, p.concurrency, p.qps)
	}
}

func fireOne(ctx context.Context, client *http.Client, r *rand.Rand, buf *bytes.Buffer, p waveParams) {
	req := buildRequest(r, p)
	var sig, kid string
	if p.signer != nil {
		sig = p.signer.SignContextMatch(req, p.providerEndpoint, tmproto.CurrentEpoch())
		kid = p.signer.KeyID
	}
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(req); err != nil {
		if p.samples != nil {
			p.samples <- sample{err: true}
		}
		return
	}
	body := bytes.NewReader(buf.Bytes())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.target, body)
	if err != nil {
		if p.samples != nil {
			p.samples <- sample{err: true}
		}
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if sig != "" {
		httpReq.Header.Set(tmproto.HeaderTMPSignature, sig)
		httpReq.Header.Set(tmproto.HeaderTMPKeyID, kid)
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	latency := time.Since(start)
	if err != nil {
		if p.samples != nil && ctx.Err() == nil {
			p.samples <- sample{latency: latency, err: true}
		}
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if p.samples != nil {
		p.samples <- sample{latency: latency, status: resp.StatusCode}
	}
}

func buildRequest(r *rand.Rand, p waveParams) *tmproto.ContextMatchRequest {
	propertyRID := p.propertyRIDs[r.IntN(len(p.propertyRIDs))]
	placement := corpus.PlacementIDs[r.IntN(len(corpus.PlacementIDs))]

	req := &tmproto.ContextMatchRequest{
		AdcpVersion:     p.adcpVersion,
		Type:            tmproto.TypeContextMatchRequest,
		ProtocolVersion: "1.0",
		RequestID:       fmt.Sprintf("req-%x-%x", r.Uint64(), r.Uint64()),
		PropertyRID:     propertyRID,
		PropertyID:      corpus.PropertyID,
		PropertyType:    tmproto.PropertyType(corpus.PropertyType),
		PlacementID:     placement,
		SellerAgentURL:  p.sellerAgent,
	}
	if p.packagesPerReq > 0 && p.totalPackages > 0 {
		ids := make([]string, 0, p.packagesPerReq)
		for j := 0; j < p.packagesPerReq; j++ {
			ids = append(ids, corpus.PackageID(r.IntN(p.totalPackages)))
		}
		req.PackageIDs = ids
	}
	if p.artifactRefsPerReq > 0 && p.totalArtifacts > 0 {
		refs := make([]tmproto.ArtifactRef, 0, p.artifactRefsPerReq)
		for j := 0; j < p.artifactRefsPerReq; j++ {
			// url_hash matches the signalstore key_type=url_hash side and
			// is a topic-eligible ref type the engine consults (see
			// engine.extractArtifactKeys).
			refs = append(refs, tmproto.ArtifactRef{
				Type:  tmproto.ArtifactRefTypeURLHash,
				Value: corpus.ArtifactURL(r.IntN(p.totalArtifacts)),
			})
		}
		req.ArtifactRefs = refs
	}
	return req
}

type report struct {
	Label              string        `json:"label,omitempty"`
	Target             string        `json:"target"`
	TargetQPS          int           `json:"target_qps"`
	Concurrency        int           `json:"concurrency"`
	PackagesPerReq     int           `json:"packages_per_req"`
	ArtifactRefsPerReq int           `json:"artifact_refs_per_req"`
	Signed             bool          `json:"signed"`
	WallSeconds        float64       `json:"wall_seconds"`
	Total              int64         `json:"total"`
	OK                 int64         `json:"ok_2xx"`
	Non2xx             int64         `json:"non_2xx"`
	Errors             int64         `json:"transport_errors"`
	AchievedQPS        float64       `json:"achieved_qps"`
	P50Ms              float64       `json:"p50_ms"`
	P90Ms              float64       `json:"p90_ms"`
	P99Ms              float64       `json:"p99_ms"`
	P999Ms             float64       `json:"p99_9_ms"`
	MaxMs              float64       `json:"max_ms"`
	MeanMs             float64       `json:"mean_ms"`
	StatusCodes        map[int]int64 `json:"status_codes"`
}

func collectStats(samples <-chan sample) report {
	var lats []time.Duration
	statuses := make(map[int]int64)
	var total, ok, non2xx, errs int64
	var sumLat time.Duration
	for s := range samples {
		total++
		if s.err {
			errs++
			continue
		}
		statuses[s.status]++
		if s.status >= 200 && s.status < 300 {
			ok++
		} else {
			non2xx++
		}
		lats = append(lats, s.latency)
		sumLat += s.latency
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		idx := int(p * float64(len(lats)))
		if idx >= len(lats) {
			idx = len(lats) - 1
		}
		return float64(lats[idx].Microseconds()) / 1000
	}
	rep := report{
		Total: total, OK: ok, Non2xx: non2xx, Errors: errs,
		StatusCodes: statuses,
		P50Ms:       pct(0.50), P90Ms: pct(0.90), P99Ms: pct(0.99), P999Ms: pct(0.999),
	}
	if len(lats) > 0 {
		rep.MaxMs = float64(lats[len(lats)-1].Microseconds()) / 1000
		rep.MeanMs = float64(sumLat.Microseconds()) / 1000 / float64(len(lats))
	}
	return rep
}

func (r report) print(w io.Writer) {
	fmt.Fprintf(w, "\n=== loadgen report ===\n")
	if r.Label != "" {
		fmt.Fprintf(w, "label:               %s\n", r.Label)
	}
	fmt.Fprintf(w, "target:              %s\n", r.Target)
	fmt.Fprintf(w, "target_qps:          %d\n", r.TargetQPS)
	fmt.Fprintf(w, "concurrency:         %d\n", r.Concurrency)
	fmt.Fprintf(w, "wall_seconds:        %.2f\n", r.WallSeconds)
	if r.WallSeconds > 0 {
		r.AchievedQPS = float64(r.Total) / r.WallSeconds
	}
	fmt.Fprintf(w, "achieved_qps:        %.1f\n", r.AchievedQPS)
	fmt.Fprintf(w, "total:               %d\n", r.Total)
	fmt.Fprintf(w, "ok_2xx:              %d\n", r.OK)
	fmt.Fprintf(w, "non_2xx:             %d\n", r.Non2xx)
	fmt.Fprintf(w, "transport_errors:    %d\n", r.Errors)
	fmt.Fprintf(w, "latency (ms):        p50=%.2f  p90=%.2f  p99=%.2f  p99.9=%.2f  max=%.2f  mean=%.3f\n",
		r.P50Ms, r.P90Ms, r.P99Ms, r.P999Ms, r.MaxMs, r.MeanMs)
	for code, n := range r.StatusCodes {
		fmt.Fprintf(w, "  status %d: %d\n", code, n)
	}
	fmt.Fprintf(w, "======================\n\n")
}

func writeJSON(path string, r report) error {
	if r.WallSeconds > 0 {
		r.AchievedQPS = float64(r.Total) / r.WallSeconds
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	// strconv.Atoi rejects trailing garbage ("1000x"); fmt.Sscanf would
	// silently accept the leading digits and mask the typo.
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("%s=%q is not an integer: %v", name, v, err)
	}
	return n
}

func envDur(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("%s=%q is not a duration: %v", name, v, err)
	}
	return d
}

func envBool(name string, def bool) bool {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	case "0", "f", "false", "n", "no", "off":
		return false
	default:
		log.Fatalf("%s=%q is not a boolean", name, v)
	}
	return def
}
