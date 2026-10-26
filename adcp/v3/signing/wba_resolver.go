package signing

import (
	"container/list"
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/safefetch"
)

// DirectoryFetcher fetches a key directory URL. It is a trusted transport
// seam: a custom fetcher must enforce the fetch rules itself (HTTPS, SSRF
// checks, no redirects, a 256 KiB cap). The default uses an SSRF-safe
// client that does.
type DirectoryFetcher func(ctx context.Context, url string) (*DirectoryResponse, error)

// DirectoryResolverOptions configures NewDirectoryResolver.
type DirectoryResolverOptions struct {
	// Fetch defaults to an SSRF-safe HTTPS fetch.
	Fetch DirectoryFetcher
	// MaxOrigins bounds cached origins, cooldown entries, and concurrent
	// discoveries. Defaults to 256.
	MaxOrigins int
	// MaxPendingPerOperator bounds concurrent discoveries for origins of
	// one operator (a registrable domain, with every tenant of a PSL
	// PRIVATE hosting suffix counted together), so one operator (for
	// example with wildcard DNS and a stalling server) cannot hold every
	// discovery slot. Defaults to 8.
	MaxPendingPerOperator int
	// MaxStale bounds how long a cached directory keeps verifying requests
	// while its refreshes fail (WBA §6.10: a failed fetch does not evict a
	// cached entry), counted from the last successful fetch and never past
	// the directory signatures' expiry. Defaults to 24 hours.
	MaxStale time.Duration
	// Clock defaults to time.Now.
	Clock func() time.Time
}

// DefaultDirectoryMaxStale is the default DirectoryResolverOptions.MaxStale.
const DefaultDirectoryMaxStale = 24 * time.Hour

// DirectoryResolver is the WBAKeyResolver for many signers. It discovers
// each signer's keys from the key directory at the origin the covered
// Signature-Agent names and looks keys up by (origin, thumbprint), so a
// seller needs no hand-written keyid-to-agent map. It never reads
// capabilities, brand.json, or trust.json.
//
// Caching follows WBA §6.10 and the profile. A directory is refreshed after
// at most five minutes (sooner if its signatures expire). A key absent from
// a directory that resolves is absent: the new directory replaces the
// cached one, and there is no refetch on a miss. A directory that fails to
// resolve (a network or TLS error, a timeout, any status other than 200, an
// invalid directory, or a fetch the resolver's own limits refuse) is not
// evidence: it never evicts the cached entry, which keeps verifying until
// MaxStale has passed since the last successful fetch, and the failure is
// retried after a 60-second cooldown. A refresh the resolver's own limits
// refuse keeps the cached entry for only one more cache period, so load on
// the verifier cannot stretch a removed key's life to MaxStale; refreshes
// of cached origins do not compete with first discoveries for the global
// discovery slots. With no usable cached entry, the
// lookup fails with a discovery failure (ErrDiscoveryFailure): the request
// is unverified. Concurrent lookups for one origin share a single fetch,
// and one slow origin does not block another.
type DirectoryResolver struct {
	fetch          DirectoryFetcher
	maxOrigins     int
	maxPerOperator int
	maxStale       time.Duration
	clock          func() time.Time

	mu       sync.Mutex
	cache    map[string]*directoryEntry // directories that resolved
	lru      *list.List                 // front = most recently used origin
	failures map[string]time.Time       // discovery cooldowns, kept apart so failures never evict successes
	pending  map[string]*directoryCall
}

type directoryEntry struct {
	origin    string
	resp      *DirectoryResponse
	keys      map[string]JWK
	refreshAt time.Time // refetch after this
	expires   time.Time // keys stop verifying after this, even if refreshes fail
	elem      *list.Element
}

type directoryCall struct {
	done  chan struct{}
	entry *directoryEntry
	err   error
}

const (
	directoryCacheTTL    = 5 * time.Minute
	discoveryNegativeTTL = 60 * time.Second
	discoveryTimeout     = 10 * time.Second
)

// NewDirectoryResolver returns a DirectoryResolver.
func NewDirectoryResolver(opts DirectoryResolverOptions) *DirectoryResolver {
	d := &DirectoryResolver{
		fetch:          opts.Fetch,
		maxOrigins:     opts.MaxOrigins,
		maxPerOperator: opts.MaxPendingPerOperator,
		maxStale:       opts.MaxStale,
		clock:          opts.Clock,
		cache:          map[string]*directoryEntry{},
		lru:            list.New(),
		failures:       map[string]time.Time{},
		pending:        map[string]*directoryCall{},
	}
	if d.maxOrigins <= 0 {
		d.maxOrigins = 256
	}
	if d.maxPerOperator <= 0 {
		d.maxPerOperator = 8
	}
	if d.maxStale <= 0 {
		d.maxStale = DefaultDirectoryMaxStale
	}
	if d.clock == nil {
		d.clock = time.Now
	}
	if d.fetch == nil {
		client := safefetch.NewClient()
		d.fetch = func(ctx context.Context, url string) (*DirectoryResponse, error) {
			r, err := safefetch.Get(ctx, client, url, safefetch.Options{Accept: DirectoryMediaType, MaxBytes: directoryMaxBytes})
			if err != nil {
				return nil, err
			}
			return &DirectoryResponse{Status: r.Status, Header: r.Header, Body: r.Body}, nil
		}
	}
	return d
}

func (e *directoryEntry) lookup(keyID string) *JWK {
	jwk, ok := e.keys[keyID]
	if !ok {
		return nil
	}
	return &jwk
}

