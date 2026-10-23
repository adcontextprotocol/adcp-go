package signing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignKeyDirectoryRoundTrip(t *testing.T) {
	keys := []any{newEd25519(t), newEd25519(t)}
	resp, err := SignKeyDirectory("https://buyer.example.com/agent", keys, wbaTestNow, time.Hour)
	require.NoError(t, err)
	require.Equal(t, DirectoryMediaType, resp.Header.Get("Content-Type"))
	require.Equal(t, "max-age=300", resp.Header.Get("Cache-Control"))
	require.NotContains(t, string(resp.Body), `"d"`)
	got, err := VerifyDirectoryResponse("https://buyer.example.com", resp, wbaTestNow)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, k := range keys {
		jwk, _ := PublicDirectoryKey(k)
		require.Contains(t, got, jwk.Kid)
	}
	in, _ := parseSFDictionary(resp.Header.Get("Signature-Input"))
	require.Equal(t, []string{"key1", "key2"}, in.keys())

	_, err = SignKeyDirectory("https://buyer.example.com", nil, wbaTestNow, time.Hour)
	require.Error(t, err)
	k := newEd25519(t)
	_, err = SignKeyDirectory("https://buyer.example.com", []any{k, k}, wbaTestNow, time.Hour)
	require.Error(t, err, "duplicate keys")
	_, err = SignKeyDirectory("http://buyer.example.com", []any{k}, wbaTestNow, time.Hour)
	require.Error(t, err)
	// ES256 is not a Web Bot Auth algorithm.
	_, err = SignKeyDirectory("https://buyer.example.com", []any{newP256(t)}, wbaTestNow, time.Hour)
	require.Error(t, err)
}

