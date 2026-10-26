package signing

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const coveredComponentsTestNow int64 = 1_790_000_000

// coveredComponentsFixture is a matched signer key, resolver, and request
// template for exercising the covered-component list directly. The
// production Signer only emits the profile's fixed list, so these tests
// build the signature base themselves to model a peer signer that covers
// additional HTTP fields.
type coveredComponentsFixture struct {
	priv     ed25519.PrivateKey
	kid      string
	resolver *StaticJWKSResolver
	profile  Profile
}

func newCoveredComponentsFixture(t *testing.T, profile Profile) *coveredComponentsFixture {
	t.Helper()
	kid := "covered-components-test"
	res, err := GenerateKeyForProfile(AlgEd25519, kid, profile)
	require.NoError(t, err)
	priv, _, err := LoadPrivateKey(res.PrivateKeyPEM)
	require.NoError(t, err)
	resolver := NewStaticJWKSResolver()
	jwk := res.PublicJWK
	resolver.Put(kid, &jwk, "https://buyer.example.com")
	return &coveredComponentsFixture{priv: priv.(ed25519.PrivateKey), kid: kid, resolver: resolver, profile: profile}
}

func (f *coveredComponentsFixture) request(t *testing.T) *http.Request {
	t.Helper()
	body := `{"plan_id":"p1"}`
	r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Digest", computeSHA256DigestHeader([]byte(body)))
	r.Header.Set("Signature-Agent", `sig1="https://buyer.example.com"`)
	r.Header.Set("X-Adcp-Trace", "trace-1")
	return r
}

// sign attaches Signature-Input and Signature covering exactly covered. Field
// values come from the request as RFC 9421 §2.1 defines them (lines trimmed
// of SP/HTAB and joined with ", "), independent of the verifier's helpers.
func (f *coveredComponentsFixture) sign(t *testing.T, r *http.Request, covered []string) {
	t.Helper()
	canonicalURI, err := canonicalTargetURI(reconstructRequestURL(r, "https"))
	require.NoError(t, err)
	values := map[string]string{
		componentMethod:    strings.ToUpper(r.Method),
		componentTargetURI: canonicalURI,
		componentAuthority: canonicalAuthority(authorityOf(r), "https"),
	}
	for _, name := range covered {
		if strings.HasPrefix(name, "@") {
			if _, ok := values[name]; !ok {
				values[name] = "/placeholder" // a derived component the profile does not define
			}
			continue
		}
		var lines []string
		for _, v := range r.Header.Values(name) {
			lines = append(lines, strings.Trim(v, " \t"))
		}
		values[name] = strings.Join(lines, ", ")
	}
	params := formatSigParams(covered, coveredComponentsTestNow, coveredComponentsTestNow+300,
		"KXYnfEfJ0PBRZXQyVXfVQA", f.kid, AlgEd25519, f.profile.Tag)
	base, err := buildSignatureBase(covered, values, params)
	require.NoError(t, err)
	sig := ed25519.Sign(f.priv, []byte(base))
	r.Header.Set(signatureInputHeader, "sig1="+params)
	r.Header.Set(signatureHeader, "sig1=:"+b64UrlEncodeRaw(sig)+":")
}

func (f *coveredComponentsFixture) verify(r *http.Request) (*VerifiedSigner, error) {
	return VerifyRequestSignature(r, VerifyOptions{
		Profile:             f.profile,
		ContentDigestPolicy: DigestRequired,
		Resolver:            f.resolver,
		Replay:              NewMemoryReplayStore(0),
		Clock:               func() time.Time { return time.Unix(coveredComponentsTestNow, 0) },
	})
}

var profileCovered = []string{"@method", "@target-uri", "@authority", "content-type", "content-digest"}

func withCovered(extra ...string) []string {
	return append(append([]string{}, profileCovered...), extra...)
}

func profileName(p Profile) string {
	return strings.ReplaceAll(p.Tag, "/", "_")
}