// ResolveWBAKey implements WBAKeyResolver.
func (d *DirectoryResolver) ResolveWBAKey(ctx context.Context, origin, keyID string) (*JWK, error) {
	origin, err := webBotAuthOrigin(origin)
	if err != nil {
		return nil, &WBAError{Status: http.StatusForbidden, Reason: "invalid signer origin", Wrapped: err}
	}
	d.mu.Lock()
	now := d.clock()
	e := d.cache[origin]
	if e != nil && now.Before(e.refreshAt) {
		d.lru.MoveToFront(e.elem)
		d.mu.Unlock()
		return e.lookup(keyID), nil
	}
	// stale is the cached entry a failed refresh falls back to.
	stale := func() *directoryEntry {
		if e != nil && d.clock().Before(e.expires) {
			return e
		}
		return nil
	}
	if until, ok := d.failures[origin]; ok && now.Before(until) {
		d.mu.Unlock()
		if s := stale(); s != nil {
			return s.lookup(keyID), nil
		}
		return nil, discoveryFailure("key directory discovery is cooling down", nil)
	}
	call := d.pending[origin]
	if call == nil {
		refused := ""
		// Refreshing a cached origin does not compete with first discovery
		// for the global slots (the cache bounds such refreshes), so a
		// flood of new origins cannot hold a known signer's refresh off.
		if e == nil && len(d.pending) >= d.maxOrigins {
			refused = "key directory discovery capacity exceeded"
		} else {
			op := operatorBucket(origin)
			n := 0
			for o := range d.pending {
				if operatorBucket(o) == op {
					n++
				}
			}
			if n >= d.maxPerOperator {
				refused = "key directory discovery capacity exceeded for operator"
			}
		}
		if refused != "" {
			// A fetch the verifier's own limits refuse is a discovery
			// failure too (WBA §6.10). A refusal is local, not evidence of
			// an outage at the origin, so the cached keys stay usable only
			// for one more cache period, not for MaxStale: load on the
			// verifier cannot keep a removed key alive for long.
			d.mu.Unlock()
			if e != nil && now.Before(minTime(e.expires, e.refreshAt.Add(directoryCacheTTL))) {
				return e.lookup(keyID), nil
			}
			return nil, discoveryFailure(refused, nil)
		}
		call = &directoryCall{done: make(chan struct{})}
		d.pending[origin] = call
		// A caller that gives up does not cancel discovery for other
		// waiters; the fetch keeps its own deadline.
		go d.refresh(context.WithoutCancel(ctx), origin, call)
	}
	d.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if call.err != nil {
		d.mu.Lock()
		e = d.cache[origin]
		s := stale()
		d.mu.Unlock()
		if s != nil {
			return s.lookup(keyID), nil
		}
		// Every waiter gets its own copy: the verifier annotates the error
		// with its request's labels.
		if we := AsWBAError(call.err); we != nil {
			c := *we
			return nil, &c
		}
		return nil, call.err
	}
	return call.entry.lookup(keyID), nil
}

func (d *DirectoryResolver) refresh(ctx context.Context, origin string, call *directoryCall) {
	defer close(call.done)
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	resp, err := d.fetch(ctx, origin+DirectoryPath)
	var keys map[string]JWK
	var sigExpiry time.Time
	switch {
	case err != nil:
	case resp == nil || resp.Status != http.StatusOK:
		// Any status other than 200 is a discovery failure (WBA §5.5).
		err = errors.New("key directory fetch did not return 200")
	default:
		keys, sigExpiry, err = verifyDirectoryResponse(origin, resp, d.clock())
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pending, origin)
	now := d.clock()
	if err != nil {
		// A failure says nothing about the signer: it never evicts the
		// cached directory, whose keys keep verifying until they expire.
		d.failures[origin] = now.Add(discoveryNegativeTTL)
		if len(d.failures) > d.maxOrigins {
			for o, until := range d.failures {
				if !now.Before(until) {
					delete(d.failures, o)
				}
			}
			for o := range d.failures {
				if len(d.failures) <= d.maxOrigins {
					break
				}
				if o != origin {
					delete(d.failures, o)
				}
			}
		}
		call.err = discoveryFailure("key directory could not be resolved", err)
		return
	}
	delete(d.failures, origin)
	refreshAt := now.Add(directoryCacheTTL)
	expires := now.Add(d.maxStale)
	expires = minTime(expires, sigExpiry)
	refreshAt = minTime(refreshAt, expires)
	e := &directoryEntry{origin: origin, resp: resp, keys: keys, refreshAt: refreshAt, expires: expires}
	if old := d.cache[origin]; old != nil {
		e.elem = old.elem
	}
	d.store(e)
	call.entry = e
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// store inserts or refreshes e and evicts the least recently used origins
// beyond MaxOrigins. Called with mu held.
func (d *DirectoryResolver) store(e *directoryEntry) {
	if e.elem == nil {
		e.elem = d.lru.PushFront(e.origin)
	} else {
		d.lru.MoveToFront(e.elem)
	}
	d.cache[e.origin] = e
	for d.lru.Len() > d.maxOrigins {
		back := d.lru.Back()
		d.lru.Remove(back)
		delete(d.cache, back.Value.(string))
	}
}

// Evidence returns the last directory response that resolved for origin,
// the signed record of what the verifier fetched,
// or nil.
func (d *DirectoryResolver) Evidence(origin string) *DirectoryResponse {
	origin, err := webBotAuthOrigin(origin)
	if err != nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.cache[origin]; e != nil {
		return e.resp
	}
	return nil
}