func TestVerifyDirectoryResponseRejectsTampering(t *testing.T) {
	origin := "https://buyer.example.com"
	keyA, keyB := newEd25519(t), newEd25519(t)
	fresh := func() *DirectoryResponse {
		resp, err := SignKeyDirectory(origin, []any{keyA, keyB}, wbaTestNow.Add(-time.Minute), time.Hour)
		require.NoError(t, err)
		return resp
	}
	resign := func(resp *DirectoryResponse, body []byte) {
		resp.Body = body
		resp.Header.Set("Content-Digest", computeSHA256DigestHeaderForEncoding(body, BinaryEncodingRFC8941))
	}
	cases := []struct {
		name   string
		mutate func(*DirectoryResponse)
		origin string
		now    time.Time
	}{
		{"body changed", func(r *DirectoryResponse) { r.Body = []byte(strings.Replace(string(r.Body), `"sig"`, `"enc"`, 1)) }, origin, wbaTestNow},
		{"body and digest changed", func(r *DirectoryResponse) {
			var doc struct{ Keys []JWK }
			_ = json.Unmarshal(r.Body, &doc)
			doc.Keys = doc.Keys[:1]
			b, _ := json.Marshal(doc)
			resign(r, b)
		}, origin, wbaTestNow},
		{"one of two signatures removed from Signature only", func(r *DirectoryResponse) {
			r.Header.Set("Signature", strings.SplitN(r.Header.Get("Signature"), ", key2=", 2)[0])
		}, origin, wbaTestNow},
		{"signed but digest missing", func(r *DirectoryResponse) { r.Header.Del("Content-Digest") }, origin, wbaTestNow},
		{"Signature-Input without Signature", func(r *DirectoryResponse) { r.Header.Del("Signature") }, origin, wbaTestNow},
		{"only unusable keys", func(r *DirectoryResponse) {
			resign(r, []byte(`{"keys":[{"kty":"EC","crv":"P-384","x":"AAAA","y":"AAAA"}]}`))
			r.Header.Del("Signature")
			r.Header.Del("Signature-Input")
		}, origin, wbaTestNow},
		{"private member in another case", func(r *DirectoryResponse) {
			resign(r, []byte(strings.Replace(string(r.Body), `"use":"sig"`, `"use":"sig","D":"AAAA"`, 1)))
			r.Header.Del("Signature")
			r.Header.Del("Signature-Input")
		}, origin, wbaTestNow},
		{"only a non-canonical (padded) key encoding", func(r *DirectoryResponse) {
			jwk, _ := PublicDirectoryKey(keyA)
			resign(r, []byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","x":"`+jwk.X+`="}]}`))
			r.Header.Del("Signature")
			r.Header.Del("Signature-Input")
		}, origin, wbaTestNow},
		{"symmetric key material", func(r *DirectoryResponse) {
			resign(r, []byte(`{"keys":[{"kty":"oct","k":"c2VjcmV0"}]}`))
			r.Header.Del("Signature")
			r.Header.Del("Signature-Input")
		}, origin, wbaTestNow},
		{"signature labels differ", func(r *DirectoryResponse) {
			r.Header.Set("Signature", strings.Replace(r.Header.Get("Signature"), "key2=", "key9=", 1))
		}, origin, wbaTestNow},
		{"wrong media type", func(r *DirectoryResponse) { r.Header.Set("Content-Type", "text/html") }, origin, wbaTestNow},
		// WBA §5.5.1: a directory MUST be served as
		// application/http-message-signatures-directory+json.
		{"plain JWK Set media type", func(r *DirectoryResponse) { r.Header.Set("Content-Type", "application/jwk-set+json") }, origin, wbaTestNow},
		{"plain JSON media type", func(r *DirectoryResponse) { r.Header.Set("Content-Type", "application/json") }, origin, wbaTestNow},
		{"no media type", func(r *DirectoryResponse) { r.Header.Del("Content-Type") }, origin, wbaTestNow},
		// RFC 9421 §4.1 and §4.2, under any label, as http-message-sig parses.
		{"a Signature member that is not a byte sequence", func(r *DirectoryResponse) {
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`, key9=("@authority";req "content-digest");created=1;expires=2;keyid="unknown";tag="`+DirectoryTag+`"`)
			r.Header.Set("Signature", r.Header.Get("Signature")+", key9=token")
		}, origin, wbaTestNow},
		{"a Signature-Input member that is not an inner list", func(r *DirectoryResponse) {
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`, key9=token;tag="`+DirectoryTag+`"`)
			r.Header.Set("Signature", r.Header.Get("Signature")+", key9=:AAAA:")
		}, origin, wbaTestNow},
		{"not 200", func(r *DirectoryResponse) { r.Status = http.StatusNotModified }, origin, wbaTestNow},
		{"oversized", func(r *DirectoryResponse) { r.Body = make([]byte, directoryMaxBytes+1) }, origin, wbaTestNow},
		{"served for another origin", func(*DirectoryResponse) {}, "https://evil.example.com", wbaTestNow},
		{"signatures expired", func(*DirectoryResponse) {}, origin, wbaTestNow.Add(2 * time.Hour)},
		{"signatures created in the future (no skew)", func(*DirectoryResponse) {}, origin, wbaTestNow.Add(-2 * time.Minute)},
		{"wrong tag", func(r *DirectoryResponse) {
			r.Header.Set("Signature-Input", strings.ReplaceAll(r.Header.Get("Signature-Input"), DirectoryTag, ProfileTagWebBotAuth))
		}, origin, wbaTestNow},
		{"authority not covered", func(r *DirectoryResponse) {
			r.Header.Set("Signature-Input", strings.ReplaceAll(r.Header.Get("Signature-Input"), `"@authority";req `, ``))
		}, origin, wbaTestNow},
		{"private key member", func(r *DirectoryResponse) {
			resign(r, []byte(strings.Replace(string(r.Body), `"use":"sig"`, `"use":"sig","d":"AAAA"`, 1)))
		}, origin, wbaTestNow},
		{"kid is not the thumbprint", func(r *DirectoryResponse) {
			var doc struct{ Keys []JWK }
			_ = json.Unmarshal(r.Body, &doc)
			doc.Keys[0].Kid = "operator-chosen"
			b, _ := json.Marshal(doc)
			resign(r, b)
		}, origin, wbaTestNow},
		{"duplicate JSON key", func(r *DirectoryResponse) {
			resign(r, []byte(`{"keys":[],"keys":[]}`))
		}, origin, wbaTestNow},
		{"no keys", func(r *DirectoryResponse) { resign(r, []byte(`{"keys":[]}`)) }, origin, wbaTestNow},
		// The profile requires signed directories, one signature per key
		// (WBA Appendix B.1).
		{"unsigned directory", func(r *DirectoryResponse) {
			r.Header.Del("Signature")
			r.Header.Del("Signature-Input")
			r.Header.Del("Content-Digest")
		}, origin, wbaTestNow},
		{"unsigned directory with a digest", func(r *DirectoryResponse) {
			r.Header.Del("Signature")
			r.Header.Del("Signature-Input")
		}, origin, wbaTestNow},
		{"one key without a signature", func(r *DirectoryResponse) {
			r.Header.Set("Signature-Input", strings.SplitN(r.Header.Get("Signature-Input"), ", key2=", 2)[0])
			r.Header.Set("Signature", strings.SplitN(r.Header.Get("Signature"), ", key2=", 2)[0])
		}, origin, wbaTestNow},
		{"only a P-256 key (ES256 is not a Web Bot Auth algorithm)", func(r *DirectoryResponse) {
			p256, _ := jwkFromPublicKey(&newP256(t).PublicKey)
			*r = *signDirectoryBody(t, origin, []byte(`{"keys":[`+mustJSON(t, p256)+`]}`))
		}, origin, wbaTestNow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := fresh()
			tc.mutate(resp)
			_, err := VerifyDirectoryResponse(tc.origin, resp, tc.now)
			require.Error(t, err)
		})
	}
	_, err := VerifyDirectoryResponse(origin, fresh(), wbaTestNow)
	require.NoError(t, err)

	// Accepted: media type parameters, and keys the profile cannot use (here
	// P-384 and P-256), which are skipped and need no signature. A signature
	// naming a skipped key is not read.
	jwkA, _ := PublicDirectoryKey(keyA)
	p256, _ := jwkFromPublicKey(&newP256(t).PublicKey)
	p256Kid, err := JWKThumbprint(p256)
	require.NoError(t, err)
	accepted := map[string]func(*DirectoryResponse){
		"media type with a parameter": func(r *DirectoryResponse) { r.Header.Set("Content-Type", DirectoryMediaType+"; charset=utf-8") },
		"an unusable key is skipped": func(r *DirectoryResponse) {
			*r = *signDirectoryBody(t, origin, []byte(`{"keys":[{"kty":"EC","crv":"P-384","x":"AAAA","y":"AAAA"},{"kty":"OKP","crv":"Ed25519","x":"`+jwkA.X+`","kid":"operator-chosen"}]}`), keyA)
		},
		"a signature naming a key the directory does not hold is not read": func(r *DirectoryResponse) {
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`, key9=("@authority";req "content-digest");created=1;expires=2;keyid="unknown";tag="`+DirectoryTag+`"`)
			r.Header.Set("Signature", r.Header.Get("Signature")+", key9=:AAAA:")
		},
		"a signature by a skipped key is not read": func(r *DirectoryResponse) {
			*r = *signDirectoryBody(t, origin, []byte(`{"keys":[`+mustJSON(t, p256)+`,{"kty":"OKP","crv":"Ed25519","x":"`+jwkA.X+`"}]}`), keyA)
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`, key9=("@authority";req "content-digest");created=1;expires=2;keyid="`+p256Kid+`";tag="`+DirectoryTag+`"`)
			r.Header.Set("Signature", r.Header.Get("Signature")+", key9=:AAAA:")
		},
	}
	for name, mutate := range accepted {
		t.Run(name, func(t *testing.T) {
			resp := fresh()
			mutate(resp)
			keys, err := VerifyDirectoryResponse(origin, resp, wbaTestNow)
			require.NoError(t, err)
			require.Contains(t, keys, jwkA.Kid, "keys are indexed by thumbprint")
		})
	}
}

