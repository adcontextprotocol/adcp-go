package identity

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/psl"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/safefetch"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// TrustRecordError is a trust record that provides no authority. It is
// one of two kinds, which the spec treats differently for a bound origin:
//   - not listed (Unlisted or Absent): the operator's record was read and
//     does not list the origin with the role, the operator has never
//     published one (a 404 or 410 before any 200 from it), or the origin
//     has no registrable domain. A bound origin falls back to
//     identity-established. Absent is also the one case that permits a
//     configured legacy (brand.json) fallback for grants.
//   - unavailable (neither flag): the record could not be fetched or read,
//     including a 404 or 410 from an operator that has served a record: a
//     discovery failure, never absence and never a removal. The last record
//     read stays authoritative (operators retire agents by publishing an
//     empty agents[]); spend-committing calls fail with SERVICE_UNAVAILABLE,
//     and other calls may continue on the last record within the seller's
//     OutageContinuation bound.
type TrustRecordError struct {
	Absent   bool
	Unlisted bool
	Reason   string
	Wrapped  error
}

func (e *TrustRecordError) Error() string { return "identity: " + e.Reason }
func (e *TrustRecordError) Unwrap() error { return e.Wrapped }

// IsTrustRecordAbsent reports whether err is a TrustRecordError with Absent
// set.
func IsTrustRecordAbsent(err error) bool {
	var e *TrustRecordError
	return errors.As(err, &e) && e.Absent
}

// IsTrustRecordUnavailable reports whether err means the record could not
// be read (a fetch, parse, or capacity failure), as opposed to a record
// that does not list the origin. Any error that is not a TrustRecordError
// counts as unavailable.
func IsTrustRecordUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var e *TrustRecordError
	if !errors.As(err, &e) {
		return true
	}
	return !e.Absent && !e.Unlisted
}

func trustErr(reason string, wrapped error) *TrustRecordError {
	return &TrustRecordError{Reason: reason, Wrapped: wrapped}
}

// TrustRecordURL derives the trust record location for an origin or a
// domain: https://{eTLD+1}/.well-known/trust.json, using the pinned ICANN
// and PRIVATE Public Suffix List snapshot. Never a caller-supplied pointer.
// An IP literal, a public suffix, or a host with no eTLD+1 has no record.
func TrustRecordURL(originOrDomain string) (string, error) {
	domain, ok := psl.RegistrableDomain(originOrDomain)
	if !ok {
		return "", &TrustRecordError{Unlisted: true, Reason: "identity has no registrable operator domain"}
	}
	return "https://" + domain + "/.well-known/trust.json", nil
}

// TrustResponse is one trust record fetch.
type TrustResponse struct {
	Status int
	Body   []byte
}

// TrustFetcher fetches a trust record URL. It is a trusted transport seam:
// a custom fetcher must enforce the fetch rules (HTTPS, SSRF checks, no
// redirects, 2 MiB cap). The default does.
type TrustFetcher func(ctx context.Context, url string) (*TrustResponse, error)

// EvaluatedTrustRecord is a validated trust record. OperatorDomain is the
// eTLD+1 the record speaks for, derived from the identity and never read
// from the document or from an authoritative_location hop target.
type EvaluatedTrustRecord struct {
	OperatorDomain string
	SourceURL      string
	Record         *TrustRecord
	ExpiresAt      time.Time
	// Stale reports the last record read, returned during an outage within
	// the evaluator's OutageContinuation bound; ExpiresAt is then the end of
	// that bound.
	Stale bool
}

// Agent returns the record's single agents[] entry for origin with role.
func (e *EvaluatedTrustRecord) Agent(origin, role string) (*TrustAgent, error) {
	return e.Record.Agent(origin, role)
}

