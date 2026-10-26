package signing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestRevocationDetailsMergeAndExposure(t *testing.T) {
	early, late := ts("2026-04-17T09:00:00Z"), ts("2026-04-18T09:00:00Z")
	cases := []struct {
		name       string
		old, newer RevocationDetails
		want       RevocationDetails
	}{
		{"keyCompromise wins over a later weaker reason", RevocationDetails{Reason: RevocationReasonKeyCompromise}, RevocationDetails{Reason: RevocationReasonSuperseded}, RevocationDetails{Reason: RevocationReasonKeyCompromise}},
		{"keyCompromise upgrades an earlier reason", RevocationDetails{Reason: RevocationReasonSuperseded}, RevocationDetails{Reason: RevocationReasonKeyCompromise}, RevocationDetails{Reason: RevocationReasonKeyCompromise}},
		{"a reason is never withdrawn", RevocationDetails{Reason: RevocationReasonSuperseded}, RevocationDetails{}, RevocationDetails{Reason: RevocationReasonSuperseded}},
		{"the earliest invalidity date wins", RevocationDetails{InvalidityDate: late}, RevocationDetails{InvalidityDate: early}, RevocationDetails{InvalidityDate: early}},
		{"a date is never withdrawn", RevocationDetails{InvalidityDate: early}, RevocationDetails{InvalidityDate: late}, RevocationDetails{InvalidityDate: early}},
		{"a date is added", RevocationDetails{}, RevocationDetails{InvalidityDate: late}, RevocationDetails{InvalidityDate: late}},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, tc.old.Merge(tc.newer), tc.name)
	}
	compromised := RevocationDetails{Reason: RevocationReasonKeyCompromise, InvalidityDate: early}
	require.False(t, compromised.Exposes(early.Add(-time.Second)))
	require.True(t, compromised.Exposes(*early))
	require.True(t, compromised.Exposes(late.Add(time.Hour)))
	require.True(t, RevocationDetails{Reason: RevocationReasonKeyCompromise}.Exposes(time.Unix(0, 0)), "no date: any time")
	require.False(t, RevocationDetails{Reason: RevocationReasonSuperseded}.Exposes(*late))
	require.False(t, RevocationDetails{}.Exposes(*late))
}

func TestParseRevocationList(t *testing.T) {
	issuer := "https://gov.example.com"
	base := func(extra string) string {
		return `{"version":1,"issuer":"https://gov.example.com","updated":"2026-04-18T14:00:00Z","next_update":"2026-04-18T14:15:00Z","revoked_kids":["k1","k2"]` + extra + `}`
	}
	l, err := ParseRevocationList([]byte(base(`,"revocation_details":{"k1":{"reason":"keyCompromise","invalidity_date":"2026-04-17T09:00:00Z"},"k2":{"reason":"superseded"}}`)), issuer)
	require.NoError(t, err)
	require.True(t, l.IsRevoked("k1"))
	require.False(t, l.IsRevoked("k3"))
	require.Equal(t, RevocationReasonKeyCompromise, l.Details["k1"].Reason)
	require.Equal(t, *ts("2026-04-17T09:00:00Z"), *l.Details["k1"].InvalidityDate)
	require.Nil(t, l.Details["k2"].InvalidityDate)

	// The issuer compares by canonical origin.
	_, err = ParseRevocationList([]byte(strings.Replace(base(""), `"https://gov.example.com"`, `"HTTPS://Gov.Example.com:443/"`, 1)), issuer)
	require.NoError(t, err)

	for name, doc := range map[string]string{
		"detail for an unrevoked kid":  base(`,"revocation_details":{"k9":{"reason":"superseded"}}`),
		"unknown reason":               base(`,"revocation_details":{"k1":{"reason":"oops"}}`),
		"date without a timezone":      base(`,"revocation_details":{"k1":{"invalidity_date":"2026-04-17T09:00:00"}}`),
		"detail is not an object":      base(`,"revocation_details":{"k1":"keyCompromise"}`),
		"other issuer":                 strings.Replace(base(""), "gov.example.com", "evil.example.com", 1),
		"issuer with userinfo":         strings.Replace(base(""), "https://gov", "https://u@gov", 1),
		"cadence below the 60 s floor": strings.Replace(base(""), "14:15:00Z", "14:00:30Z", 1),
		"missing version":              strings.Replace(base(""), `"version":1,`, ``, 1),
		"zero version":                 strings.Replace(base(""), `"version":1`, `"version":0`, 1),
		"kids not strings":             strings.Replace(base(""), `["k1","k2"]`, `[1]`, 1),
		"duplicate JSON key":           strings.Replace(base(""), `"version":1`, `"version":1,"version":2`, 1),
		"bad timestamp":                strings.Replace(base(""), "2026-04-18T14:00:00Z", "yesterday", 1),
	} {
		_, err := ParseRevocationList([]byte(doc), issuer)
		require.Error(t, err, name)
	}
}