func TestDirectoryPublisherResigns(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	key := newEd25519(t)
	p, err := NewDirectoryPublisher(DirectoryPublisherOptions{AgentOrigin: "https://buyer.example.com", PrivateKeys: []any{key}, Lifetime: time.Hour, Clock: clock})
	require.NoError(t, err)
	first, _ := p.Current()
	now = now.Add(20 * time.Minute)
	same, _ := p.Current()
	require.Same(t, first, same)
	now = now.Add(15 * time.Minute) // past half the lifetime
	next, _ := p.Current()
	require.NotSame(t, first, next)
	_, err = VerifyDirectoryResponse("https://buyer.example.com", next, now)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://buyer.example.com"+DirectoryPath, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, DirectoryMediaType, rec.Header().Get("Content-Type"))
	served := &DirectoryResponse{Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
	_, err = VerifyDirectoryResponse("https://buyer.example.com", served, now)
	require.NoError(t, err)

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "https://buyer.example.com"+DirectoryPath, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, rec.Body.Len())
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "https://buyer.example.com"+DirectoryPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	_, err = NewDirectoryPublisher(DirectoryPublisherOptions{AgentOrigin: "https://buyer.example.com"})
	require.Error(t, err)
}

func TestDirectoryResolverCaching(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	dirs := newFakeDirectories()
	origin := "https://buyer.example.com"
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)
	dirs.publish(t, origin, now.Add(-time.Minute), 2*time.Minute, key) // expires in 1 minute
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: clock})
	ctx := context.Background()

	got, err := d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)
	require.Equal(t, jwk.X, got.X)
	// A miss in a fresh directory is absent, with no refetch.
	got, err = d.ResolveWBAKey(ctx, origin, "unknown-kid")
	require.NoError(t, err)
	require.Nil(t, got)
	// The same origin spelled differently shares the cache.
	_, err = d.ResolveWBAKey(ctx, "https://BUYER.example.com:443/x", jwk.Kid)
	require.NoError(t, err)
	require.Equal(t, 1, dirs.count(origin))
	require.NotNil(t, d.Evidence(origin))

	// The cache never outlives the directory signatures (1 minute here,
	// under the 5-minute cap).
	now = now.Add(61 * time.Second)
	dirs.publish(t, origin, now.Add(-time.Minute), time.Hour, key)
	_, err = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)
	require.Equal(t, 2, dirs.count(origin))
	// Then at most five minutes.
	now = now.Add(4 * time.Minute)
	_, _ = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.Equal(t, 2, dirs.count(origin))
	now = now.Add(time.Minute + time.Second)
	_, _ = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.Equal(t, 3, dirs.count(origin))
}

