// Command verify drives the running stack through the router's public
// protocol surface and asserts the outcome of every hop.
//
// The router is the only thing verify talks to for protocol traffic. If an
// offer comes back, then the publisher→router request validated, the router
// resolved the property from its registry, signed a fan-out, the agent
// verified that signature against the key its keystore resolved, the agent
// read the seeded Valkey state, and the router merged the response — all of
// it, or the assertion fails.
//
// Two checks deliberately bypass the router: the direct unsigned probes
// against each agent, which prove signature enforcement is actually on rather
// than inferred from a passing signed request.
//
// Exit status is 0 only when every check passed.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/e2e/stack/internal/fixture"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

const (
	readinessTimeout = 90 * time.Second
	pollInterval     = 500 * time.Millisecond
	requestTimeout   = 15 * time.Second
)

func main() {
	client := &http.Client{Timeout: requestTimeout}
	r := &run{client: client}

	if err := r.awaitReadiness(); err != nil {
		log.Printf("FATAL readiness: %v", err)
		r.dumpDiagnostics()
		os.Exit(1)
	}

	// Fault counters are asserted as deltas from here, not as absolutes.
	// Startup legitimately produces provider errors: the router boots before
	// the agents, so its first fan-outs and health probes can miss. What must
	// not happen is a fault during the asserted scenarios.
	baseline, err := r.fetchMetrics()
	if err != nil {
		log.Printf("FATAL baseline metrics: %v", err)
		os.Exit(1)
	}
	r.baseline = baseline

	r.checkRouterSurface()
	r.checkStubsWerePolled()
	r.checkContextMatch()
	r.checkIdentityMatch()
	r.checkContextCache()
	r.checkSignatureEnforcement()
	r.checkRouterMetrics()

	fmt.Println()
	fmt.Printf("=== %d checks, %d failed ===\n", r.total, len(r.failures))
	if len(r.failures) == 0 {
		fmt.Println("PASS")
		return
	}
	for _, f := range r.failures {
		fmt.Printf("  FAIL %s\n", f)
	}
	fmt.Println("FAIL")
	os.Exit(1)
}

// --- check bookkeeping -------------------------------------------------------

type run struct {
	client *http.Client
	// baseline is the metrics snapshot taken once readiness converged.
	// Counters that must not move during the asserted scenarios are compared
	// against it rather than against zero.
	baseline metrics
	total    int
	failures []string
}

func (r *run) pass(name string) {
	r.total++
	fmt.Printf("  ok   %s\n", name)
}

func (r *run) fail(name string, format string, args ...any) {
	r.total++
	msg := fmt.Sprintf("%s: %s", name, fmt.Sprintf(format, args...))
	r.failures = append(r.failures, msg)
	fmt.Printf("  FAIL %s\n", msg)
}

func (r *run) check(name string, err error) bool {
	if err != nil {
		r.fail(name, "%v", err)
		return false
	}
	r.pass(name)
	return true
}

func (r *run) section(title string) {
	fmt.Println()
	fmt.Printf("--- %s\n", title)
}

// --- readiness ---------------------------------------------------------------

// awaitReadiness blocks until every asynchronous startup path the assertions
// depend on has actually converged:
//
//  1. the router serves /healthz;
//  2. the router's /registry/snapshot carries both the feed's properties and
//     the router's own signing key — the agents cannot verify a signature
//     until that merge has happened;
//  3. the router's health checker has seen both providers answer;
//  4. a context request through the whole chain returns the ungated offer,
//     which is the only proof that the context-agent's property bitmap has
//     finished hydrating from the feed.
//
// Step 4 polls on a fresh placement every attempt. The router caches provider
// responses per {property_rid, placement_id, provider_id, seller, country} and
// caches empty ones too, so a poll that lands before the bitmap is hydrated
// would otherwise pin an empty response for the cache TTL — first over a
// placement the assertions need, and then, with a fixed warmup placement, over
// the readiness probe's own key for the rest of its budget.
func (r *run) awaitReadiness() error {
	if err := r.await("router /healthz", func() error {
		_, err := r.getRaw(fixture.RouterBaseURL + "/healthz")
		return err
	}); err != nil {
		return err
	}
	if err := r.await("router /registry/snapshot merged", r.snapshotReady); err != nil {
		return err
	}
	if err := r.await("providers healthy", r.providersHealthy); err != nil {
		return err
	}
	warmAttempt := 0
	return r.await("context match warm", func() error {
		warmAttempt++
		req := contextRequest(fixture.NewsProperty(),
			fmt.Sprintf("%s-%d", placement(fixture.PlacementWarmup), warmAttempt))
		resp, err := r.postContext(req)
		if err != nil {
			return err
		}
		if !slices.Contains(offerIDs(resp), fixture.PackageContextOpen) {
			return fmt.Errorf("offers %v do not yet include %s", offerIDs(resp), fixture.PackageContextOpen)
		}
		return nil
	})
}