// TestRevocationListRetainNeverUnrevokes: revocations are monotonic.
func TestRevocationListRetainNeverUnrevokes(t *testing.T) {
	issuer := "https://gov.example.com"
	old, err := ParseRevocationList([]byte(`{"version":1,"issuer":"https://gov.example.com","updated":"2026-04-18T14:00:00Z","next_update":"2026-04-18T14:15:00Z","revoked_kids":["k1"],"revoked_jtis":["j1"],"revocation_details":{"k1":{"reason":"keyCompromise","invalidity_date":"2026-04-17T09:00:00Z"}}}`), issuer)
	require.NoError(t, err)
	newer, err := ParseRevocationList([]byte(`{"version":1,"issuer":"https://gov.example.com","updated":"2026-04-18T14:15:00Z","next_update":"2026-04-18T14:30:00Z","revoked_kids":["k2"],"revocation_details":{}}`), issuer)
	require.NoError(t, err)
	require.NoError(t, newer.Retain(old))
	require.True(t, newer.IsRevoked("k1"), "a revocation is never withdrawn")
	require.True(t, newer.IsRevoked("k2"))
	require.Contains(t, newer.RevokedJTIs, "j1")
	require.Equal(t, RevocationReasonKeyCompromise, newer.Details["k1"].Reason)
	other := &RevocationList{Issuer: "https://other.example.com"}
	require.Error(t, newer.Retain(other))
}