// TestDirectoryResolverDiscoveryFailure: a directory that fails to resolve
// is not evidence and never evicts the cached entry, which keeps verifying
// until it expires (WBA §6.10); a directory that resolves without the key is
// evidence and replaces it.
func TestDirectoryResolverDiscoveryFailure(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	dirs := newFakeDirectories()
	origin := "https://buyer.example.com"
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)
	dirs.publish(t, origin, now.Add(-time.Minute), time.Hour, key)
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: clock})
	ctx := context.Background()
	_, err := d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)

	// Past the refresh point the origin goes down: the cached keys keep
	// verifying.
	now = now.Add(6 * time.Minute)
	dirs.fail(origin, errors.New("connection reset"))
	got, err := d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got, "a failed refresh does not evict the cached directory")
	require.Equal(t, 2, dirs.count(origin))
	// The failure cools down for 60 s: no refetch, still the cached keys.
	got, err = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 2, dirs.count(origin))
	// A 404 is a discovery failure too, not evidence of removal.
	now = now.Add(61 * time.Second)
	dirs.mu.Lock()
	delete(dirs.errs, origin)
	delete(dirs.dirs, origin)
	dirs.mu.Unlock()
	got, err = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 3, dirs.count(origin))

	// Once the cached directory's signatures expire, its keys stop
	// verifying: an unverified request.
	now = wbaTestNow.Add(time.Hour)
	_, err = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	we := requireWBAError(t, err, http.StatusForbidden)
	require.True(t, IsDiscoveryFailure(err))
	require.NotEmpty(t, we.ResponseHeader().Get("Accept-Signature"))
	require.NotNil(t, d.Evidence(origin), "the last directory is kept as evidence")

	// A directory that resolves without the key replaces the cached one.
	now = now.Add(61 * time.Second)
	other := newEd25519(t)
	dirs.publish(t, origin, now.Add(-time.Minute), time.Hour, other)
	got, err = d.ResolveWBAKey(ctx, origin, jwk.Kid)
	require.NoError(t, err)
	require.Nil(t, got, "a removed key stops verifying")
}