func requireCode(t *testing.T, err error, want ErrorCode, detail string) {
	t.Helper()
	require.Error(t, err)
	e := AsError(err)
	require.NotNil(t, e, "untyped error %v", err)
	require.Equal(t, want, e.Code, e.Detail)
	if detail != "" {
		require.Equal(t, detail, e.Detail)
	}
}

// TestVerifyToleratesAdditionalCoveredComponents grades the RFC 9421 profile
// rule that the required components are a minimum, not an exact list: a
// signer may cover more HTTP fields and the verifier must still accept the
// signature.
func TestVerifyToleratesAdditionalCoveredComponents(t *testing.T) {
	cases := []struct {
		name    string
		covered []string
		setup   func(r *http.Request)
	}{
		{name: "signature-agent field", covered: withCovered("signature-agent")},
		{name: "two additional fields", covered: withCovered("signature-agent", "x-adcp-trace")},
		{name: "additional field first", covered: append([]string{"signature-agent"}, profileCovered...)},
		{name: "additional field interleaved", covered: []string{"@method", "x-adcp-trace", "@target-uri", "@authority", "content-type", "content-digest"}},
		{
			name:    "multi-line field joined per RFC 9421",
			covered: withCovered("x-adcp-trace"),
			setup:   func(r *http.Request) { r.Header.Add("X-Adcp-Trace", "  trace-2 ") },
		},
		{
			name:    "empty field value",
			covered: withCovered("x-adcp-trace"),
			setup:   func(r *http.Request) { r.Header.Set("X-Adcp-Trace", "") },
		},
	}
	for _, profile := range []Profile{ProfileRequestSigning, ProfileWebhookSigning} {
		for _, tc := range cases {
			t.Run(profileName(profile)+"/"+tc.name, func(t *testing.T) {
				f := newCoveredComponentsFixture(t, profile)
				r := f.request(t)
				if tc.setup != nil {
					tc.setup(r)
				}
				f.sign(t, r, tc.covered)
				signer, err := f.verify(r)
				require.NoError(t, err)
				require.Equal(t, "https://buyer.example.com", signer.AgentURL)
				require.Equal(t, tc.covered, signer.CoveredComponents)
			})
		}
	}
}

// TestVerifyAdditionalCoveredComponentsStillBound proves an additional
// component is part of the signature: changing it after signing breaks
// verification, as for any other covered component.
func TestVerifyAdditionalCoveredComponentsStillBound(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(r *http.Request)
		mutate func(r *http.Request)
	}{
		{name: "value changed", mutate: func(r *http.Request) {
			r.Header.Set("Signature-Agent", `sig1="https://attacker.example"`)
		}},
		{name: "field line added", mutate: func(r *http.Request) {
			r.Header.Add("Signature-Agent", `sig2="https://attacker.example"`)
		}},
		{
			name: "field lines reordered",
			setup: func(r *http.Request) {
				r.Header["Signature-Agent"] = []string{`sig1="https://buyer.example.com"`, `sig2="https://b.example"`}
			},
			mutate: func(r *http.Request) {
				r.Header["Signature-Agent"] = []string{`sig2="https://b.example"`, `sig1="https://buyer.example.com"`}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCoveredComponentsFixture(t, ProfileRequestSigning)
			r := f.request(t)
			if tc.setup != nil {
				tc.setup(r)
			}
			f.sign(t, r, withCovered("signature-agent"))
			tc.mutate(r)
			_, err := f.verify(r)
			requireCode(t, err, CodeInvalid, "")
		})
	}
}

// TestVerifyCoveredFieldLineSplitting pins RFC 9421 §2.1: a field's value is
// its lines joined with ", ", so re-splitting or merging lines without
// changing the joined value still verifies. A consumer of a covered field
// must therefore read every line, never only the first.
func TestVerifyCoveredFieldLineSplitting(t *testing.T) {
	f := newCoveredComponentsFixture(t, ProfileRequestSigning)
	r := f.request(t)
	r.Header["X-Adcp-Trace"] = []string{"a, b"}
	f.sign(t, r, withCovered("x-adcp-trace"))
	r.Header["X-Adcp-Trace"] = []string{"a", "b"}
	_, err := f.verify(r)
	require.NoError(t, err)
}