// TrustRecordEvaluatorOptions configures NewTrustRecordEvaluator.
type TrustRecordEvaluatorOptions struct {
	// Fetch defaults to an SSRF-safe HTTPS fetch with a 2 MiB cap.
	Fetch TrustFetcher
	// PollingInterval bounds how long an evaluation is cached: the
	// revocation polling interval, in (0, 30 min]. Defaults to 60 s.
	PollingInterval time.Duration
	// MaxRecords bounds cached records, negative entries, and concurrent
	// discoveries. Defaults to 256.
	MaxRecords int
	// MaxFetchesPerHost bounds concurrent fetches to one host. Defaults to 4.
	MaxFetchesPerHost int
	// History persists authority history (which operators have served a
	// trust.json, and the last record read from each). Defaults to a
	// bounded in-memory history; public deployments must supply a durable
	// one. See AuthorityHistory.
	History AuthorityHistory
	// OutageContinuation is seller policy: how long past its freshness the
	// last record read keeps authorizing non-spend calls (bindings, and
	// grants for non-spend operations) while the operator's record cannot
	// be read. Spend-committing calls never continue on a stale record.
	// Defaults to 4x PollingInterval; a negative value disables
	// continuation.
	OutageContinuation time.Duration
	// Clock defaults to time.Now.
	Clock func() time.Time
	// Logger receives warnings about records that read but may not mean
	// what their author intended. Defaults to slog.Default().
	Logger *slog.Logger
}

// TrustRecordEvaluator evaluates operator trust records at binding, grant
// evaluation, and open onboarding, never on the signature path:
//   - derive https://{eTLD+1}/.well-known/trust.json (TrustRecordURL);
//   - fetch it under the SSRF rules with a 2 MiB cap and strict parsing;
//   - follow at most one authoritative_location hop, with no HTTP redirects,
//     refusing a hop target that itself points onward;
//   - cache the result no longer than the polling interval.
//
// Fetch failures, invalid records, and expired results fail closed. Fetches
// are single-flighted per operator and per URL, bounded per host, and a
// failed fetch is negatively cached for 60 s. A 200 from an operator's
// derived URL is remembered in History, so a later 404 is never treated
// as absence (no downgrade to brand.json).
type TrustRecordEvaluator struct {
	fetch      TrustFetcher
	interval   time.Duration
	maxRecords int
	maxPerHost int
	clock      func() time.Time
	outage     time.Duration
	logger     *slog.Logger

	mu       sync.Mutex
	cache    map[string]*cachedRecord
	lru      *list.List
	failures map[string]trustFailure
	pending  map[string]*trustCall
	history  AuthorityHistory

	responses       map[string]cachedResponse
	responsePending map[string]*responseCall
	hostsInflight   map[string]int
	hostFailures    map[string]time.Time
}

type cachedRecord struct {
	rec  *EvaluatedTrustRecord
	elem *list.Element
}

type trustFailure struct {
	err        *TrustRecordError
	retryAfter time.Time
}

type trustCall struct {
	done chan struct{}
	rec  *EvaluatedTrustRecord
	err  error
}

type cachedResponse struct {
	expires time.Time
	resp    *TrustResponse
}

type responseCall struct {
	done chan struct{}
	res  cachedResponse
	err  error
}

const (
	trustNegativeTTL      = 60 * time.Second
	trustMaxInterval      = 30 * time.Minute
	trustDefaultInterval  = 60 * time.Second
	trustDiscoveryTimeout = 10 * time.Second
)

// NewTrustRecordEvaluator returns a TrustRecordEvaluator.
func NewTrustRecordEvaluator(opts TrustRecordEvaluatorOptions) (*TrustRecordEvaluator, error) {
	e := &TrustRecordEvaluator{
		fetch:           opts.Fetch,
		interval:        opts.PollingInterval,
		maxRecords:      opts.MaxRecords,
		maxPerHost:      opts.MaxFetchesPerHost,
		clock:           opts.Clock,
		logger:          opts.Logger,
		cache:           map[string]*cachedRecord{},
		lru:             list.New(),
		failures:        map[string]trustFailure{},
		pending:         map[string]*trustCall{},
		history:         opts.History,
		outage:          opts.OutageContinuation,
		responses:       map[string]cachedResponse{},
		responsePending: map[string]*responseCall{},
		hostsInflight:   map[string]int{},
		hostFailures:    map[string]time.Time{},
	}
	if e.interval == 0 {
		e.interval = trustDefaultInterval
	}
	if e.logger == nil {
		e.logger = slog.Default()
	}
	if e.interval < 0 || e.interval > trustMaxInterval {
		return nil, errors.New("identity: PollingInterval must be in (0, 30m]")
	}
	if e.maxRecords <= 0 {
		e.maxRecords = 256
	}
	switch {
	case e.outage == 0:
		e.outage = 4 * e.interval
	case e.outage < 0:
		e.outage = 0
	}
	if e.maxPerHost <= 0 {
		e.maxPerHost = 4
	}
	if e.clock == nil {
		e.clock = time.Now
	}
	if e.history == nil {
		e.history = newMemoryAuthorityHistory()
	}
	if e.fetch == nil {
		client := safefetch.NewClient()
		e.fetch = func(ctx context.Context, u string) (*TrustResponse, error) {
			r, err := safefetch.Get(ctx, client, u, safefetch.Options{Accept: "application/json", MaxBytes: TrustRecordMaxBytes})
			if err != nil {
				return nil, err
			}
			return &TrustResponse{Status: r.Status, Body: r.Body}, nil
		}
	}
	return e, nil
}