// TestDirectoryResolverSignedDirectoriesOnly: an unsigned directory is a
// discovery failure, and a signed directory keeps verifying through an
// outage only until MaxStale.
func TestDirectoryResolverSignedDirectoriesOnly(t *testing.T) {
	now := wbaTestNow
	clock := func() time.Time { return now }
	origin := "https://buyer.example.com"
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)
	plain := &DirectoryResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {DirectoryMediaType}},
		Body: []byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","x":"` + jwk.X + `"}]}`)}
	_, err := NewDirectoryResolver(DirectoryResolverOptions{Fetch: func(context.Context, string) (*DirectoryResponse, error) { return plain, nil }, Clock: clock}).
		ResolveWBAKey(context.Background(), origin, jwk.Kid)
	require.True(t, IsDiscoveryFailure(err), "an unsigned directory is refused")

	signed := signDirectoryBody(t, origin, plain.Body, key)
	var fail atomic.Bool
	fetch := func(context.Context, string) (*DirectoryResponse, error) {
		if fail.Load() {
			return nil, errors.New("timeout")
		}
		return signed, nil
	}
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: fetch, Clock: clock, MaxStale: 10 * time.Minute})
	got, err := d.ResolveWBAKey(context.Background(), origin, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got, "found by thumbprint without a kid")
	fail.Store(true)
	now = now.Add(9 * time.Minute)
	got, err = d.ResolveWBAKey(context.Background(), origin, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got)
	now = now.Add(2 * time.Minute)
	_, err = d.ResolveWBAKey(context.Background(), origin, jwk.Kid)
	require.True(t, IsDiscoveryFailure(err), "MaxStale bounds an outage")
}

func TestDirectoryResolverCoalescesPerOrigin(t *testing.T) {
	dirs := newFakeDirectories()
	slow, fast := "https://slow.example.com", "https://fast.example.com"
	slowKey, fastKey := newEd25519(t), newEd25519(t)
	dirs.publish(t, slow, wbaTestNow.Add(-time.Minute), time.Hour, slowKey)
	dirs.publish(t, fast, wbaTestNow.Add(-time.Minute), time.Hour, fastKey)
	release := make(chan struct{})
	dirs.block[slow] = release
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: wbaClock})
	slowJWK, _ := PublicDirectoryKey(slowKey)
	fastJWK, _ := PublicDirectoryKey(fastKey)

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := d.ResolveWBAKey(context.Background(), slow, slowJWK.Kid)
			if err == nil && k == nil {
				err = errors.New("missing key")
			}
			errs <- err
		}()
	}
	// A waiter that gives up does not cancel discovery for the others.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.ResolveWBAKey(ctx, slow, slowJWK.Kid)
	require.ErrorIs(t, err, context.Canceled)
	// A slow origin does not block another origin.
	k, err := d.ResolveWBAKey(context.Background(), fast, fastJWK.Kid)
	require.NoError(t, err)
	require.NotNil(t, k)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, dirs.count(slow), "concurrent lookups share one fetch")
}

func TestDirectoryResolverBounds(t *testing.T) {
	dirs := newFakeDirectories()
	keys := map[string]any{}
	for _, o := range []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"} {
		keys[o] = newEd25519(t)
		dirs.publish(t, o, wbaTestNow.Add(-time.Minute), time.Hour, keys[o])
	}
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: wbaClock, MaxOrigins: 2})
	for _, o := range []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"} {
		jwk, _ := PublicDirectoryKey(keys[o])
		_, err := d.ResolveWBAKey(context.Background(), o, jwk.Kid)
		require.NoError(t, err)
	}
	require.Nil(t, d.Evidence("https://a.example.com"), "least recently used origin evicted")
	require.NotNil(t, d.Evidence("https://c.example.com"))

	// Concurrent discoveries beyond MaxOrigins are refused with 429.
	release := make(chan struct{})
	dirs.block["https://a.example.com"] = release
	dirs.block["https://b.example.com"] = release
	d2 := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: wbaClock, MaxOrigins: 2})
	var wg sync.WaitGroup
	for _, o := range []string{"https://a.example.com", "https://b.example.com"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = d2.ResolveWBAKey(context.Background(), o, "k") }()
	}
	require.Eventually(t, func() bool {
		return dirs.count("https://a.example.com") >= 2 && dirs.count("https://b.example.com") >= 2
	}, time.Second, time.Millisecond)
	_, err := d2.ResolveWBAKey(context.Background(), "https://c.example.com", "k")
	requireWBAStatus(t, err, http.StatusForbidden)
	require.True(t, IsDiscoveryFailure(err), "a fetch the verifier's own limits refuse is a discovery failure (WBA §6.10)")
	close(release)
	wg.Wait()

	_, err = d.ResolveWBAKey(context.Background(), "http://a.example.com", "k")
	requireWBAStatus(t, err, http.StatusForbidden)
}

