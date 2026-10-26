package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

type trustServer struct {
	mu      sync.Mutex
	docs    map[string]*TrustResponse
	errs    map[string]error
	block   map[string]chan struct{}
	fetches map[string]int
}

func newTrustServer() *trustServer {
	return &trustServer{docs: map[string]*TrustResponse{}, errs: map[string]error{}, block: map[string]chan struct{}{}, fetches: map[string]int{}}
}

func (s *trustServer) set(url string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[url] = &TrustResponse{Status: status, Body: []byte(body)}
	delete(s.errs, url)
}

func (s *trustServer) fail(url string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs[url] = err
}

func (s *trustServer) count(url string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches[url]
}

func (s *trustServer) fetch(ctx context.Context, url string) (*TrustResponse, error) {
	s.mu.Lock()
	s.fetches[url]++
	block, err, doc := s.block[url], s.errs[url], s.docs[url]
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return &TrustResponse{Status: http.StatusNotFound}, nil
	}
	return doc, nil
}

func trustURL(domain string) string { return "https://" + domain + "/.well-known/trust.json" }

type evalRig struct {
	now    time.Time
	server *trustServer
	eval   *TrustRecordEvaluator
}

func newEvalRig(t *testing.T) *evalRig {
	t.Helper()
	r := &evalRig{now: testNow, server: newTrustServer()}
	var err error
	r.eval, err = NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{Fetch: r.server.fetch, Clock: func() time.Time { return r.now }, PollingInterval: time.Minute})
	require.NoError(t, err)
	return r
}

const buyerRecord = `{"agents":[{"url":"https://buyer.example.com/mcp","roles":["adcp:buying"]}]}`

func TestTrustRecordURL(t *testing.T) {
	u, err := TrustRecordURL("https://agent.buyer.example.co.uk:8443/mcp")
	require.NoError(t, err)
	require.Equal(t, "https://example.co.uk/.well-known/trust.json", u)
	for _, bad := range []string{"https://192.0.2.1", "https://co.uk", "https://buyer.example", "https://localhost"} {
		_, err := TrustRecordURL(bad)
		require.Error(t, err, bad)
	}
	_, err = NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{PollingInterval: time.Hour})
	require.Error(t, err, "the cache bound may not exceed the 30 min polling ceiling")
}

func TestEvaluateRequiresExactlyOneAgentWithRole(t *testing.T) {
	r := newEvalRig(t)
	ctx := context.Background()
	r.server.set(trustURL("example.com"), 200, buyerRecord)
	rec, err := r.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.NoError(t, err)
	require.Equal(t, "example.com", rec.OperatorDomain)
	_, err = r.eval.Evaluate(ctx, "https://buyer.example.com", RoleSales)
	require.Error(t, err)
	_, err = r.eval.Evaluate(ctx, "https://other.example.com", RoleBuying)
	require.Error(t, err)
	require.Equal(t, 1, r.server.count(trustURL("example.com")), "one fetch serves every origin of the operator")

	// The returned record is a copy: mutating it cannot change authority.
	rec.Record.Agents[0].Roles = []string{RoleSales}
	_, err = r.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.NoError(t, err)
}