// dumpDiagnostics prints the router's own view of the world when readiness
// never converged. The router logs steady-state provider health-check failures
// at debug level and its log level is fixed at info, so the reason a provider
// is unreachable does not appear in its output — /providers plus the snapshot
// is the most an operator can see from outside.
//
// The commonest cause of every provider failing at once is the container subnet:
// the router refuses to dial an RFC 1918 address, so a stack brought up on a
// default Docker network cannot reach its own agents.
func (r *run) dumpDiagnostics() {
	for _, u := range []string{
		fixture.RouterAdminBaseURL + "/providers",
		fixture.RouterBaseURL + "/registry/snapshot",
	} {
		body, err := r.getRaw(u)
		if err != nil {
			log.Printf("diagnostics: %s: %v", u, err)
			continue
		}
		log.Printf("diagnostics: %s\n%s", u, body)
	}
	log.Printf("diagnostics: if every provider is unreachable, check the compose " +
		"network subnet — the router rejects RFC 1918 targets (see E2E_SUBNET)")
}

func (r *run) await(name string, probe func() error) error {
	deadline := time.Now().Add(readinessTimeout)
	var last error
	for time.Now().Before(deadline) {
		if last = probe(); last == nil {
			log.Printf("ready: %s", name)
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("%s not ready within %s: %w", name, readinessTimeout, last)
}

type registrySnapshot struct {
	Properties []struct {
		PropertyID  string               `json:"property_id"`
		PropertyRID string               `json:"property_rid"`
		Domain      string               `json:"domain"`
		SigningKeys []tmproto.SigningKey `json:"signing_keys"`
	} `json:"properties"`
}

func (r *run) fetchSnapshot() (*registrySnapshot, error) {
	body, err := r.getRaw(fixture.RouterBaseURL + "/registry/snapshot")
	if err != nil {
		return nil, err
	}
	var snap registrySnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	return &snap, nil
}

func (r *run) snapshotReady() error {
	snap, err := r.fetchSnapshot()
	if err != nil {
		return err
	}
	byRID := make(map[string]bool, len(snap.Properties))
	kidSeen := false
	for _, p := range snap.Properties {
		byRID[p.PropertyRID] = true
		for _, k := range p.SigningKeys {
			if k.Kid == fixture.RouterSigningKID {
				kidSeen = true
			}
		}
	}
	for _, want := range fixture.Properties {
		if !byRID[want.PropertyRID] {
			return fmt.Errorf("snapshot is missing property_rid %s", want.PropertyRID)
		}
	}
	if !kidSeen {
		return fmt.Errorf("snapshot carries no signing key with kid %s", fixture.RouterSigningKID)
	}
	return nil
}

type providerInfo struct {
	ID     string `json:"id"`
	Health struct {
		Successes   int64 `json:"successes"`
		CircuitOpen bool  `json:"circuit_open"`
	} `json:"health"`
}

func (r *run) fetchProviders() ([]providerInfo, error) {
	body, err := r.getRaw(fixture.RouterAdminBaseURL + "/providers")
	if err != nil {
		return nil, err
	}
	var out []providerInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse providers: %w", err)
	}
	return out, nil
}

// providersHealthy requires a positive success count, not merely a closed
// circuit: the circuit starts closed, so "not open" is also true for a
// provider the health checker has never reached.
func (r *run) providersHealthy() error {
	providers, err := r.fetchProviders()
	if err != nil {
		return err
	}
	byID := make(map[string]providerInfo, len(providers))
	for _, p := range providers {
		byID[p.ID] = p
	}
	for _, want := range []string{fixture.ContextProviderID, fixture.IdentityProviderID} {
		p, ok := byID[want]
		if !ok {
			return fmt.Errorf("provider %s is not registered", want)
		}
		if p.Health.CircuitOpen {
			return fmt.Errorf("provider %s has an open circuit", want)
		}
		if p.Health.Successes == 0 {
			return fmt.Errorf("provider %s has no successful health check yet", want)
		}
	}
	return nil
}

// --- router surface ----------------------------------------------------------

func (r *run) checkRouterSurface() {
	r.section("router surface")

	snap, err := r.fetchSnapshot()
	if !r.check("registry snapshot fetched", err) {
		return
	}

	// The snapshot proves the registry bridge did both halves of its job:
	// projected the stub feed's property metadata, and attached the router's
	// own signing key to the RIDs it is authorized to sign for.
	wantDomains := make(map[string]string, len(fixture.Properties))
	for _, p := range fixture.Properties {
		wantDomains[p.PropertyRID] = p.Domain
	}
	mismatched := 0
	for _, p := range snap.Properties {
		want, known := wantDomains[p.PropertyRID]
		if !known {
			continue
		}
		if p.Domain != want {
			mismatched++
			r.fail("snapshot property domain", "property_rid %s has domain %q, want %q",
				p.PropertyRID, p.Domain, want)
		}
		delete(wantDomains, p.PropertyRID)
	}
	if len(wantDomains) > 0 {
		r.fail("snapshot property coverage", "properties absent from snapshot: %v", slices.Sorted(maps.Keys(wantDomains)))
	} else if mismatched == 0 {
		r.pass("snapshot carries every feed property with its domain")
	}

	// bootstrap authorizes the router to sign for every fixture property, so
	// the key has to appear on every one of them. Accepting "at least one"
	// would miss a regression that attaches the key to only the first record:
	// the sole snapshot-keystore consumer here matches on kid alone, so it
	// would keep verifying while a provider scoped to a different property
	// silently lost its trust anchor.
	signedRIDs := 0
	for _, p := range snap.Properties {
		for _, k := range p.SigningKeys {
			if k.Kid == fixture.RouterSigningKID {
				signedRIDs++
			}
		}
	}
	if signedRIDs != len(fixture.Properties) {
		r.fail("snapshot signing key", "kid %s is on %d properties, want all %d",
			fixture.RouterSigningKID, signedRIDs, len(fixture.Properties))
	} else {
		r.pass(fmt.Sprintf("snapshot publishes kid %s on all %d properties",
			fixture.RouterSigningKID, len(fixture.Properties)))
	}

	providers, err := r.fetchProviders()
	if !r.check("provider list fetched", err) {
		return
	}
	if len(providers) != 2 {
		r.fail("provider count", "want 2 providers, got %d", len(providers))
	} else {
		r.pass("router registered both providers")
	}

}

// checkStubsWerePolled reads each stub's call counter. Without this, a stack
// whose images happened to carry the fixture data baked in — or one where an
// agent silently fell back to a static config — would satisfy every offer
// assertion while never touching the external systems the stubs stand for.
//
// Two of the three counters have exactly one consumer, so they attribute
// precisely: only the context-agent looks up authorization rows, and only the
// identity-agent polls the config source. The feed counter is shared by the
// router and the context-agent and so proves only that the feed was consumed at
// all; the context-agent's own sync is what the property-bitmap scenarios in
// checkContextMatch depend on, and they fail without it.
func (r *run) checkStubsWerePolled() {
	for _, want := range []struct {
		name    string
		url     string
		counter string
	}{
		{"registry feed was consumed", fixture.RegistryStubBaseURL + "/stats", "feed_polls"},
		{"context-agent looked up authorization rows", fixture.RegistryStubBaseURL + "/stats", "authorization_lookups"},
		{"identity-agent polled the config source", fixture.ConfigStubBaseURL + "/stats", "config_polls"},
	} {
		body, err := r.getRaw(want.url)
		if err != nil {
			r.fail(want.name, "%v", err)
			continue
		}
		var stats map[string]int64
		if err := json.Unmarshal(body, &stats); err != nil {
			r.fail(want.name, "parse %s: %v", want.url, err)
			continue
		}
		if n := stats[want.counter]; n <= 0 {
			r.fail(want.name, "%s is %d, want > 0", want.counter, n)
			continue
		}
		r.pass(fmt.Sprintf("%s (%s = %d)", want.name, want.counter, stats[want.counter]))
	}
}

// --- context match -----------------------------------------------------------

func (r *run) checkContextMatch() {
	r.section("context match")

	news := fixture.NewsProperty()

	// The matched artifact carries the topic the topic-gated package targets
	// and the URL the seeded signal keys on, so all three ungated-or-matched
	// packages offer. The video-only package must not: its config scopes it
	// to the CTV property's RID.
	r.expectContextOffers("matched artifact activates topic and signal gates",
		withArtifacts(contextRequest(news, placement(fixture.PlacementMatchedArtifact)), fixture.ArtifactMatched),
		fixture.PackageContextOpen, fixture.PackageContextTopic, fixture.PackageContextSignal)

	// Same property, an artifact with a non-overlapping topic and no signal
	// key. Both gates now fail, and both fail closed rather than defaulting
	// open.
	r.expectContextOffers("unmatched artifact closes topic and signal gates",
		withArtifacts(contextRequest(news, placement(fixture.PlacementUnmatchedArtifact)), fixture.ArtifactUnmatched),
		fixture.PackageContextOpen)

	// No artifact_refs at all. The two gates diverge here, and the divergence
	// is intentional in the engine: a request that discloses no topics passes
	// the topic gate vacuously, while a signal cfg with nothing to key on
	// fails closed. Asserting both directions pins the asymmetry so a change
	// to either one is visible.
	r.expectContextOffers("absent artifact_refs: topic gate passes vacuously, signal gate fails closed",
		contextRequest(news, placement(fixture.PlacementNoArtifact)),
		fixture.PackageContextOpen, fixture.PackageContextTopic)

	// The CTV property is where the property-scoped package belongs.
	video := fixture.VideoProperty()
	r.expectContextOffers("property-scoped package offers on its own property",
		contextRequest(video, placement(fixture.PlacementPreroll)),
		fixture.PackageContextOpen, fixture.PackageContextTopic, fixture.PackageContextVideoOnly)

	// Property-level kill switch. The property is in the registry and has
	// active packages; only the suppression marker stands between it and a
	// full offer list.
	r.expectNoOffers("suppressed property returns no offers",
		contextRequest(fixture.ShutteredProperty(), placement(fixture.PlacementMatchedArtifact)))

	// A property_rid the feed never published. The agent's property bitmap
	// short-circuits it. property_id is deliberately omitted: the router
	// resolves rid from slug when both are present, which would rewrite the
	// unregistered value before the agent ever saw it.
	unregistered := contextRequest(news, placement(fixture.PlacementMatchedArtifact))
	unregistered.PropertyID = ""
	unregistered.PropertyRID = fixture.UnregisteredPropertyRID
	r.expectNoOffers("unregistered property returns no offers", unregistered)

	// property_id only. The router's registry enrichment has to supply the
	// wire-required property_rid before validation, or this request never
	// reaches a provider.
	slugOnly := contextRequest(news, placement(fixture.PlacementSlugOnly))
	slugOnly.PropertyRID = ""
	r.expectContextOffers("router resolves property_rid from property_id",
		slugOnly, fixture.PackageContextOpen, fixture.PackageContextTopic)
}

// expectNoOffers asserts an empty offer list AND that the context provider was
// actually dialed for it. An empty list on its own is ambiguous: the router
// returns one just as readily when it excluded the provider from the fan-out,
// so a suppression or property-bitmap scenario would pass on a stack where the
// agent was never consulted at all.
func (r *run) expectNoOffers(name string, req *tmproto.ContextMatchRequest) {
	before, err := r.fetchMetrics()
	if err != nil {
		r.fail(name, "metrics snapshot: %v", err)
		return
	}
	if !r.expectContextOffers(name, req) {
		return
	}
	after, err := r.fetchMetrics()
	if err != nil {
		r.fail(name+" reached the provider", "metrics snapshot: %v", err)
		return
	}
	d, ok := after.delta(before, "tmp_provider_duration_ms_count", fixture.ContextProviderID)
	switch {
	case !ok:
		r.fail(name+" reached the provider",
			"tmp_provider_duration_ms_count{%s} matched more than one series", fixture.ContextProviderID)
		return
	case d < 1:
		r.fail(name+" reached the provider",
			"tmp_provider_duration_ms_count{%s} rose by %g, want >= 1 — the empty offer list did not come from the agent",
			fixture.ContextProviderID, d)
		return
	}
	r.pass(name + " reached the provider")
}

// expectContextOffers reports whether the assertion passed, so callers that
// layer a second check on the same request can skip it once the first failed.
func (r *run) expectContextOffers(name string, req *tmproto.ContextMatchRequest, wantPackages ...string) bool {
	resp, err := r.postContext(req)
	if err != nil {
		r.fail(name, "%v", err)
		return false
	}
	if resp.Type != tmproto.TypeContextMatchResponse {
		r.fail(name, "response type %q, want %q", resp.Type, tmproto.TypeContextMatchResponse)
		return false
	}
	if resp.RequestID != req.RequestID {
		r.fail(name, "response echoed request_id %q, want %q", resp.RequestID, req.RequestID)
		return false
	}
	got := offerIDs(resp)
	want := append([]string(nil), wantPackages...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		r.fail(name, "offers %v, want %v", got, want)
		return false
	}
	// The summary comes from the seeded package config, so matching it proves
	// the offer body travelled from Valkey through the agent and the router's
	// merge rather than being synthesized anywhere along the way.
	for _, offer := range resp.Offers {
		if wantSummary := fixture.OfferSummary(offer.PackageID); offer.Summary != wantSummary {
			r.fail(name, "offer %s summary %q, want %q", offer.PackageID, offer.Summary, wantSummary)
			return false
		}
	}
	r.pass(name)
	return true
}

// --- identity match ----------------------------------------------------------

func (r *run) checkIdentityMatch() {
	r.section("identity match")

	// The segmented user is in the audience the audience-gated package
	// targets, and carries the frequency-cap marker on the capped package.
	r.expectEligible("segmented user: audience passes, frequency cap excludes",
		identityRequest(fixture.UserSegmented(), fixture.IdentityPackages),
		fixture.PackageIdentityOpen, fixture.PackageIdentityAudience)

	// The anonymous user is the mirror image: no segment membership, no cap
	// marker. Neither package resolves the same way as for the first user,
	// which is what rules out a hard-coded response.
	r.expectEligible("anonymous user: audience excludes, no frequency cap",
		identityRequest(fixture.UserAnonymous(), fixture.IdentityPackages),
		fixture.PackageIdentityOpen, fixture.PackageIdentityCapped)

	// Omitting package_ids makes the agent resolve the seller's whole
	// package set out of the config snapshot it polled from the stub.
	r.expectEligible("absent package_ids resolves the seller's package set",
		identityRequest(fixture.UserSegmented(), nil),
		fixture.PackageIdentityOpen, fixture.PackageIdentityAudience)
}

func (r *run) expectEligible(name string, req *tmproto.IdentityMatchRequest, wantPackages ...string) {
	resp, err := r.postIdentity(req)
	if err != nil {
		r.fail(name, "%v", err)
		return
	}
	if resp.Type != tmproto.TypeIdentityMatchResponse {
		r.fail(name, "response type %q, want %q", resp.Type, tmproto.TypeIdentityMatchResponse)
		return
	}
	if resp.RequestID != req.RequestID {
		r.fail(name, "response echoed request_id %q, want %q", resp.RequestID, req.RequestID)
		return
	}
	got := append([]string(nil), resp.EligiblePackageIDs...)
	slices.Sort(got)
	want := append([]string(nil), wantPackages...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		r.fail(name, "eligible_package_ids %v, want %v", got, want)
		return
	}
	// The schema bounds serve_window_sec to [1, 300] and the router clamps
	// the merged value into that range, so anything outside it is a bug in
	// the merge rather than a provider quirk.
	if resp.ServeWindowSec < 1 || resp.ServeWindowSec > 300 {
		r.fail(name, "serve_window_sec %d outside [1, 300]", resp.ServeWindowSec)
		return
	}
	r.pass(name)
}

// --- router response cache ---------------------------------------------------

// checkContextCache proves the router's Context Match cache is wired: two
// identical requests on a placement no other check touches must produce one
// miss and one hit, with the same offers both times.
func (r *run) checkContextCache() {
	r.section("router context cache")

	before, err := r.fetchMetrics()
	if !r.check("metrics snapshot before cache probe", err) {
		return
	}

	// The first request has to be pinned to the expected offer set, not merely
	// compared against the second. The router caches empty responses too, so
	// an agent returning zero offers here would satisfy a first-equals-second
	// check — and the miss and hit counters would both still move.
	cacheReq := func() *tmproto.ContextMatchRequest {
		return withArtifacts(contextRequest(fixture.NewsProperty(), placement(fixture.PlacementCache)),
			fixture.ArtifactMatched)
	}
	wantOffers := []string{fixture.PackageContextOpen, fixture.PackageContextTopic, fixture.PackageContextSignal}
	if !r.expectContextOffers("first cache-probe request fans out", cacheReq(), wantOffers...) {
		return
	}

	// A fresh request_id on the second call: the cached entry is keyed on
	// property/placement/provider/seller/country, and the router overwrites
	// request_id from the live request when it serves a hit. Reusing the id
	// would hide a bug where it does not.
	if !r.expectContextOffers("cached response matches fresh response", cacheReq(), wantOffers...) {
		return
	}

	after, err := r.fetchMetrics()
	if !r.check("metrics snapshot after cache probe", err) {
		return
	}

	for _, want := range []struct {
		name   string
		series string
	}{
		{"cache recorded a miss", "tmp_context_cache_misses_total"},
		{"cache recorded a hit", "tmp_context_cache_hits_total"},
	} {
		d, ok := after.delta(before, want.series, fixture.ContextProviderID)
		switch {
		case !ok:
			r.fail(want.name, "%s{%s} matched more than one series", want.series, fixture.ContextProviderID)
		case d < 1:
			r.fail(want.name, "%s{%s} rose by %g, want >= 1", want.series, fixture.ContextProviderID, d)
		default:
			r.pass(want.name)
		}
	}
}

// --- signature enforcement ---------------------------------------------------

// checkSignatureEnforcement talks to the agents directly, without the
// router's signature. Both must refuse. Without this, every passing check
// above would also pass on a stack where signature verification was
// accidentally disabled.
func (r *run) checkSignatureEnforcement() {
	r.section("signature enforcement")

	ctxReq := withArtifacts(contextRequest(fixture.NewsProperty(), placement(fixture.PlacementMatchedArtifact)), fixture.ArtifactMatched)
	r.expectUnauthorized("context-agent rejects an unsigned request",
		fixture.ContextAgentEndpoint+"/context", ctxReq)

	idReq := identityRequest(fixture.UserSegmented(), fixture.IdentityPackages)
	// The router strips country before forwarding; it is a routing directive
	// and is not part of the signing input.
	idReq.Country = ""
	r.expectUnauthorized("identity-agent rejects an unsigned request",
		fixture.IdentityAgentEndpoint+"/identity", idReq)
}

func (r *run) expectUnauthorized(name, url string, payload any) {
	status, body, err := r.post(url, payload)
	if err != nil {
		r.fail(name, "%v", err)
		return
	}
	if status != http.StatusUnauthorized {
		r.fail(name, "status %d (%s), want 401", status, firstLine(body))
		return
	}
	r.pass(name)
}

// --- metrics -----------------------------------------------------------------

func (r *run) checkRouterMetrics() {
	r.section("router metrics")

	m, err := r.fetchMetrics()
	if !r.check("metrics scraped", err) {
		return
	}

	for _, want := range []struct {
		name   string
		labels []string
	}{
		{"router_requests_total", []string{"context"}},
		{"router_requests_total", []string{"identity"}},
		{"tmp_offers_total", nil},
		{"tmp_provider_duration_ms_count", []string{fixture.ContextProviderID}},
		{"tmp_provider_duration_ms_count", []string{fixture.IdentityProviderID}},
	} {
		label := want.name
		if len(want.labels) > 0 {
			label = fmt.Sprintf("%s{%s}", want.name, strings.Join(want.labels, ","))
		}
		v, ok := m.value(want.name, want.labels...)
		switch {
		case !ok:
			r.fail("metric present", "%s is absent", label)
		case v <= 0:
			r.fail("metric positive", "%s is %g, want > 0", label, v)
		default:
			r.pass(fmt.Sprintf("%s = %g", label, v))
		}
	}

	// A provider timeout or transport error during the asserted scenarios
	// means the fan-out was flaky even if the merged offers happened to come
	// out right — a retry-shaped bug that a pass/fail on offers alone hides.
	for _, name := range []string{"tmp_provider_timeout_total", "tmp_provider_error_total"} {
		for _, provider := range []string{fixture.ContextProviderID, fixture.IdentityProviderID} {
			d, ok := m.delta(r.baseline, name, provider)
			switch {
			case !ok:
				r.fail("no provider faults", "%s{%s} matched more than one series", name, provider)
			case d > 0:
				r.fail("no provider faults", "%s{%s} rose by %g during the run, want 0", name, provider, d)
			default:
				r.pass(fmt.Sprintf("%s{%s} did not rise", name, provider))
			}
		}
	}

	for _, provider := range []string{fixture.ContextProviderID, fixture.IdentityProviderID} {
		if v, ok := m.value("tmp_provider_health_status", provider); !ok || v != 1 {
			r.fail("provider health gauge", "tmp_provider_health_status{%s} is %g (present=%t), want 1",
				provider, v, ok)
		} else {
			r.pass(fmt.Sprintf("tmp_provider_health_status{%s} = 1", provider))
		}
	}
}

// metrics is a parsed Prometheus text exposition: series key → value, where
// the key is the raw "name{labels}" text the exposition emitted.
type metrics map[string]float64

// value looks a series up by metric name and label values. Label names are not
// reconstructed — the router owns a fixed label set per series, so matching on
// the quoted values is enough and keeps this parser small.
//
// The match count callers get back from lookup is what matters: 0 means the
// series has not been emitted yet (legitimate for a counter still at zero),
// while >1 means the lookup is ambiguous and any single value would be an
// arbitrary pick out of a randomly ordered map.
func (m metrics) lookup(name string, labelValues ...string) (float64, int) {
	var found float64
	matches := 0
	for key, v := range m {
		if !strings.HasPrefix(key, name) {
			continue
		}
		rest := key[len(name):]
		if len(labelValues) == 0 {
			if rest != "" {
				continue
			}
		} else {
			if !strings.HasPrefix(rest, "{") {
				continue
			}
			if !containsAllLabels(rest, labelValues) {
				continue
			}
		}
		found = v
		matches++
	}
	return found, matches
}

// value returns the single series matching name and labels. Absent and
// ambiguous both report false — a caller of value wants one definite series.
func (m metrics) value(name string, labelValues ...string) (float64, bool) {
	v, matches := m.lookup(name, labelValues...)
	return v, matches == 1
}

func containsAllLabels(labelText string, values []string) bool {
	for _, v := range values {
		if !strings.Contains(labelText, `"`+v+`"`) {
			return false
		}
	}
	return true
}

// delta returns the movement in a series between two snapshots. A series absent
// from both is zero movement, which is the normal state of a counter that never
// fired. An ambiguous match on either side reports ok=false: silently returning
// zero there would turn "this counter must not move" into a check that passes
// precisely when it can no longer be evaluated.
func (m metrics) delta(before metrics, name string, labelValues ...string) (float64, bool) {
	now, nowMatches := m.lookup(name, labelValues...)
	then, thenMatches := before.lookup(name, labelValues...)
	if nowMatches > 1 || thenMatches > 1 {
		return 0, false
	}
	return now - then, true
}

func (r *run) fetchMetrics() (metrics, error) {
	body, err := r.getRaw(fixture.RouterAdminBaseURL + "/metrics")
	if err != nil {
		return nil, err
	}
	out := metrics{}
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.LastIndex(line, " ")
		if idx < 0 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[idx+1:]), 64)
		if err != nil {
			continue
		}
		out[strings.TrimSpace(line[:idx])] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no series parsed from /metrics")
	}
	return out, nil
}