// PollingInterval returns the evaluator's cache bound.
func (e *TrustRecordEvaluator) PollingInterval() time.Duration { return e.interval }

// Evaluate returns the trust record for the operator of originOrDomain.
// With role set, the record must list exactly one agent whose canonical
// origin is originOrDomain and that carries role. The returned record is a
// copy; mutating it cannot change cached authority.
func (e *TrustRecordEvaluator) Evaluate(ctx context.Context, originOrDomain, role string) (*EvaluatedTrustRecord, error) {
	recordURL, err := TrustRecordURL(originOrDomain)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(recordURL)
	domain := u.Hostname()
	e.mu.Lock()
	now := e.clock()
	var rec *EvaluatedTrustRecord
	var call *trustCall
	if c := e.cache[domain]; c != nil && now.Before(c.rec.ExpiresAt) {
		e.lru.MoveToFront(c.elem)
		rec = c.rec
	} else {
		if f, ok := e.failures[domain]; ok && now.Before(f.retryAfter) {
			e.mu.Unlock()
			return nil, f.err
		}
		call = e.pending[domain]
		if call == nil {
			if len(e.pending) >= e.maxRecords {
				e.mu.Unlock()
				return nil, trustErr("trust discovery capacity exceeded", nil)
			}
			call = &trustCall{done: make(chan struct{})}
			e.pending[domain] = call
			// One disconnected caller cannot cancel the shared fetch.
			go e.refresh(context.WithoutCancel(ctx), domain, recordURL, call)
		}
	}
	e.mu.Unlock()
	if call != nil {
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call.err != nil {
			return nil, call.err
		}
		rec = call.rec
	}
	if !e.clock().Before(rec.ExpiresAt) {
		return nil, trustErr("trust record expired while being evaluated", nil)
	}
	if role != "" {
		if _, err := rec.Agent(originOrDomain, role); err != nil {
			return nil, &TrustRecordError{Unlisted: true, Reason: "trust record does not list the origin with the required role", Wrapped: err}
		}
	}
	return rec.clone(), nil
}

func (r *EvaluatedTrustRecord) clone() *EvaluatedTrustRecord {
	out := *r
	b, err := json.Marshal(r.Record)
	if err == nil {
		var rec TrustRecord
		if json.Unmarshal(b, &rec) == nil {
			out.Record = &rec
		}
	}
	return &out
}

func (e *TrustRecordEvaluator) refresh(ctx context.Context, domain, recordURL string, call *trustCall) {
	defer close(call.done)
	ctx, cancel := context.WithTimeout(ctx, trustDiscoveryTimeout)
	defer cancel()
	rec, err := e.resolve(ctx, domain, recordURL)
	if err == nil {
		err = e.storeLastGood(ctx, domain, rec)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.pending, domain)
	if err != nil {
		var te *TrustRecordError
		if !errors.As(err, &te) {
			te = trustErr("trust record fetch failed", err)
		}
		e.failures[domain] = trustFailure{err: te, retryAfter: e.clock().Add(trustNegativeTTL)}
		e.boundFailures()
		call.err = te
		return
	}
	if c := e.cache[domain]; c != nil {
		c.rec = rec
		e.lru.MoveToFront(c.elem)
	} else {
		e.cache[domain] = &cachedRecord{rec: rec, elem: e.lru.PushFront(domain)}
	}
	delete(e.failures, domain)
	for e.lru.Len() > e.maxRecords {
		back := e.lru.Back()
		e.lru.Remove(back)
		delete(e.cache, back.Value.(string))
	}
	call.rec = rec
}

// boundFailures keeps the negative cache within MaxRecords, dropping
// expired entries first. Called with mu held.
func (e *TrustRecordEvaluator) boundFailures() {
	if len(e.failures) <= e.maxRecords {
		return
	}
	now := e.clock()
	for d, f := range e.failures {
		if !now.Before(f.retryAfter) {
			delete(e.failures, d)
		}
	}
	for len(e.failures) > e.maxRecords {
		var oldest string
		var at time.Time
		for d, f := range e.failures {
			if oldest == "" || f.retryAfter.Before(at) {
				oldest, at = d, f.retryAfter
			}
		}
		delete(e.failures, oldest)
	}
}