func TestEvaluateAuthoritativeLocation(t *testing.T) {
	r := newEvalRig(t)
	ctx := context.Background()
	hosted := "https://records.host.example.net/brand/trust.json"
	r.server.set(trustURL("example.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
	r.server.set(hosted, 200, buyerRecord)
	rec, err := r.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.NoError(t, err)
	require.Equal(t, "example.com", rec.OperatorDomain, "the operator is never the hop target's host")
	require.Equal(t, hosted, rec.SourceURL)

	// A hosted record cannot make another domain's agents this operator's.
	r2 := newEvalRig(t)
	r2.server.set(trustURL("example.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
	r2.server.set(hosted, 200, `{"agents":[{"url":"https://agent.host.example.net/mcp","roles":["adcp:buying"]}]}`)
	_, err = r2.eval.Evaluate(ctx, "https://buyer.example.com", "")
	require.Error(t, err)

	cases := map[string]func(s *trustServer){
		"second hop": func(s *trustServer) {
			s.set(trustURL("example.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
			s.set(hosted, 200, `{"authoritative_location":"https://third.example.org/trust.json"}`)
		},
		"HTTP redirect is not followed": func(s *trustServer) {
			s.set(trustURL("example.com"), http.StatusFound, ``)
		},
		"pointer to a private address": func(s *trustServer) {
			s.set(trustURL("example.com"), 200, `{"authoritative_location":"https://10.0.0.5/trust.json"}`)
		},
		"pointer with userinfo": func(s *trustServer) {
			s.set(trustURL("example.com"), 200, `{"authoritative_location":"https://user@hosted.example.net/trust.json"}`)
		},
		"strict JSON": func(s *trustServer) {
			s.set(trustURL("example.com"), 200, `{"agents":[],"agents":[]}`)
		},
		"oversized": func(s *trustServer) {
			s.set(trustURL("example.com"), 200, `{"grants":[],"ext":{"v":{"p":"`+strings.Repeat("x", TrustRecordMaxBytes)+`"}}}`)
		},
		"server error": func(s *trustServer) { s.set(trustURL("example.com"), 503, ``) },
		"transport error": func(s *trustServer) {
			s.fail(trustURL("example.com"), errors.New("connection refused"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newEvalRig(t)
			setup(r.server)
			_, err := r.eval.Evaluate(ctx, "https://buyer.example.com", "")
			require.Error(t, err)
			require.False(t, IsTrustRecordAbsent(err), "a failure is never absence")
		})
	}
}

func TestEvaluateAbsenceAndNoDowngrade(t *testing.T) {
	r := newEvalRig(t)
	ctx := context.Background()
	_, err := r.eval.Evaluate(ctx, "brand-7k3q.com", "")
	require.True(t, IsTrustRecordAbsent(err), "404 before any 200 is absence")
	r.server.set(trustURL("brand-7k3q.com"), http.StatusGone, "")
	r.now = r.now.Add(61 * time.Second)
	_, err = r.eval.Evaluate(ctx, "brand-7k3q.com", "")
	require.True(t, IsTrustRecordAbsent(err), "410 too")

	r.server.set(trustURL("brand-7k3q.com"), 200, `{"grants":[]}`)
	r.now = r.now.Add(61 * time.Second)
	_, err = r.eval.Evaluate(ctx, "brand-7k3q.com", "")
	require.NoError(t, err)
	// After a 200 the record disappears: a discovery failure, not absence
	// (so no fallback to brand.json) and not a removal. The last record
	// read stays authoritative.
	r.server.set(trustURL("brand-7k3q.com"), http.StatusNotFound, "")
	r.now = r.now.Add(2 * time.Minute)
	_, err = r.eval.Evaluate(ctx, "brand-7k3q.com", "")
	require.True(t, IsTrustRecordUnavailable(err), "%v", err)
	require.False(t, IsTrustRecordAbsent(err))

	// A hop target that is gone is a discovery failure too.
	hosted := "https://records.host.example.net/brand/trust.json"
	r2 := newEvalRig(t)
	r2.server.set(trustURL("example.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
	_, err = r2.eval.Evaluate(ctx, "example.com", "")
	require.True(t, IsTrustRecordUnavailable(err), "%v", err)
}

func TestEvaluateFreshnessAndNegativeCache(t *testing.T) {
	r := newEvalRig(t)
	ctx := context.Background()
	u := trustURL("example.com")
	r.server.set(u, 200, buyerRecord)
	rec, err := r.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.NoError(t, err)
	require.Equal(t, r.now.Add(time.Minute), rec.ExpiresAt, "cached no longer than the polling interval")
	r.now = r.now.Add(30 * time.Second)
	_, _ = r.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.Equal(t, 1, r.server.count(u))

	// Delisting ends authority within one cache lifetime.
	r.server.set(u, 200, `{"agents":[{"url":"https://other.example.com","roles":["adcp:buying"]}]}`)
	r.now = r.now.Add(31 * time.Second)
	_, err = r.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.Error(t, err)

	// A failure is negatively cached for at most 60 s.
	r2 := newEvalRig(t)
	r2.server.fail(u, errors.New("down"))
	_, err = r2.eval.Evaluate(ctx, "https://buyer.example.com", "")
	require.Error(t, err)
	_, err = r2.eval.Evaluate(ctx, "https://buyer.example.com", "")
	require.Error(t, err)
	require.Equal(t, 1, r2.server.count(u))
	r2.server.set(u, 200, buyerRecord)
	r2.now = r2.now.Add(61 * time.Second)
	_, err = r2.eval.Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.NoError(t, err)
}

func TestEvaluateRecordExpiringDuringFetchGrantsNothing(t *testing.T) {
	r := newEvalRig(t)
	u := trustURL("example.com")
	r.server.set(u, 200, buyerRecord)
	release := make(chan struct{})
	r.server.block[u] = release
	done := make(chan error)
	go func() {
		_, err := r.eval.Evaluate(context.Background(), "https://buyer.example.com", RoleBuying)
		done <- err
	}()
	require.Eventually(t, func() bool { return r.server.count(u) == 1 }, time.Second, time.Millisecond)
	r.eval.mu.Lock()
	r.now = r.now.Add(2 * time.Minute) // the fetch outlives the record's lifetime
	r.eval.mu.Unlock()
	close(release)
	require.Error(t, <-done)
}

func TestEvaluateSingleFlightAndIsolation(t *testing.T) {
	r := newEvalRig(t)
	slow, fast := trustURL("slow-7k3q.com"), trustURL("fast-7k3q.com")
	r.server.set(slow, 200, `{"agents":[{"url":"https://a.slow-7k3q.com","roles":["adcp:buying"]}]}`)
	r.server.set(fast, 200, `{"agents":[{"url":"https://a.fast-7k3q.com","roles":["adcp:buying"]}]}`)
	_, err := r.eval.Evaluate(context.Background(), "https://a.fast-7k3q.com", RoleBuying)
	require.NoError(t, err)
	release := make(chan struct{})
	r.server.block[slow] = release
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.eval.Evaluate(context.Background(), "https://a.slow-7k3q.com", RoleBuying)
			require.NoError(t, err)
		}()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.eval.Evaluate(ctx, "https://a.slow-7k3q.com", RoleBuying)
	require.ErrorIs(t, err, context.Canceled, "a waiter that gives up does not cancel the shared fetch")
	// One slow operator does not block another operator's cached record.
	_, err = r.eval.Evaluate(context.Background(), "https://a.fast-7k3q.com", RoleBuying)
	require.NoError(t, err)
	close(release)
	wg.Wait()
	require.Equal(t, 1, r.server.count(slow))
}

func TestEvaluateSharedHopCannotExtendFreshness(t *testing.T) {
	r := newEvalRig(t)
	hosted := "https://records.host.example.net/shared/trust.json"
	r.server.set(trustURL("one-7k3q.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
	r.server.set(trustURL("two-7k3q.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
	r.server.set(hosted, 200, `{"grants":[]}`)
	a, err := r.eval.Evaluate(context.Background(), "one-7k3q.com", "")
	require.NoError(t, err)
	r.now = r.now.Add(40 * time.Second)
	b, err := r.eval.Evaluate(context.Background(), "two-7k3q.com", "")
	require.NoError(t, err)
	require.Equal(t, 1, r.server.count(hosted), "hop bytes are shared")
	require.Equal(t, "two-7k3q.com", b.OperatorDomain, "authority is not shared")
	require.Equal(t, a.ExpiresAt, b.ExpiresAt, "a cached hop response bounds the later record's freshness")
}

func TestEvaluateHostFailureCooldownIsNotAbsence(t *testing.T) {
	r := newEvalRig(t)
	hosted := "https://records.host.example.net/a/trust.json"
	r.server.set(trustURL("one-7k3q.com"), 200, `{"authoritative_location":"`+hosted+`"}`)
	r.server.set(hosted, 503, ``)
	_, err := r.eval.Evaluate(context.Background(), "one-7k3q.com", "")
	require.Error(t, err)
	// Another operator hosted on the same host, whose own derived URL 404s
	// on the first try, still gets "absent" only from its own URL.
	_, err = r.eval.Evaluate(context.Background(), "two-7k3q.com", "")
	require.True(t, IsTrustRecordAbsent(err))
	// But a different path on the cooling host fails without being absence.
	r.server.set(trustURL("three-7k3q.com"), 200, `{"authoritative_location":"https://records.host.example.net/b/trust.json"}`)
	_, err = r.eval.Evaluate(context.Background(), "three-7k3q.com", "")
	require.Error(t, err)
	require.False(t, IsTrustRecordAbsent(err))
	require.Zero(t, r.server.count("https://records.host.example.net/b/trust.json"), "the host is cooling down")
}

func TestEvaluateBoundsPending(t *testing.T) {
	server := newTrustServer()
	release := make(chan struct{})
	for _, d := range []string{"a-7k3q.com", "b-7k3q.com"} {
		server.set(trustURL(d), 200, `{"grants":[]}`)
		server.block[trustURL(d)] = release
	}
	eval, _ := NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{Fetch: server.fetch, MaxRecords: 2, Clock: func() time.Time { return testNow }})
	var wg sync.WaitGroup
	for _, d := range []string{"a-7k3q.com", "b-7k3q.com"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = eval.Evaluate(context.Background(), d, "") }()
	}
	require.Eventually(t, func() bool {
		return server.count(trustURL("a-7k3q.com")) == 1 && server.count(trustURL("b-7k3q.com")) == 1
	}, time.Second, time.Millisecond)
	_, err := eval.Evaluate(context.Background(), "c-7k3q.com", "")
	require.Error(t, err)
	close(release)
	wg.Wait()
}

func TestBindingsAndGate(t *testing.T) {
	ctx := context.Background()
	b := NewOriginBindings()
	require.NoError(t, b.Bind(ctx, "https://buyer.example.com/mcp", "p1", BindOptions{}))
	require.NoError(t, b.Bind(ctx, "https://BUYER.example.com:443", "p1", BindOptions{}), "rebinding to the same principal is idempotent")
	require.ErrorIs(t, b.Bind(ctx, "https://buyer.example.com", "p2", BindOptions{}), ErrRebind)
	require.NoError(t, b.Bind(ctx, "https://second.example.com", "p1", BindOptions{}), "a principal may own several origins")
	require.ErrorIs(t, b.Bind(ctx, "https://legacy.example.com", "p9", BindOptions{ExistingRFC9421Principal: true}), ErrMigrationNotApproved)
	require.NoError(t, b.Bind(ctx, "https://legacy.example.com", "p9", BindOptions{ExistingRFC9421Principal: true, MigrationApproved: true}))
	require.Error(t, b.Bind(ctx, "http://plain.example.com", "p3", BindOptions{}))
	require.Error(t, b.Bind(ctx, "https://x.example.com", "", BindOptions{}))
	p, ok, _ := b.PrincipalForOrigin(ctx, "https://buyer.example.com/other")
	require.True(t, ok)
	require.Equal(t, "p1", p)

	r := newEvalRig(t)
	r.server.set(trustURL("example.com"), 200, buyerRecord)
	gate := &BindingGate{Bindings: b, Records: r.eval}
	signer := &signing.VerifiedSigner{AgentURL: "https://buyer.example.com", Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}
	got, err := gate.AuthorizeWBA(ctx, signer, "create_media_buy")
	require.NoError(t, err)
	require.Equal(t, "p1", got)
	unbound := &signing.VerifiedSigner{AgentURL: "https://new.example.com", Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}
	got, err = gate.AuthorizeWBA(ctx, unbound, "get_principal")
	require.NoError(t, err)
	require.Empty(t, got, "an unbound origin is identity-established")

	// An unreachable record is not a delisting: spend-committing calls fail
	// with the retryable SERVICE_UNAVAILABLE, other calls continue under
	// the binding.
	r.server.fail(trustURL("example.com"), errors.New("connection refused"))
	r.now = r.now.Add(2 * time.Minute)
	_, err = gate.AuthorizeWBA(ctx, signer, "create_media_buy")
	we := signing.AsWBAError(err)
	require.NotNil(t, we)
	require.True(t, we.Authorization)
	require.Equal(t, signing.CodeServiceUnavailable, we.Code)
	got, err = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
	require.NoError(t, err)
	require.Equal(t, "p1", got, "non-spend calls continue under the binding")

	// Delisting ends the binding's authority: the origin falls back to
	// identity-established, for spend and other calls alike, not to
	// deny-everything. The binding is kept, so it still blocks an RFC 9421
	// downgrade, and it cannot be bound again while unlisted.
	r.server.set(trustURL("example.com"), 200, `{"agents":[{"url":"https://other.example.com","roles":["adcp:buying"]}]}`)
	r.now = r.now.Add(2 * time.Minute)
	for _, op := range []string{"create_media_buy", "get_principal", "sync_accounts"} {
		got, err = gate.AuthorizeWBA(ctx, signer, op)
		require.NoError(t, err, op)
		require.Empty(t, got, op)
	}
	// No exception for task polling or cancellation: delisted is
	// identity-established.
	for _, op := range []string{"get_task_status", "tasks/get", "tasks/cancel"} {
		got, err = gate.AuthorizeWBA(ctx, signer, op)
		require.NoError(t, err, op)
		require.Empty(t, got, op)
	}
	_, still, _ := b.PrincipalForOrigin(ctx, "https://buyer.example.com")
	require.True(t, still)
	_, err = r.eval.Bind(ctx, signer, "p1", b, BindOptions{})
	require.Error(t, err, "open onboarding cannot bind an unlisted origin again")

	// A published record that disappears before any 200 is absence: also
	// not listed.
	r2 := newEvalRig(t)
	b2 := NewOriginBindings()
	require.NoError(t, b2.Bind(ctx, "https://agent.absent-7k3q.com", "p9", BindOptions{}))
	got, err = (&BindingGate{Bindings: b2, Records: r2.eval}).AuthorizeWBA(ctx,
		&signing.VerifiedSigner{AgentURL: "https://agent.absent-7k3q.com", Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}, "create_media_buy")
	require.NoError(t, err)
	require.Empty(t, got)

	// Binding is explicit and needs the trust record listing.
	r.server.set(trustURL("example.com"), 200, `{"agents":[{"url":"https://new.example.com","roles":["adcp:buying"]},{"url":"https://sales.example.com","roles":["adcp:sales"]}]}`)
	r.now = r.now.Add(2 * time.Minute)
	_, err = r.eval.Bind(ctx, unbound, "p5", b, BindOptions{})
	require.NoError(t, err)
	p, _, _ = b.PrincipalForOrigin(ctx, unbound.AgentURL)
	require.Equal(t, "p5", p)
	_, err = r.eval.Bind(ctx, &signing.VerifiedSigner{AgentURL: "https://sales.example.com", Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}, "p6", b, BindOptions{})
	require.Error(t, err, "the required role is adcp:buying")
	_, err = r.eval.Bind(ctx, &signing.VerifiedSigner{AgentURL: "https://sales.example.com", Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}, "p6", b, BindOptions{Role: RoleSales})
	require.NoError(t, err)
	_, err = r.eval.Bind(ctx, &signing.VerifiedSigner{AgentURL: "https://new.example.com", Profile: signing.ProfileTagRequestSigning}, "p7", b, BindOptions{})
	require.Error(t, err, "binding requires a Web Bot Auth identity")
	_, err = r.eval.Bind(ctx, nil, "p7", b, BindOptions{})
	require.Error(t, err)
	// An origin that serves no revocation list (or whose list is stale, or
	// was never checked) cannot be bound for spend.
	for _, state := range []signing.RevocationState{signing.RevocationNoList, signing.RevocationStale, signing.RevocationUnchecked} {
		_, err = r.eval.Bind(ctx, &signing.VerifiedSigner{AgentURL: "https://new.example.com", Profile: signing.ProfileTagWebBotAuth, Revocation: state}, "p8", NewOriginBindings(), BindOptions{})
		require.ErrorIs(t, err, ErrRevocationListRequired, state.String())
	}
}

// TestBindingGateOutageRules: an outage never restores access. The last
// record read stays authoritative, through a 404 or 410 too; non-spend calls
// continue under a binding only within the OutageContinuation bound (4x the
// polling interval past the listing record's freshness by default); caps
// that force an "outage" cannot bypass a delisting.
func TestBindingGateOutageRules(t *testing.T) {
	ctx := context.Background()
	origin := "https://buyer.example.com"
	signer := &signing.VerifiedSigner{AgentURL: origin, Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}
	listed := `{"agents":[{"url":"https://buyer.example.com/mcp","roles":["adcp:buying"]}]}`
	delisted := `{"agents":[{"url":"https://other.example.com","roles":["adcp:buying"]}]}`
	newGate := func(t *testing.T) (*evalRig, *BindingGate) {
		r := newEvalRig(t)
		b := NewOriginBindings()
		require.NoError(t, b.Bind(ctx, origin, "p1", BindOptions{}))
		return r, &BindingGate{Bindings: b, Records: r.eval}
	}
	expectUnavailable := func(t *testing.T, err error) {
		t.Helper()
		we := signing.AsWBAError(err)
		require.NotNil(t, we, "%v", err)
		require.Equal(t, signing.CodeServiceUnavailable, we.Code)
	}

	t.Run("listed, then outage: grace for non-spend, then 503", func(t *testing.T) {
		r, gate := newGate(t)
		r.server.set(trustURL("example.com"), 200, listed)
		got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Equal(t, "p1", got)
		r.server.fail(trustURL("example.com"), errors.New("connection refused"))
		r.now = r.now.Add(2 * time.Minute) // record (1 min freshness) expired
		got, err = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Equal(t, "p1", got, "within 4x the polling interval past freshness")
		_, err = gate.AuthorizeWBA(ctx, signer, "create_media_buy")
		expectUnavailable(t, err)
		r.now = r.now.Add(4 * time.Minute) // past freshness + 4 x 1 min
		_, err = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		expectUnavailable(t, err)
		r.server.set(trustURL("example.com"), 200, listed)
		r.now = r.now.Add(61 * time.Second) // past the negative cache
		got, err = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Equal(t, "p1", got, "a successful fetch restores the binding")
	})

	t.Run("stale delisting plus outage stays delisted", func(t *testing.T) {
		r, gate := newGate(t)
		r.server.set(trustURL("example.com"), 200, delisted)
		got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Empty(t, got)
		r.server.fail(trustURL("example.com"), errors.New("connection refused"))
		r.now = r.now.Add(2 * time.Minute)
		for _, op := range []string{"get_media_buys", "create_media_buy"} {
			got, err = gate.AuthorizeWBA(ctx, signer, op)
			require.NoError(t, err, op)
			require.Empty(t, got, "an outage never restores access: %s", op)
		}
	})

	t.Run("no history and an outage: 503, never access", func(t *testing.T) {
		r, gate := newGate(t)
		r.server.fail(trustURL("example.com"), errors.New("connection refused"))
		_, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		expectUnavailable(t, err)
	})

	t.Run("404 from an operator that never published: identity-established", func(t *testing.T) {
		_, gate := newGate(t) // the fake serves 404 for an unknown URL
		got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Empty(t, got)
	})

	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(fmt.Sprintf("%d after a read is a discovery failure: the last record stands", status), func(t *testing.T) {
			r, gate := newGate(t)
			r.server.set(trustURL("example.com"), 200, listed)
			_, _ = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
			r.server.set(trustURL("example.com"), status, "")
			r.now = r.now.Add(2 * time.Minute)
			got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
			require.NoError(t, err)
			require.Equal(t, "p1", got, "not a delisting")
			_, err = gate.AuthorizeWBA(ctx, signer, "create_media_buy")
			expectUnavailable(t, err)
		})
	}

	t.Run("an empty agents[] retires the agent", func(t *testing.T) {
		r, gate := newGate(t)
		r.server.set(trustURL("example.com"), 200, listed)
		_, _ = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		r.server.set(trustURL("example.com"), 200, `{"agents":[]}`)
		r.now = r.now.Add(2 * time.Minute)
		got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("OutageContinuation is seller policy", func(t *testing.T) {
		for _, tc := range []struct {
			bound time.Duration
			after time.Duration
			cont  bool
		}{
			{10 * time.Minute, 9 * time.Minute, true},
			{10 * time.Minute, 11 * time.Minute, false},
			{-1, 30 * time.Second, false},
		} {
			server := newTrustServer()
			now := testNow
			eval, err := NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{Fetch: server.fetch, Clock: func() time.Time { return now }, PollingInterval: time.Minute, OutageContinuation: tc.bound})
			require.NoError(t, err)
			b := NewOriginBindings()
			require.NoError(t, b.Bind(ctx, origin, "p1", BindOptions{}))
			gate := &BindingGate{Bindings: b, Records: eval}
			server.set(trustURL("example.com"), 200, listed)
			_, _ = gate.AuthorizeWBA(ctx, signer, "get_media_buys")
			server.fail(trustURL("example.com"), errors.New("connection refused"))
			now = now.Add(time.Minute + tc.after) // the record's 1 min freshness, then the outage
			got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
			if tc.cont {
				require.NoError(t, err)
				require.Equal(t, "p1", got)
			} else {
				expectUnavailable(t, err)
			}
		}
	})

	t.Run("revocation state", func(t *testing.T) {
		r, gate := newGate(t)
		r.server.set(trustURL("example.com"), 200, listed)
		for _, state := range []signing.RevocationState{signing.RevocationNoList, signing.RevocationUnchecked} {
			s := *signer
			s.Revocation = state
			got, err := gate.AuthorizeWBA(ctx, &s, "create_media_buy")
			require.NoError(t, err)
			require.Empty(t, got, "no revocation list: identity-established, never bound for spend (%s)", state)
		}
		unavailable := *signer
		unavailable.Revocation = signing.RevocationUnavailable
		_, err := gate.AuthorizeWBA(ctx, &unavailable, "get_media_buys")
		expectUnavailable(t, err)
		stale := *signer
		stale.Revocation = signing.RevocationStale
		_, err = gate.AuthorizeWBA(ctx, &stale, "create_media_buy")
		expectUnavailable(t, err)
		got, err := gate.AuthorizeWBA(ctx, &stale, "get_media_buys")
		require.NoError(t, err)
		require.Equal(t, "p1", got, "a stale list blocks spend only")
	})

	t.Run("a cap-forced outage cannot bypass a delisting", func(t *testing.T) {
		server := newTrustServer()
		now := testNow
		eval, _ := NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{Fetch: server.fetch, Clock: func() time.Time { return now }, PollingInterval: time.Minute, MaxFetchesPerHost: 1})
		b := NewOriginBindings()
		require.NoError(t, b.Bind(ctx, origin, "p1", BindOptions{}))
		gate := &BindingGate{Bindings: b, Records: eval}
		server.set(trustURL("example.com"), 200, delisted)
		got, err := gate.AuthorizeWBA(ctx, signer, "get_media_buys")
		require.NoError(t, err)
		require.Empty(t, got)
		// Hold the only fetch slot for the host so the refresh fails on the
		// cap, then ask again after the record expires.
		now = now.Add(2 * time.Minute)
		release := make(chan struct{})
		server.mu.Lock()
		server.block[trustURL("example.com")] = release
		server.mu.Unlock()
		go func() { _, _ = eval.Evaluate(context.Background(), "example.com", "") }()
		require.Eventually(t, func() bool { return server.count(trustURL("example.com")) == 2 }, time.Second, time.Millisecond)
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		got, err = gate.AuthorizeWBA(short, signer, "get_media_buys")
		require.NoError(t, err)
		require.Empty(t, got, "the last record read still delists the origin")
		close(release)
	})
}

// TestAuthorityHistoryResistsFlooding: one party with many free hosting
// subdomains (a PSL PRIVATE suffix) cannot evict another operator's
// authority history, so a brand's later 404 is still a discovery failure,
// not absence (the brand.json fallback stays disabled), after the flood.
func TestAuthorityHistoryResistsFlooding(t *testing.T) {
	ctx := context.Background()
	r := newEvalRig(t)
	r.eval.maxRecords = 1 << 12
	r.server.set(trustURL("brand-7k3q.com"), 200, `{"grants":[]}`)
	_, err := r.eval.Evaluate(ctx, "brand-7k3q.com", "")
	require.NoError(t, err)
	for i := range historyPerOperator + 10 {
		tenant := fmt.Sprintf("tenant%d.github.io", i)
		r.server.set(trustURL(tenant), 200, `{"grants":[]}`)
		_, err := r.eval.Evaluate(ctx, tenant, "")
		require.NoError(t, err)
	}
	mem := r.eval.history.(*memoryAuthorityHistory)
	mem.mu.Lock()
	require.Equal(t, historyPerOperator, mem.table.GroupLen("github.io"), "the platform's tenants share one quota")
	brand, _ := mem.table.Get("brand-7k3q.com")
	mem.mu.Unlock()
	require.True(t, brand.Served, "the brand's history was not evicted")

	r.server.set(trustURL("brand-7k3q.com"), http.StatusNotFound, "")
	r.now = r.now.Add(2 * time.Minute)
	_, err = r.eval.Evaluate(ctx, "brand-7k3q.com", "")
	require.False(t, IsTrustRecordAbsent(err), "not absence, so authorized_operators does not come back")
	require.True(t, IsTrustRecordUnavailable(err))

	// A refused tenant simply has no history: its own later 404 is absence.
	over := fmt.Sprintf("tenant%d.github.io", historyPerOperator+5)
	r.server.set(trustURL(over), http.StatusNotFound, "")
	_, err = r.eval.Evaluate(ctx, over, "")
	require.True(t, IsTrustRecordAbsent(err))
}

// sharedHistory is a stand-in for a durable AuthorityHistory shared by
// replicas.
type sharedHistory struct {
	mu       sync.Mutex
	states   map[string]AuthorityState
	storeErr error
	loadErr  error
}

func (h *sharedHistory) LoadAuthority(_ context.Context, domain string) (AuthorityState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.states[domain], h.loadErr
}

func (h *sharedHistory) StoreAuthority(_ context.Context, domain string, s AuthorityState) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.storeErr != nil {
		return h.storeErr
	}
	h.states[domain] = s
	return nil
}

// TestDurableAuthorityHistory: with a shared History, a replica that never
// read the record still treats a 404 as a discovery failure, not absence,
// and keeps the last record through an outage; a history that cannot be
// read or written fails the evaluation closed.
func TestDurableAuthorityHistory(t *testing.T) {
	ctx := context.Background()
	hist := &sharedHistory{states: map[string]AuthorityState{}}
	server := newTrustServer()
	now := testNow
	newEval := func() *TrustRecordEvaluator {
		e, err := NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{Fetch: server.fetch, Clock: func() time.Time { return now }, History: hist})
		require.NoError(t, err)
		return e
	}
	server.set(trustURL("example.com"), 200, buyerRecord)
	_, err := newEval().Evaluate(ctx, "https://buyer.example.com", RoleBuying)
	require.NoError(t, err)
	require.True(t, hist.states["example.com"].Served)
	require.NotNil(t, hist.states["example.com"].LastRecord)

	// Another replica, during an outage: the shared last record holds.
	server.set(trustURL("example.com"), http.StatusServiceUnavailable, "")
	l := newEval().CheckListing(ctx, "https://buyer.example.com", RoleBuying)
	require.Equal(t, Unavailable, l.Status)
	require.True(t, l.WithinGrace)

	// Another replica after a 404: not absence; the last record stands.
	server.set(trustURL("example.com"), http.StatusNotFound, "")
	e := newEval()
	_, err = e.Evaluate(ctx, "example.com", "")
	require.False(t, IsTrustRecordAbsent(err))
	l = e.CheckListing(ctx, "https://buyer.example.com", RoleBuying)
	require.Equal(t, Unavailable, l.Status)
	require.True(t, l.WithinGrace)
	require.True(t, l.Record.Stale)

	// A history that cannot be written or read fails closed.
	server.set(trustURL("other.com"), 200, `{"grants":[]}`)
	hist.storeErr = errors.New("db down")
	_, err = newEval().Evaluate(ctx, "other.com", "")
	require.True(t, IsTrustRecordUnavailable(err), "%v", err)
	hist.storeErr = nil
	hist.loadErr = errors.New("db down")
	server.set(trustURL("gone.com"), http.StatusNotFound, "")
	_, err = newEval().Evaluate(ctx, "gone.com", "")
	require.True(t, IsTrustRecordUnavailable(err), "a 404 is not absence when history is unreadable: %v", err)
	l = newEval().CheckListing(ctx, "https://buyer.example.com", RoleBuying)
	require.Equal(t, Unavailable, l.Status)
	require.False(t, l.WithinGrace)
}