// TestDirectoryResolverOperatorIsolation: one operator cannot hold every
// discovery slot, and failed discoveries never evict verified directories.
func TestDirectoryResolverOperatorIsolation(t *testing.T) {
	dirs := newFakeDirectories()
	release := make(chan struct{})
	good := newEd25519(t)
	dirs.publish(t, "https://good.example.org", wbaTestNow.Add(-time.Minute), time.Hour, good)
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: wbaClock, MaxOrigins: 4, MaxPendingPerOperator: 2})
	goodJWK, _ := PublicDirectoryKey(good)
	_, err := d.ResolveWBAKey(context.Background(), "https://good.example.org", goodJWK.Kid)
	require.NoError(t, err)

	var wg sync.WaitGroup
	attackers := []string{"https://a1.attacker.example.com", "https://a2.attacker.example.com"}
	dirs.mu.Lock()
	for _, o := range attackers {
		dirs.block[o] = release
	}
	dirs.mu.Unlock()
	for _, o := range attackers {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = d.ResolveWBAKey(context.Background(), o, "k") }()
	}
	require.Eventually(t, func() bool {
		return dirs.count("https://a1.attacker.example.com") == 1 && dirs.count("https://a2.attacker.example.com") == 1
	}, time.Second, time.Millisecond)
	_, err = d.ResolveWBAKey(context.Background(), "https://a3.attacker.example.com", "k")
	requireWBAStatus(t, err, http.StatusForbidden)
	require.True(t, IsDiscoveryFailure(err))
	close(release)
	wg.Wait()

	// Many failed discoveries do not push the verified directory out.
	for i := range 10 {
		_, _ = d.ResolveWBAKey(context.Background(), fmt.Sprintf("https://f%d.example.net", i), "k")
	}
	require.NotNil(t, d.Evidence("https://good.example.org"))
	got, err := d.ResolveWBAKey(context.Background(), "https://good.example.org", goodJWK.Kid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 1, dirs.count("https://good.example.org"))
}