func (e *TrustRecordEvaluator) resolve(ctx context.Context, domain, recordURL string) (*EvaluatedTrustRecord, error) {
	source := recordURL
	expires := e.clock().Add(e.interval)
	for hop := 0; hop < 2; hop++ {
		if err := safefetch.ValidateTarget(source); err != nil {
			return nil, trustErr("invalid trust fetch target", err)
		}
		res, err := e.fetchResponse(ctx, source)
		if err != nil {
			return nil, trustErr("trust record fetch failed", err)
		}
		if res.expires.Before(expires) {
			expires = res.expires
		}
		status := res.resp.Status
		if status == http.StatusNotFound || status == http.StatusGone {
			state, herr := e.history.LoadAuthority(ctx, domain)
			if herr != nil {
				return nil, trustErr("trust record history unavailable", herr)
			}
			if state.Served {
				// The operator has published a record: a 404 now is a
				// discovery failure, not absence and not a removal. The
				// last record read stays authoritative, and brand.json
				// does not come back.
				return nil, trustErr(fmt.Sprintf("trust record not found (HTTP %d) after the operator published one", status), nil)
			}
			if hop == 0 {
				return nil, &TrustRecordError{Absent: true, Reason: "no trust record is published"}
			}
		}
		if status != http.StatusOK || len(res.resp.Body) > TrustRecordMaxBytes {
			return nil, trustErr(fmt.Sprintf("trust record unavailable (HTTP %d)", status), nil)
		}
		if err := e.markSeen200(ctx, domain); err != nil {
			return nil, trustErr("trust record history unavailable", err)
		}
		rec, err := ValidateTrustRecord(res.resp.Body, TrustRecordOptions{Mode: Reading, Owner: domain})
		if err != nil {
			return nil, trustErr("trust record is invalid", err)
		}
		e.warnNonRegistrableGrantees(domain, rec)
		if rec.AuthoritativeLocation == "" {
			return &EvaluatedTrustRecord{OperatorDomain: domain, SourceURL: source, Record: rec, ExpiresAt: expires}, nil
		}
		if hop == 1 {
			return nil, trustErr("a second authoritative_location hop is forbidden", nil)
		}
		source = rec.AuthoritativeLocation
	}
	return nil, trustErr("trust record resolution failed", nil)
}

// markSeen200 records that domain served a 200, keeping its last record.
func (e *TrustRecordEvaluator) markSeen200(ctx context.Context, domain string) error {
	state, err := e.history.LoadAuthority(ctx, domain)
	if err != nil {
		return err
	}
	if state.Served {
		return nil
	}
	state.Served = true
	return e.history.StoreAuthority(ctx, domain, state)
}

// storeLastGood records rec as the last record read for domain.
func (e *TrustRecordEvaluator) storeLastGood(ctx context.Context, domain string, rec *EvaluatedTrustRecord) error {
	state, err := e.history.LoadAuthority(ctx, domain)
	if err != nil {
		return trustErr("trust record history unavailable", err)
	}
	state.LastRecord = rec
	if err := e.history.StoreAuthority(ctx, domain, state); err != nil {
		return trustErr("trust record history unavailable", err)
	}
	return nil
}

// ListingStatus is the outcome of CheckListing.
type ListingStatus int

const (
	// Listed: the operator's record lists the origin with the role.
	Listed ListingStatus = iota
	// Unlisted: the last record read does not list it (including an empty
	// agents[]), the operator has never published a record, or the origin
	// has no registrable domain.
	Unlisted
	// Unavailable: the record cannot be read now, and the last record read
	// listed the origin (or there is none).
	Unavailable
)

// Listing is the outcome of CheckListing.
type Listing struct {
	Status ListingStatus
	// Record is the evaluated record when Listed.
	Record *EvaluatedTrustRecord
	// WithinGrace, for Unavailable, reports that the last record that
	// listed the origin is within the OutageContinuation bound past its
	// freshness, so non-spend calls may continue under a binding. Record is
	// then that last record, marked Stale, expiring at the bound.
	WithinGrace bool
	// Err is the evaluation error, for logs.
	Err error
}