// --- request builders --------------------------------------------------------

var requestSeq int

// nextRequestID returns a fresh, ordered request id. Ordering makes a router
// log easy to line up against this program's output.
func nextRequestID(kind string) string {
	requestSeq++
	return fmt.Sprintf("e2e-%s-%03d", kind, requestSeq)
}

// runNonce scopes every placement this process sends to this process.
//
// The router's Context Match cache lives for the router's lifetime, and its
// key includes placement_id. Without a per-run suffix, a second verify run
// against a router that is already warm — which is exactly what
// `KEEP_STACK=1` invites — would be served from the cache: the context
// assertions would pass without reaching an agent, and the cache scenario's
// first request would be a hit rather than the miss it asserts.
var runNonce = time.Now().UnixNano()

// placement returns the run-scoped form of one of the fixture's placement ids.
// Placement ids are not validated anywhere in the chain — a seeded
// MediaBuyPackage leaves PlacementIDs empty, which the engine reads as "any
// placement" — so the suffix changes cache keys and nothing else.
func placement(base string) string {
	return fmt.Sprintf("%s-%d", base, runNonce)
}

func contextRequest(p fixture.Property, placement string) *tmproto.ContextMatchRequest {
	return &tmproto.ContextMatchRequest{
		AdcpVersion:     "3.1",
		Type:            tmproto.TypeContextMatchRequest,
		ProtocolVersion: "1.0",
		RequestID:       nextRequestID("ctx"),
		PropertyRID:     p.PropertyRID,
		PropertyID:      p.PropertyID,
		PropertyType:    p.PropertyType,
		PlacementID:     placement,
		SellerAgentURL:  fixture.SellerAgentURL,
	}
}