// TestVerifyCoveredComponentRejections pins what extra-component tolerance
// does not relax: required components, duplicates, unsupported derived
// components, malformed names, and fields absent from the request. Each case
// asserts the detail as well as the code, so it proves which check fired.
func TestVerifyCoveredComponentRejections(t *testing.T) {
	cases := []struct {
		name    string
		covered []string
		mutate  func(r *http.Request) // after signing
		want    ErrorCode
		// wantWebhook is the code under the webhook profile, when it differs.
		wantWebhook ErrorCode
		detail      string
	}{
		{
			name:    "required @authority missing despite extras",
			covered: []string{"@method", "@target-uri", "content-type", "content-digest", "signature-agent"},
			want:    CodeComponentsIncomplete,
			detail:  "missing required covered component",
		},
		{
			name:    "required component checked before an absent extra field",
			covered: []string{"@method", "@target-uri", "content-type", "content-digest", "x-adcp-trace"},
			mutate:  func(r *http.Request) { r.Header.Del("X-Adcp-Trace") },
			want:    CodeComponentsIncomplete,
			detail:  "missing required covered component",
		},
		{
			name:    "content-type missing on a body despite extras",
			covered: []string{"@method", "@target-uri", "@authority", "content-digest", "signature-agent"},
			want:    CodeComponentsIncomplete,
			detail:  "content-type not covered",
		},
		{
			name:        "duplicate additional component",
			covered:     withCovered("signature-agent", "signature-agent"),
			want:        CodeComponentsUnexpected,
			wantWebhook: CodeHeaderMalformed,
			detail:      "covered component listed more than once",
		},
		{
			name:        "duplicate profile component",
			covered:     withCovered("@method"),
			want:        CodeComponentsUnexpected,
			wantWebhook: CodeHeaderMalformed,
			detail:      "covered component listed more than once",
		},
		{
			name:    "covered field absent from request",
			covered: withCovered("x-adcp-trace"),
			mutate:  func(r *http.Request) { r.Header.Del("X-Adcp-Trace") },
			want:    CodeHeaderMalformed,
			detail:  "covered header field absent from request",
		},
		{
			name:    "covered field value with a line break",
			covered: withCovered("x-adcp-trace"),
			mutate: func(r *http.Request) {
				r.Header["X-Adcp-Trace"] = []string{"trace-1\n\"@method\": GET"}
			},
			want:   CodeHeaderMalformed,
			detail: "covered header field value contains a control character",
		},
		{
			name:    "unsupported derived component",
			covered: withCovered("@path"),
			want:    CodeHeaderMalformed,
			detail:  "unsupported derived component",
		},
		{
			name:    "@signature-params is not a coverable component",
			covered: withCovered("@signature-params"),
			want:    CodeHeaderMalformed,
			detail:  "unsupported derived component",
		},
		{
			name:    "uppercase field name",
			covered: withCovered("Signature-Agent"),
			want:    CodeHeaderMalformed,
			detail:  "covered component is not a lowercase HTTP field name",
		},
		{
			name:    "field name with a space",
			covered: withCovered("x adcp"),
			want:    CodeHeaderMalformed,
			detail:  "covered component is not a lowercase HTTP field name",
		},
		{
			name:    "empty component name",
			covered: withCovered(""),
			want:    CodeHeaderMalformed,
			detail:  "covered component is not a lowercase HTTP field name",
		},
	}
	for _, profile := range []Profile{ProfileRequestSigning, ProfileWebhookSigning} {
		for _, tc := range cases {
			t.Run(profileName(profile)+"/"+tc.name, func(t *testing.T) {
				f := newCoveredComponentsFixture(t, profile)
				r := f.request(t)
				f.sign(t, r, tc.covered)
				if tc.mutate != nil {
					tc.mutate(r)
				}
				_, err := f.verify(r)
				want := tc.want
				if profile.Tag == ProfileWebhookSigning.Tag && tc.wantWebhook != "" {
					want = tc.wantWebhook
				}
				requireCode(t, err, want, tc.detail)
				if profile.Tag == ProfileWebhookSigning.Tag {
					require.Equal(t, "webhook_signature_"+strings.TrimPrefix(string(want), "request_signature_"),
						AsError(err).WireCode(profile))
				}
			})
		}
	}
}