// CheckListing evaluates whether the origin's operator lists it with role,
// applying the outage rules: a fetch failure is not absence, and an outage
// never restores access.
//   - The last record read stays authoritative: if it did not list the
//     origin, the origin stays Unlisted even when a refresh fails, whatever
//     the cause (including a rate or concurrency cap, or a 404 after the
//     operator published a record).
//   - A 404 or 410 from an operator that has never published a record is
//     Unlisted (no record).
//   - Otherwise an unreadable record is Unavailable, WithinGrace while the
//     last record that listed the origin is no more than OutageContinuation
//     past its freshness. Without authority history (another replica, a
//     restart, no durable History) there is no last record, and an outage
//     is Unavailable without grace.
func (e *TrustRecordEvaluator) CheckListing(ctx context.Context, origin, role string) Listing {
	rec, err := e.Evaluate(ctx, origin, role)
	if err == nil {
		return Listing{Status: Listed, Record: rec}
	}
	if !IsTrustRecordUnavailable(err) {
		// Not listed, never published, or no registrable domain.
		return Listing{Status: Unlisted, Err: err}
	}
	domain, ok := psl.RegistrableDomain(origin)
	if !ok {
		return Listing{Status: Unlisted, Err: err}
	}
	state, herr := e.history.LoadAuthority(ctx, domain)
	last := state.LastRecord
	if herr != nil || last == nil {
		return Listing{Status: Unavailable, Err: err}
	}
	if _, merr := last.Agent(origin, role); merr != nil {
		return Listing{Status: Unlisted, Err: err}
	}
	l := Listing{Status: Unavailable, Err: err}
	if stale := e.staleWithinBound(last); stale != nil {
		l.WithinGrace, l.Record = true, stale
	}
	return l
}

// staleWithinBound returns a copy of last marked Stale and expiring at the
// OutageContinuation bound, or nil when the bound has passed.
func (e *TrustRecordEvaluator) staleWithinBound(last *EvaluatedTrustRecord) *EvaluatedTrustRecord {
	until := last.ExpiresAt.Add(e.outage)
	if e.outage <= 0 || !e.clock().Before(until) {
		return nil
	}
	stale := last.clone()
	stale.ExpiresAt, stale.Stale = until, true
	return stale
}

// evaluateForGrant evaluates a grantor's or intermediary's record. During
// an outage a non-spend claim continues on the last record read within the
// OutageContinuation bound; a spend claim never does.
func (e *TrustRecordEvaluator) evaluateForGrant(ctx context.Context, domain string, spend bool) (*EvaluatedTrustRecord, error) {
	rec, err := e.Evaluate(ctx, domain, "")
	if err == nil || spend || !IsTrustRecordUnavailable(err) {
		return rec, err
	}
	operator, ok := psl.RegistrableDomain(domain)
	if !ok {
		return nil, err
	}
	state, herr := e.history.LoadAuthority(ctx, operator)
	if herr != nil || state.LastRecord == nil {
		return nil, err
	}
	if stale := e.staleWithinBound(state.LastRecord); stale != nil {
		return stale, nil
	}
	return nil, err
}

// fetchResponse shares transport bytes per URL while each owner validates
// them separately. A cached response's lifetime bounds the record built
// from it, so a shared hop cannot extend old authority.
func (e *TrustRecordEvaluator) fetchResponse(ctx context.Context, rawURL string) (cachedResponse, error) {
	u, _ := url.Parse(rawURL)
	host := u.Hostname()
	e.mu.Lock()
	now := e.clock()
	if c, ok := e.responses[rawURL]; ok && now.Before(c.expires) {
		e.mu.Unlock()
		return c, nil
	}
	if until, ok := e.hostFailures[host]; ok && now.Before(until) {
		e.mu.Unlock()
		return cachedResponse{}, errors.New("trust fetch host is cooling down")
	}
	call := e.responsePending[rawURL]
	if call == nil {
		if len(e.responsePending) >= e.maxRecords || e.hostsInflight[host] >= e.maxPerHost {
			e.mu.Unlock()
			return cachedResponse{}, errors.New("trust fetch host capacity exceeded")
		}
		call = &responseCall{done: make(chan struct{})}
		e.responsePending[rawURL] = call
		e.hostsInflight[host]++
		go e.refreshResponse(context.WithoutCancel(ctx), rawURL, host, call)
	}
	e.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		return cachedResponse{}, ctx.Err()
	}
	return call.res, call.err
}