func withArtifacts(req *tmproto.ContextMatchRequest, urls ...string) *tmproto.ContextMatchRequest {
	refs := make([]tmproto.ArtifactRef, 0, len(urls))
	for _, u := range urls {
		// Type `url` with the raw URL, which is what a publisher sends. The
		// engine feeds it to the topic index verbatim and to the signal
		// keyspace canonicalized-and-hashed; the seeder keys each store the
		// same way, so both derivations are under test.
		refs = append(refs, tmproto.ArtifactRef{
			Type:  tmproto.ArtifactRefTypeURL,
			Value: u,
		})
	}
	req.ArtifactRefs = refs
	return req
}

func identityRequest(userToken string, packageIDs []string) *tmproto.IdentityMatchRequest {
	return &tmproto.IdentityMatchRequest{
		AdcpVersion:     "3.1",
		Type:            tmproto.TypeIdentityMatchRequest,
		ProtocolVersion: "1.0",
		RequestID:       nextRequestID("id"),
		SellerAgentURL:  fixture.SellerAgentURL,
		Country:         fixture.Country,
		Identities: []tmproto.IdentityToken{{
			UIDType:   fixture.UIDType,
			UserToken: userToken,
		}},
		PackageIDs: packageIDs,
	}
}

// --- HTTP helpers ------------------------------------------------------------