// TestDirectoryResolverLocalRefusals: refreshing a cached origin never
// competes with first discovery for the global slots, and when the
// resolver's own limits refuse a refresh, the cached keys stay usable for
// one more cache period only, never for MaxStale.
func TestDirectoryResolverLocalRefusals(t *testing.T) {
	// The clock is read by background discovery goroutines, so it is
	// atomic.
	var nowNanos atomic.Int64
	setNow := func(t time.Time) { nowNanos.Store(t.UnixNano()) }
	setNow(wbaTestNow)
	clock := func() time.Time { return time.Unix(0, nowNanos.Load()) }
	dirs := newFakeDirectories()
	a, b, c := "https://a.example.com", "https://b.example.com", "https://c.example.net"
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)
	for _, o := range []string{a, b, c} {
		dirs.publish(t, o, wbaTestNow.Add(-time.Minute), 24*time.Hour, key)
	}
	ctx := context.Background()

	// Global cap: one slot, held by a stalled first discovery of c.
	d := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: clock, MaxOrigins: 1})
	_, err := d.ResolveWBAKey(ctx, a, jwk.Kid)
	require.NoError(t, err)
	release := make(chan struct{})
	dirs.mu.Lock()
	dirs.block[c] = release
	dirs.mu.Unlock()
	go func() { _, _ = d.ResolveWBAKey(context.Background(), c, jwk.Kid) }()
	require.Eventually(t, func() bool { return dirs.count(c) == 1 }, time.Second, time.Millisecond)
	setNow(clock().Add(6 * time.Minute))
	got, err := d.ResolveWBAKey(ctx, a, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 2, dirs.count(a), "the cached origin was refreshed despite the full global cap")
	close(release)

	// Per-operator cap: a refused refresh serves the cache for one more
	// cache period, then fails as a discovery failure.
	setNow(wbaTestNow)
	d = NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: clock, MaxPendingPerOperator: 1})
	_, err = d.ResolveWBAKey(ctx, a, jwk.Kid)
	require.NoError(t, err)
	release = make(chan struct{})
	defer close(release)
	dirs.mu.Lock()
	dirs.block[b] = release
	dirs.mu.Unlock()
	go func() { _, _ = d.ResolveWBAKey(context.Background(), b, jwk.Kid) }()
	require.Eventually(t, func() bool { return dirs.count(b) == 1 }, time.Second, time.Millisecond)
	setNow(clock().Add(6 * time.Minute))
	got, err = d.ResolveWBAKey(ctx, a, jwk.Kid)
	require.NoError(t, err)
	require.NotNil(t, got, "within one cache period of the refresh point")
	setNow(clock().Add(5 * time.Minute))
	_, err = d.ResolveWBAKey(ctx, a, jwk.Kid)
	require.True(t, IsDiscoveryFailure(err), "a local refusal does not stretch the cache to MaxStale")
}

// TestDirectoryResolverFailureErrorsArePerRequest: concurrent waiters on one
// failed discovery each get their own error, so one request's labels never
// reach another's Accept-Signature.
func TestDirectoryResolverFailureErrorsArePerRequest(t *testing.T) {
	rig := newWBARig(t)
	release := make(chan struct{})
	rig.dirs.mu.Lock()
	rig.dirs.errs[rig.origin] = errors.New("timeout")
	rig.dirs.block[rig.origin] = release
	rig.dirs.mu.Unlock()
	rig.resolver = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: wbaClock})
	labels := []string{"alpha", "bravo", "charlie", "delta"}
	type result struct {
		label  string
		header string
	}
	results := make(chan result, len(labels))
	var wg sync.WaitGroup
	for _, label := range labels {
		r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
		require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Label: label, Clock: wbaClock}))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := VerifyWebBotAuthRequest(r, rig.opts())
			we := AsWBAError(err)
			if we == nil {
				results <- result{label, ""}
				return
			}
			results <- result{label, we.ResponseHeader().Get("Accept-Signature")}
		}()
	}
	require.Eventually(t, func() bool { return rig.dirs.count(rig.origin) == 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let every waiter join the shared fetch
	close(release)
	wg.Wait()
	close(results)
	for res := range results {
		require.True(t, strings.HasPrefix(res.header, res.label+"="), "%s got %q", res.label, res.header)
	}
}

// TestDirectoryResolverExpiryFromVerifiedSignatures: only signatures the
// verifier checked bound the cache. An unread signature with an expires in
// the past does not make the directory expire at once.
func TestDirectoryResolverExpiryFromVerifiedSignatures(t *testing.T) {
	origin := "https://buyer.example.com"
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)
	resp := signDirectoryBody(t, origin, []byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","x":"`+jwk.X+`"}]}`), key)
	resp.Header.Set("Signature-Input", resp.Header.Get("Signature-Input")+`, key9=("@authority";req "content-digest");created=1;expires=2;keyid="unknown";tag="`+DirectoryTag+`"`)
	resp.Header.Set("Signature", resp.Header.Get("Signature")+", key9=:AAAA:")
	var fetches atomic.Int32
	d := NewDirectoryResolver(DirectoryResolverOptions{Clock: wbaClock, Fetch: func(context.Context, string) (*DirectoryResponse, error) {
		fetches.Add(1)
		return resp, nil
	}})
	for range 2 {
		got, err := d.ResolveWBAKey(context.Background(), origin, jwk.Kid)
		require.NoError(t, err)
		require.NotNil(t, got)
	}
	require.Equal(t, int32(1), fetches.Load(), "cached for the cache period, not expired by the unread signature")
}