func (e *TrustRecordEvaluator) refreshResponse(ctx context.Context, rawURL, host string, call *responseCall) {
	defer close(call.done)
	ctx, cancel := context.WithTimeout(ctx, trustDiscoveryTimeout)
	defer cancel()
	resp, err := e.fetch(ctx, rawURL)
	if err == nil && len(resp.Body) > TrustRecordMaxBytes {
		err = errors.New("trust record is larger than 2 MiB")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.responsePending, rawURL)
	if e.hostsInflight[host]--; e.hostsInflight[host] <= 0 {
		delete(e.hostsInflight, host)
	}
	now := e.clock()
	if err != nil {
		// A host failure is never treated as the owner's absence.
		e.coolHost(host, now)
		call.err = err
		return
	}
	ttl := e.interval
	if resp.Status != http.StatusOK {
		ttl = trustNegativeTTL
	}
	call.res = cachedResponse{expires: now.Add(ttl), resp: resp}
	e.responses[rawURL] = call.res
	if len(e.responses) > e.maxRecords {
		for k, c := range e.responses {
			if !now.Before(c.expires) {
				delete(e.responses, k)
			}
		}
		for k := range e.responses {
			if len(e.responses) <= e.maxRecords {
				break
			}
			if k != rawURL {
				delete(e.responses, k)
			}
		}
	}
	if resp.Status >= 500 || resp.Status == http.StatusTooManyRequests {
		e.coolHost(host, now)
	}
}

// coolHost records a 60 s host cooldown. Called with mu held.
func (e *TrustRecordEvaluator) coolHost(host string, now time.Time) {
	e.hostFailures[host] = now.Add(trustNegativeTTL)
	if len(e.hostFailures) > e.maxRecords {
		for h, until := range e.hostFailures {
			if !now.Before(until) {
				delete(e.hostFailures, h)
			}
		}
		for h := range e.hostFailures {
			if len(e.hostFailures) <= e.maxRecords {
				break
			}
			if h != host {
				delete(e.hostFailures, h)
			}
		}
	}
}

// ErrRevocationListRequired is returned by Bind for a signer whose origin
// does not serve a current revocation list (VerifiedSigner.Revocation is
// not RevocationCurrent): an identity that serves no revocation list stays
// identity-established and cannot be bound.
var ErrRevocationListRequired = errors.New("identity: binding requires the origin to serve a current revocation list")

// Bind evaluates the trust record of a verified Web Bot Auth signer's
// operator, which must list the signer's origin with role (default
// adcp:buying), and then records the binding in store. The signer must
// carry a current revocation state (run signing.CheckWBARevocation first;
// Middleware does). This is the explicit,
// audited seller decision that turns an identity-established origin into a
// principal; verification alone never does.
func (e *TrustRecordEvaluator) Bind(ctx context.Context, signer *signing.VerifiedSigner, principalID string, store BindingStore, opts BindOptions) (*EvaluatedTrustRecord, error) {
	if signer == nil || signer.Profile != signing.ProfileTagWebBotAuth || signer.AgentURL == "" {
		return nil, errors.New("identity: binding requires a verified Web Bot Auth identity")
	}
	if signer.Revocation != signing.RevocationCurrent {
		return nil, ErrRevocationListRequired
	}
	role := opts.Role
	if role == "" {
		role = RoleBuying
	}
	rec, err := e.Evaluate(ctx, signer.AgentURL, role)
	if err != nil {
		return nil, err
	}
	if err := store.Bind(ctx, signer.AgentURL, principalID, opts); err != nil {
		return nil, err
	}
	return rec, nil
}

// warnNonRegistrableGrantees logs each grant whose grantee is not a
// registrable domain: a host under one, or a public suffix. Such a grant
// covers no claim (L2 Grants in trust.json), so the operator is told why it
// has no effect. Its scopes still count for exclusive_grants. The grantee is
// a lowercase DNS name (the schema's domain pattern), so it is safe to log.
func (e *TrustRecordEvaluator) warnNonRegistrableGrantees(domain string, rec *TrustRecord) {
	for i, g := range rec.Grants {
		if !isRegistrableDomain(g.Grantee) {
			e.logger.Warn("identity: trust record grant names a grantee that is not a registrable domain; the grant covers no claim",
				"operator", domain, "grant", i, "grantee", g.Grantee)
		}
	}
}