func (r *run) postContext(req *tmproto.ContextMatchRequest) (*tmproto.ContextMatchResponse, error) {
	status, body, err := r.post(fixture.RouterBaseURL+"/tmp/context", req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("POST /tmp/context returned %d: %s", status, firstLine(body))
	}
	var out tmproto.ContextMatchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse context response: %w", err)
	}
	return &out, nil
}

func (r *run) postIdentity(req *tmproto.IdentityMatchRequest) (*tmproto.IdentityMatchResponse, error) {
	status, body, err := r.post(fixture.RouterBaseURL+"/tmp/identity", req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("POST /tmp/identity returned %d: %s", status, firstLine(body))
	}
	var out tmproto.IdentityMatchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse identity response: %w", err)
	}
	return &out, nil
}

func (r *run) post(url string, payload any) (int, []byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := r.client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return 0, nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read %s response: %w", url, err)
	}
	return resp.StatusCode, body, nil
}

func (r *run) getRaw(url string) ([]byte, error) {
	resp, err := r.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, firstLine(body))
	}
	return body, nil
}

// --- small helpers -----------------------------------------------------------

func offerIDs(resp *tmproto.ContextMatchResponse) []string {
	out := make([]string, 0, len(resp.Offers))
	for _, o := range resp.Offers {
		out = append(out, o.PackageID)
	}
	slices.Sort(out)
	return out
}

func firstLine(body []byte) string {
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