func TestVerifyRevocationJWSSerializations(t *testing.T) {
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)
	resolve := func(kid string) (*JWK, error) {
		if kid == jwk.Kid {
			k := jwk
			return &k, nil
		}
		return nil, nil
	}
	payload := []byte(`{"version":1}`)
	header := map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid}
	prot, pay, sig := signJWS(t, key, header, payload)
	general, _ := json.Marshal(map[string]any{"payload": pay, "signatures": []map[string]string{{"protected": prot, "signature": sig}}})
	flattened, _ := json.Marshal(map[string]any{"payload": pay, "protected": prot, "signature": sig})
	for name, body := range map[string][]byte{"compact": []byte(prot + "." + pay + "." + sig), "general": general, "flattened": flattened} {
		got, err := verifyRevocationJWS(body, resolve)
		require.NoError(t, err, name)
		require.Equal(t, payload, got, name)
	}
	mk := func(h map[string]any) []byte {
		p, pl, s := signJWS(t, key, h, payload)
		return []byte(p + "." + pl + "." + s)
	}
	ambiguous, _ := json.Marshal(map[string]any{"payload": pay, "protected": prot, "signature": sig, "signatures": []map[string]string{{"protected": prot, "signature": sig}}})
	twoSigs, _ := json.Marshal(map[string]any{"payload": pay, "signatures": []map[string]string{{"protected": prot, "signature": sig}, {"protected": prot, "signature": sig}}})
	tampered := []byte(prot + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"version":2}`)) + "." + sig)
	for name, body := range map[string][]byte{
		"wrong typ":      mk(map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": jwk.Kid}),
		"alg none":       mk(map[string]any{"alg": "none", "typ": RevocationListTyp, "kid": jwk.Kid}),
		"alg mismatch":   mk(map[string]any{"alg": "ES256", "typ": RevocationListTyp, "kid": jwk.Kid}),
		"crit":           mk(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "crit": []string{"b64"}}),
		"no kid":         mk(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp}),
		"unknown kid":    mk(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": "someone-else"}),
		"tampered":       tampered,
		"ambiguous JSON": ambiguous,
		"two signatures": twoSigs,
		"not a JWS":      []byte("hello"),
		"empty segment":  []byte(prot + ".." + sig),
	} {
		_, err := verifyRevocationJWS(body, resolve)
		require.Error(t, err, name)
	}
}

func TestOriginRevocationCheckerLifecycle(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	rig := newWBARig(t)
	rig.resolver = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: clock})
	checker, err := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: rig.resolver, Fetch: rig.revs.fetch, Clock: clock})
	require.NoError(t, err)
	ctx := context.Background()
	jwk, _ := PublicDirectoryKey(rig.key)
	oldKid := strings.Repeat("o", 43) // thumbprint-shaped
	updated := now.Add(-time.Minute)
	rig.revs.set(t, revocationPayload{Updated: rfc3339(updated), NextUpdate: rfc3339(updated.Add(5 * time.Minute)), RevokedKids: []string{oldKid, "operator-chosen-kid"},
		Details: map[string]map[string]string{oldKid: {"reason": "keyCompromise", "invalidity_date": "2026-01-01T00:00:00Z"}}})

	revoked, err := revokedWBAKey(checker, ctx, rig.origin, jwk.Kid)
	require.NoError(t, err)
	require.False(t, revoked)
	revoked, err = revokedWBAKey(checker, ctx, rig.origin, oldKid)
	require.NoError(t, err)
	require.True(t, revoked)
	require.Equal(t, 1, rig.revs.fetches, "served from cache before next_update")
	require.Equal(t, []string{oldKid}, checker.RevokedKids(rig.origin), "only thumbprint-shaped kids, which a WBA keyid can match, are retained")
	d, ok := checker.Details(rig.origin, oldKid)
	require.True(t, ok)
	require.True(t, d.Exposes(now))

	// After next_update the checker refreshes. The new list drops old-kid
	// and weakens its reason; neither sticks.
	now = now.Add(5 * time.Minute)
	updated = now.Add(-10 * time.Second)
	rig.revs.set(t, revocationPayload{Updated: rfc3339(updated), NextUpdate: rfc3339(updated.Add(5 * time.Minute)), RevokedKids: []string{jwk.Kid},
		Details: map[string]map[string]string{jwk.Kid: {"reason": "superseded"}}})
	revoked, err = revokedWBAKey(checker, ctx, rig.origin, oldKid)
	require.NoError(t, err)
	require.True(t, revoked, "an origin cannot un-revoke a key")
	revoked, _ = revokedWBAKey(checker, ctx, rig.origin, jwk.Kid)
	require.True(t, revoked)
	d, _ = checker.Details(rig.origin, oldKid)
	require.Equal(t, RevocationReasonKeyCompromise, d.Reason)
	require.ElementsMatch(t, []string{oldKid, jwk.Kid}, checker.RevokedKids(rig.origin))
	require.True(t, slices.IsSorted(checker.RevokedKids(rig.origin)))

	// A list older than the cached one is refused (no rollback); within
	// grace the cached list still serves, past grace checks fail closed.
	now = now.Add(5 * time.Minute)
	rig.revs.set(t, revocationPayload{Updated: rfc3339(updated.Add(-time.Hour)), NextUpdate: rfc3339(now.Add(time.Hour))})
	revoked, err = revokedWBAKey(checker, ctx, rig.origin, jwk.Kid)
	require.NoError(t, err)
	require.True(t, revoked)
	now = now.Add(11 * time.Minute) // past next_update + 2 x 5 min
	_, err = revokedWBAKey(checker, ctx, rig.origin, strings.Repeat("n", 43))
	requireRevocationState(t, err, RevocationStale)
	revoked, err = revokedWBAKey(checker, ctx, rig.origin, jwk.Kid)
	require.NoError(t, err)
	require.True(t, revoked, "past grace, what was revoked stays revoked")
	require.Equal(t, `"v1"`, rig.revs.lastINM, "refreshes are conditional")
}

func TestOriginRevocationCheckerNotModifiedDoesNotExtend(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	rig := newWBARig(t)
	rig.resolver = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: clock})
	checker, _ := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: rig.resolver, Fetch: rig.revs.fetch, Clock: clock})
	updated := now.Add(-time.Minute)
	rig.revs.set(t, revocationPayload{Updated: rfc3339(updated), NextUpdate: rfc3339(updated.Add(2 * time.Minute))})
	_, err := revokedWBAKey(checker, context.Background(), rig.origin, "k")
	require.NoError(t, err)
	rig.revs.mu.Lock()
	rig.revs.status = http.StatusNotModified
	rig.revs.mu.Unlock()
	for range 6 { // 304 every minute; never extends signed freshness
		now = now.Add(time.Minute + time.Second)
		_, err = revokedWBAKey(checker, context.Background(), rig.origin, "k")
	}
	requireRevocationState(t, err, RevocationStale)
}

func TestOriginRevocationCheckerAdmission(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	dirs := newFakeDirectories()
	resolver := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: clock})
	servers := map[string]*revocationServer{}
	var mu sync.Mutex
	fetch := func(ctx context.Context, url, inm, ims string) (*RevocationFetchResult, error) {
		mu.Lock()
		s := servers[strings.TrimSuffix(url, RevocationListPath)]
		mu.Unlock()
		if s == nil {
			return &RevocationFetchResult{Status: http.StatusNotFound}, nil
		}
		return s.fetch(ctx, url, inm, ims)
	}
	revokedKid := strings.Repeat("r", 43)
	publish := func(origin string, revoked ...string) {
		key := newEd25519(t)
		dirs.publish(t, origin, now.Add(-time.Minute), time.Hour, key)
		s := newRevocationServer(t, origin, key)
		s.set(t, revocationPayload{Updated: rfc3339(now.Add(-time.Minute)), NextUpdate: rfc3339(now.Add(30 * time.Minute)), RevokedKids: revoked})
		mu.Lock()
		servers[origin] = s
		mu.Unlock()
	}
	checker, _ := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: resolver, Fetch: fetch, Clock: clock, MaxOrigins: 3, MaxOriginsPerOperator: 2})
	ctx := context.Background()
	admitted := func() int { checker.mu.Lock(); defer checker.mu.Unlock(); return len(checker.origins) }

	// A failed first discovery (no list published) costs a cooldown, not an
	// admission slot.
	for range 3 {
		_, err := revokedWBAKey(checker, ctx, "https://missing.example.net", "k")
		requireRevocationState(t, err, RevocationNoList)
	}
	require.Zero(t, admitted())

	publish("https://a.example.com", revokedKid)
	for _, o := range []string{"https://b.example.com", "https://c.example.com", "https://d.example.org", "https://e.example.org", "https://f.example.edu"} {
		publish(o)
	}
	revoked, err := revokedWBAKey(checker, ctx, "https://a.example.com", revokedKid)
	require.NoError(t, err)
	require.True(t, revoked)
	for _, o := range []string{"https://b.example.com", "https://c.example.com"} {
		_, err := revokedWBAKey(checker, ctx, o, "k")
		require.NoError(t, err, "an operator over its quota evicts its own oldest origin, never locks out")
	}
	require.Equal(t, 2, admitted())
	for _, o := range []string{"https://d.example.org", "https://e.example.org", "https://f.example.edu"} {
		_, err := revokedWBAKey(checker, ctx, o, "k")
		require.NoError(t, err, "a full checker evicts its least recently used origin")
	}
	require.Equal(t, 3, admitted())

	// The evicted origin's revocations are retained: an attacker who
	// regains it cannot serve an older list to un-revoke a key.
	mu.Lock()
	servers["https://a.example.com"].set(t, revocationPayload{Updated: rfc3339(now.Add(-time.Hour)), NextUpdate: rfc3339(now.Add(time.Hour))})
	mu.Unlock()
	revoked, err = revokedWBAKey(checker, ctx, "https://a.example.com", revokedKid)
	require.NoError(t, err)
	require.True(t, revoked, "history survives eviction")
	require.Equal(t, []string{revokedKid}, checker.RevokedKids("https://a.example.com"))
	// Past next_update the checker refetches; the older list is refused, the
	// key stays revoked within the grace window, then checks fail closed.
	now = now.Add(31 * time.Minute)
	revoked, err = revokedWBAKey(checker, ctx, "https://a.example.com", revokedKid)
	require.NoError(t, err)
	require.True(t, revoked, "an older list cannot un-revoke the key")
	now = now.Add(61 * time.Minute) // past next_update + 2 x interval
	_, err = revokedWBAKey(checker, ctx, "https://a.example.com", "k")
	requireRevocationState(t, err, RevocationStale)
	revoked, err = revokedWBAKey(checker, ctx, "https://a.example.com", revokedKid)
	require.NoError(t, err)
	require.True(t, revoked, "a stale list still revokes what it revoked")

	// A list over the retained-entry bound fails that origin closed.
	many := make([]string, maxRetainedRevocations+1)
	for i := range many {
		many[i] = fmt.Sprintf("%043d", i)
	}
	publish("https://g.example.info", many...)
	_, err = revokedWBAKey(checker, ctx, "https://g.example.info", "k")
	requireRevocationState(t, err, RevocationStale)

	// A list signed by a key outside the origin's directory is refused.
	dirs2 := newFakeDirectories()
	dirs2.publish(t, "https://x.example.com", now.Add(-time.Minute), time.Hour, newEd25519(t))
	s := newRevocationServer(t, "https://x.example.com", newEd25519(t))
	s.set(t, revocationPayload{Updated: rfc3339(now.Add(-time.Minute)), NextUpdate: rfc3339(now.Add(30 * time.Minute))})
	c2, _ := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs2.fetch, Clock: clock}), Fetch: s.fetch, Clock: clock})
	_, err = revokedWBAKey(c2, ctx, "https://x.example.com", "k")
	requireRevocationState(t, err, RevocationStale)

	_, err = NewOriginRevocationChecker(OriginRevocationCheckerOptions{})
	require.Error(t, err)
}

// TestOriginRevocationRefreshIsDetachedFromTheCaller: a caller that gives
// up does not abort the shared refresh, and the refresh holds no lock
// across I/O, so a concurrent check returns as soon as it completes.
func TestOriginRevocationRefreshIsDetachedFromTheCaller(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	rig := newWBARig(t)
	rig.resolver = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: clock})
	release := make(chan struct{})
	var fetches atomic.Int32
	fetch := func(ctx context.Context, url, inm, ims string) (*RevocationFetchResult, error) {
		fetches.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return rig.revs.fetch(ctx, url, inm, ims)
	}
	checker, _ := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: rig.resolver, Fetch: fetch, Clock: clock})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := revokedWBAKey(checker, ctx, rig.origin, "k"); done <- err }()
	require.Eventually(t, func() bool { return fetches.Load() == 1 }, time.Second, time.Millisecond)
	// While the fetch is blocked, other callers are not stuck behind a lock:
	// a second check honors its own deadline and inspection returns at once.
	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	start := time.Now()
	_, err := revokedWBAKey(checker, short, rig.origin, "k")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	require.Empty(t, checker.RevokedKids(rig.origin))
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(release)
	// The shared discovery completed despite the cancellation.
	require.Eventually(t, func() bool {
		_, err := revokedWBAKey(checker, context.Background(), rig.origin, "k")
		return err == nil
	}, time.Second, time.Millisecond)
	require.Equal(t, int32(1), fetches.Load())
}

func TestMemoryRevocationHistory(t *testing.T) {
	ctx := context.Background()
	h := newMemoryRevocationHistory(100)
	kid := func(i int) string { return fmt.Sprintf("%043d", i) }
	mk := func(origin string, updated time.Time, kids ...string) *RevocationList {
		l := &RevocationList{Issuer: origin, Updated: updated, NextUpdate: updated.Add(time.Hour),
			RevokedKids: map[string]struct{}{}, RevokedJTIs: map[string]struct{}{}, Details: map[string]RevocationDetails{}}
		for _, k := range kids {
			l.RevokedKids[k] = struct{}{}
		}
		return l
	}
	o := "https://a.example.com"
	require.NoError(t, h.StoreRevocations(ctx, "https://empty.example.com", mk("https://empty.example.com", wbaTestNow)))
	got, _ := h.LoadRevocations(ctx, "https://empty.example.com")
	require.Nil(t, got, "a list that revokes nothing is not stored")

	require.NoError(t, h.StoreRevocations(ctx, o, mk(o, wbaTestNow, kid(1))))
	// A late write from an older refresh cannot roll the history back.
	require.NoError(t, h.StoreRevocations(ctx, o, mk(o, wbaTestNow.Add(-time.Hour), kid(2))))
	got, _ = h.LoadRevocations(ctx, o)
	require.Equal(t, wbaTestNow, got.Updated)
	require.Contains(t, got.RevokedKids, kid(1))
	require.Contains(t, got.RevokedKids, kid(2))
	// A newer list that drops a kid keeps it.
	require.NoError(t, h.StoreRevocations(ctx, o, mk(o, wbaTestNow.Add(time.Hour), kid(3))))
	got, _ = h.LoadRevocations(ctx, o)
	require.Len(t, got.RevokedKids, 3)

	// One operator, or one hosting platform's free subdomains, cannot flood
	// the history: past the quota its new origins are refused (they fail
	// closed), and no other origin's history is evicted.
	for _, tenant := range []func(int) string{
		func(i int) string { return fmt.Sprintf("https://t%d.attacker.example.net", i) },
		func(i int) string { return fmt.Sprintf("https://tenant%d.github.io", i) },
	} {
		for i := range historyOriginsPerOperator {
			origin := tenant(i)
			require.NoError(t, h.StoreRevocations(ctx, origin, mk(origin, wbaTestNow, kid(100+i))))
		}
		over := tenant(historyOriginsPerOperator)
		require.ErrorIs(t, h.StoreRevocations(ctx, over, mk(over, wbaTestNow, kid(999))), errHistoryFull)
		got, _ = h.LoadRevocations(ctx, tenant(0))
		require.NotNil(t, got, "the flooding group's own earlier entries are kept")
	}
	got, _ = h.LoadRevocations(ctx, o)
	require.NotNil(t, got, "another operator's history is never evicted")
	require.Len(t, got.RevokedKids, 3)
}

// refusingHistory wraps the memory history and can be made to refuse.
type refusingHistory struct {
	inner   *memoryRevocationHistory
	refuse  atomic.Bool
	loadErr atomic.Bool
}

func (h *refusingHistory) LoadRevocations(ctx context.Context, origin string) (*RevocationList, error) {
	if h.loadErr.Load() {
		return nil, errors.New("store down")
	}
	return h.inner.LoadRevocations(ctx, origin)
}

func (h *refusingHistory) StoreRevocations(ctx context.Context, origin string, l *RevocationList) error {
	if h.refuse.Load() {
		return errHistoryFull
	}
	return h.inner.StoreRevocations(ctx, origin, l)
}

// TestOriginRevocationCheckerFailsClosedWhenHistoryRefuses: an origin whose
// revocations the History cannot keep (over its quota, or a store outage)
// is stale (no spend) rather than current, its verified list keeps revoking,
// and it recovers once the History accepts it. A History that cannot be
// read makes the state unavailable, never "nothing retained".
func TestOriginRevocationCheckerFailsClosedWhenHistoryRefuses(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	rig := newWBARig(t)
	rig.resolver = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: clock})
	hist := &refusingHistory{inner: newMemoryRevocationHistory(16)}
	checker, err := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: rig.resolver, Fetch: rig.revs.fetch, Clock: clock, History: hist})
	require.NoError(t, err)
	ctx := context.Background()
	kid := strings.Repeat("o", 43)
	unrevoked := strings.Repeat("u", 43)
	publish := func() {
		updated := now.Add(-10 * time.Second)
		rig.revs.set(t, revocationPayload{Updated: rfc3339(updated), NextUpdate: rfc3339(updated.Add(5 * time.Minute)), RevokedKids: []string{kid}})
	}

	// Discovery while the History refuses: stale, so no spend, but the
	// verified list is kept and keeps revoking its keys.
	hist.refuse.Store(true)
	publish()
	_, err = revokedWBAKey(checker, ctx, rig.origin, unrevoked)
	requireRevocationState(t, err, RevocationStale)
	revoked, err := revokedWBAKey(checker, ctx, rig.origin, kid)
	require.NoError(t, err)
	require.True(t, revoked, "a verified list the History refused still revokes")

	// Persisted on the next refresh once the History accepts it.
	hist.refuse.Store(false)
	now = now.Add(revocationMinInterval + time.Second)
	publish()
	state, err := checker.CheckWBAKey(ctx, rig.origin, unrevoked)
	require.NoError(t, err)
	require.Equal(t, RevocationCurrent, state)

	// An admitted origin whose next list cannot be stored fails closed too.
	hist.refuse.Store(true)
	now = now.Add(5 * time.Minute)
	publish()
	_, err = revokedWBAKey(checker, ctx, rig.origin, unrevoked)
	requireRevocationState(t, err, RevocationStale)

	// It recovers on the next refresh once the store succeeds, even if the
	// list is unchanged (a 304 retries the store).
	hist.refuse.Store(false)
	rig.revs.mu.Lock()
	rig.revs.status = http.StatusNotModified
	rig.revs.mu.Unlock()
	now = now.Add(revocationMinInterval + time.Second)
	revoked, err = revokedWBAKey(checker, ctx, rig.origin, kid)
	require.NoError(t, err)
	require.True(t, revoked)

	// A History read failure for an origin not held in memory.
	failing := &refusingHistory{inner: newMemoryRevocationHistory(16)}
	failing.loadErr.Store(true)
	c2, err := NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: rig.resolver, Fetch: rig.revs.fetch, Clock: clock, History: failing})
	require.NoError(t, err)
	_, err = revokedWBAKey(c2, ctx, rig.origin, kid)
	requireRevocationState(t, err, RevocationUnavailable)
}

// revocationStateError carries a CheckWBAKey state other than revoked or
// current.
type revocationStateError struct {
	state RevocationState
	err   error
}

func (e *revocationStateError) Error() string { return e.state.String() }
func (e *revocationStateError) Unwrap() error { return e.err }

// revokedWBAKey adapts CheckWBAKey to (revoked, error) for the tests.
func revokedWBAKey(c *OriginRevocationChecker, ctx context.Context, origin, kid string) (bool, error) {
	state, err := c.CheckWBAKey(ctx, origin, kid)
	switch state {
	case RevocationRevoked:
		return true, nil
	case RevocationCurrent:
		return false, nil
	}
	return false, &revocationStateError{state: state, err: err}
}

func requireRevocationState(t *testing.T, err error, want RevocationState) {
	t.Helper()
	var se *revocationStateError
	require.ErrorAs(t, err, &se)
	require.Equal(t, want, se.state, "%v", se.err)
}