// TestVerifyRejectsParameterizedComponents pins that component identifiers
// with parameters, including the Web Bot Auth member form
// "signature-agent";key="sig1", stay outside this profile.
func TestVerifyRejectsParameterizedComponents(t *testing.T) {
	for _, component := range []string{`"signature-agent";key="sig1"`, `"x-adcp-trace";sf`, `"@authority";req`} {
		t.Run(component, func(t *testing.T) {
			f := newCoveredComponentsFixture(t, ProfileRequestSigning)
			r := f.request(t)
			f.sign(t, r, profileCovered)
			input := r.Header.Get(signatureInputHeader)
			r.Header.Set(signatureInputHeader, strings.Replace(input, `"content-digest")`, `"content-digest" `+component+`)`, 1))
			_, err := f.verify(r)
			requireCode(t, err, CodeHeaderMalformed, "component not quoted")
		})
	}
}

// TestCoveredComponentCap pins the per-signature component cap at exactly
// maxCoveredComponents.
func TestCoveredComponentCap(t *testing.T) {
	list := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `"x-c` + strconv.Itoa(i) + `"`
		}
		return "sig1=(" + strings.Join(parts, " ") + `);created=1;expires=2;nonce="n";keyid="k";alg="ed25519";tag="t"`
	}
	_, err := parseSignatureInput(list(maxCoveredComponents))
	require.NoError(t, err)
	_, err = parseSignatureInput(list(maxCoveredComponents + 1))
	requireCode(t, err, CodeHeaderMalformed, "too many covered components")
}

// TestCoveredFieldValue covers the RFC 9421 §2.1 value rules directly.
func TestCoveredFieldValue(t *testing.T) {
	for _, v := range []string{"a\nb", "a\r\nb", "a\x00b", "a\r"} {
		h := http.Header{}
		h["X-Adcp-Trace"] = []string{v}
		_, err := coveredFieldValue(h, "x-adcp-trace")
		requireCode(t, err, CodeHeaderMalformed, "covered header field value contains a control character")
	}
	h := http.Header{}
	h["X-Adcp-Trace"] = []string{" a ", "\tb", ""}
	got, err := coveredFieldValue(h, "x-adcp-trace")
	require.NoError(t, err)
	require.Equal(t, "a, b, ", got)
	_, err = coveredFieldValue(http.Header{}, "x-adcp-trace")
	requireCode(t, err, CodeHeaderMalformed, "covered header field absent from request")
}

// TestSignerStillEmitsProfileComponentsOnly pins that tolerance is
// verifier-only: the signer's covered list is unchanged.
func TestSignerStillEmitsProfileComponentsOnly(t *testing.T) {
	f := newCoveredComponentsFixture(t, ProfileRequestSigning)
	signer, err := NewSigner(SignerOptions{KeyID: f.kid, PrivateKey: f.priv})
	require.NoError(t, err)
	r := f.request(t)
	require.NoError(t, signer.SignRequest(r, SignOptions{CoverContentDigest: true}))
	require.True(t, strings.HasPrefix(r.Header.Get(signatureInputHeader),
		`sig1=("@method" "@target-uri" "@authority" "content-type" "content-digest");`),
		r.Header.Get(signatureInputHeader))
}
