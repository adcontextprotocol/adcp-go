package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// The grant fixtures model DR-0025's parties:
//   - brand-7k3q.com, the brand, which publishes grants;
//   - agency-7k3q.com, an agency named as account.operator;
//   - platform-7k3q.com, a platform hosting the agency's buying agent;
//   - buyer agents at https://buy.agency-7k3q.com and https://agent.platform-7k3q.com.
const (
	agencyAgent   = "https://buy.agency-7k3q.com"
	platformAgent = "https://agent.platform-7k3q.com"
)

func grantRig(t *testing.T, records map[string]string) *evalRig {
	t.Helper()
	r := newEvalRig(t)
	for domain, doc := range records {
		r.server.set(trustURL(domain), 200, doc)
	}
	return r
}

var baseRecords = map[string]string{
	"agency-7k3q.com":   `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}]}`,
	"platform-7k3q.com": `{"agents":[{"url":"https://agent.platform-7k3q.com/mcp","roles":["adcp:buying"]}]}`,
}

func with(records map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range records {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestGrantEvaluation(t *testing.T) {
	brandGrantsAgency := `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"countries":["US","CA"],"profiles":{"adcp":{"brands":["nova"]}},"valid_until":"2027-06-30T00:00:00Z"}]}`
	cases := []struct {
		name    string
		records map[string]string
		req     GrantRequest
		want    bool
		reason  string
	}{
		{
			name:    "implicit when operator, brand, and signer share a registrable domain",
			records: map[string]string{"agency-7k3q.com": baseRecords["agency-7k3q.com"]},
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "house.agency-7k3q.com", AccountOperator: "agency-7k3q.com"},
			want:    true,
		},
		{
			name:    "direct grant to the signer's operator, which is account.operator",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": brandGrantsAgency}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova", Countries: []string{"US"}},
			want:    true,
		},
		{
			name:    "a grant does not cover a brand it does not list",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": brandGrantsAgency}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "other", Countries: []string{"US"}},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "a country-limited grant covers no claim outside it",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": brandGrantsAgency}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova", Countries: []string{"US", "MX"}},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "a country-limited grant covers no claim without countries",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": brandGrantsAgency}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova"},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "the scope must be covered",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": brandGrantsAgency}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova", Countries: []string{"US"}, Scope: ScopeCreativeGeneration},
			reason:  GrantReasonAbsent,
		},
		{
			name: "adcp:all covers adcp scopes and * covers everything",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:all"]}]}`,
				"other-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["*"]}]}`}),
			req:  GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", Scope: ScopeCreativeGeneration},
			want: true,
		},
		{
			// L2 Grants in trust.json: a grantee MUST be a registrable
			// domain, and a grant whose grantee is a host covers no claim.
			// It is never widened to the host's registrable domain.
			name:    "a grantee host under the signer's operator domain covers no claim",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"buy.agency-7k3q.com","scopes":["adcp:media_buying"]}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "a host grantee narrowed with agents[] still covers no claim",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"buy.agency-7k3q.com","scopes":["adcp:media_buying"],"agents":[{"url":"https://buy.agency-7k3q.com/mcp"}]}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
		{
			name: "a host grantee covers no claim on the second link either",
			records: with(baseRecords, map[string]string{
				"brand-7k3q.com":  `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`,
				"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"agent.platform-7k3q.com","scopes":["adcp:media_buying"]}]}`,
			}),
			req:    GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason: GrantReasonAbsent,
		},
		{
			name:    "a grantee host under another domain is not the operator",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com.evil-7k3q.com","scopes":["adcp:media_buying"]}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
		{
			// A public suffix can never be a signer's operator, so this pins
			// only that the record carrying such a grant stays readable.
			name:    "a record with a public-suffix grantee still reads",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"github.io","scopes":["adcp:media_buying"]},{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			want:    true,
		},
		{
			// alice.github.io is registrable (github.io is a PRIVATE-section
			// suffix), so a grant to it reaches Alice's agents.
			name: "a grantee under a private-section suffix is a registrable domain",
			records: map[string]string{
				"alice.github.io": `{"agents":[{"url":"https://buy.alice.github.io/mcp","roles":["adcp:buying"]}]}`,
				"brand-7k3q.com":  `{"grants":[{"grantee":"alice.github.io","scopes":["adcp:media_buying"]}]}`,
			},
			req:  GrantRequest{AgentOrigin: "https://buy.alice.github.io", BrandDomain: "brand-7k3q.com", AccountOperator: "alice.github.io"},
			want: true,
		},
		{
			name: "a grant to one private-section domain does not reach its sibling",
			records: map[string]string{
				"bob.github.io":  `{"agents":[{"url":"https://buy.bob.github.io/mcp","roles":["adcp:buying"]}]}`,
				"brand-7k3q.com": `{"grants":[{"grantee":"alice.github.io","scopes":["adcp:media_buying"]}]}`,
			},
			req:    GrantRequest{AgentOrigin: "https://buy.bob.github.io", BrandDomain: "brand-7k3q.com", AccountOperator: "bob.github.io"},
			reason: GrantReasonAbsent,
		},
		{
			// account.operator and the brand's domain compare as registrable
			// domains, so hosts under them name their operators.
			name:    "account.operator and the brand's domain compare as registrable domains",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "shop.brand-7k3q.com", AccountOperator: "ops.agency-7k3q.com"},
			want:    true,
		},
		{
			name:    "account.operator must be on the chain",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": brandGrantsAgency}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "someone-else-7k3q.com", BrandID: "nova", Countries: []string{"US"}},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "a self-operated brand (brand is account.operator) grants the hosting platform directly",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"agents":[{"url":"https://agent.platform-7k3q.com/mcp"}]}]}`}),
			req:     GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "brand-7k3q.com"},
			want:    true,
		},
		{
			name:    "a self-operated brand's grant to someone else does not cover the platform",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`}),
			req:     GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "brand-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
		{
			name: "chain through account.operator to the hosting platform",
			records: with(baseRecords, map[string]string{
				"brand-7k3q.com":  brandGrantsAgency,
				"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"agents":[{"url":"https://agent.platform-7k3q.com/mcp"}]}]}`,
			}),
			req:  GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova", Countries: []string{"CA"}},
			want: true,
		},
		{
			name: "intersection: the second link must cover the claim's countries too",
			records: with(baseRecords, map[string]string{
				"brand-7k3q.com":  brandGrantsAgency,
				"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"countries":["US"]}]}`,
			}),
			req:    GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova", Countries: []string{"CA"}},
			reason: GrantReasonAbsent,
		},
		{
			name: "agents[] narrowing is matched by canonical origin",
			records: with(baseRecords, map[string]string{
				"brand-7k3q.com":  brandGrantsAgency,
				"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"agents":[{"url":"https://other.platform-7k3q.com/mcp"}]}]}`,
			}),
			req:    GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "nova", Countries: []string{"CA"}},
			reason: GrantReasonAbsent,
		},
		{
			name: "no third link: a grant to account.operator's grantee's grantee is not followed",
			records: with(baseRecords, map[string]string{
				"brand-7k3q.com":    `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`,
				"agency-7k3q.com":   `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"middle-7k3q.com","scopes":["adcp:media_buying"]}]}`,
				"middle-7k3q.com":   `{"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"]}]}`,
				"platform-7k3q.com": baseRecords["platform-7k3q.com"],
			}),
			req:    GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason: GrantReasonAbsent,
		},
		{
			name: "the buyer operator's record never authorizes a brand outside its domain",
			records: with(baseRecords, map[string]string{
				"brand-7k3q.com":  `{"grants":[]}`,
				"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"agency-7k3q.com","scopes":["*"]}]}`,
			}),
			req:    GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason: GrantReasonAbsent,
		},
		{
			name:    "an expired grant",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"valid_until":"2026-10-13T11:00:00Z"}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "a grant not yet valid",
			records: with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"valid_from":"2026-10-14T00:00:00Z"}]}`}),
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
		{
			name:    "Web Bot Auth: the signer's operator must list the origin with adcp:buying",
			records: map[string]string{"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:sales"]}]}`},
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "agency-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonSignerUnlisted,
		},
		{
			name:    "a brand trust record that cannot be fetched is not absence",
			records: baseRecords,
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "down-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonTrustRecordUnavailable,
		},
		{
			name:    "a brand that publishes no trust record and no legacy hook",
			records: baseRecords,
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonTrustRecordAbsent,
		},
		{
			name:    "no registrable domain",
			records: baseRecords,
			req:     GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand.example", AccountOperator: "agency-7k3q.com"},
			reason:  GrantReasonAbsent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := grantRig(t, tc.records)
			r.server.fail(trustURL("down-7k3q.com"), errors.New("timeout"))
			g := &GrantEvaluator{Records: r.eval}
			res, err := g.Check(context.Background(), tc.req)
			require.NoError(t, err)
			require.Equal(t, tc.want, res.Authorized, res.Reason)
			if !tc.want {
				require.Equal(t, tc.reason, res.Reason)
				require.Error(t, res.Err())
			} else {
				require.NoError(t, res.Err())
				require.False(t, res.ExpiresAt.IsZero())
			}
		})
	}
}

func TestGrantExpiryIsTheEarliestOnTheChain(t *testing.T) {
	r := grantRig(t, with(baseRecords, map[string]string{
		"brand-7k3q.com":  `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"valid_until":"2026-10-13T12:00:50Z"}]}`,
		"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"valid_until":"2026-10-13T12:00:30Z"},{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"valid_until":"2026-10-13T12:00:40Z"}]}`,
	}))
	g := &GrantEvaluator{Records: r.eval}
	res, err := g.Check(context.Background(), GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"})
	require.NoError(t, err)
	require.True(t, res.Authorized)
	require.Equal(t, time.Date(2026, 10, 13, 12, 0, 40, 0, time.UTC), res.ExpiresAt, "min over links of each link's latest valid_until")
}

func TestGrantExpiringDuringIntermediaryFetchIsNotAuthorized(t *testing.T) {
	r := grantRig(t, with(baseRecords, map[string]string{
		"brand-7k3q.com":  `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"valid_until":"2026-10-13T12:00:05Z"}]}`,
		"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"]}]}`,
	}))
	// Evaluate brand and signer first so only the intermediary fetch runs
	// during Check, and advance the clock as it happens.
	_, _ = r.eval.Evaluate(context.Background(), "brand-7k3q.com", "")
	_, _ = r.eval.Evaluate(context.Background(), platformAgent, RoleBuying)
	fetch := r.server.fetch
	eval, _ := NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{
		Clock: func() time.Time { return r.now }, PollingInterval: time.Minute,
		Fetch: func(ctx context.Context, url string) (*TrustResponse, error) {
			if url == trustURL("agency-7k3q.com") {
				r.now = r.now.Add(10 * time.Second)
			}
			return fetch(ctx, url)
		},
	})
	_, _ = eval.Evaluate(context.Background(), "brand-7k3q.com", "")
	_, _ = eval.Evaluate(context.Background(), platformAgent, RoleBuying)
	g := &GrantEvaluator{Records: eval}
	res, err := g.Check(context.Background(), GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"})
	require.NoError(t, err)
	require.False(t, res.Authorized)
}

func TestGrantExclusiveAndHooks(t *testing.T) {
	yes := func(context.Context, GrantRequest) (bool, error) { return true, nil }
	fail := func(context.Context, GrantRequest) (bool, error) { return false, errors.New("db down") }
	req := GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"}
	ctx := context.Background()

	// Without exclusive_grants, an uncovered claim is at the seller's
	// discretion.
	r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"x-7k3q.com","scopes":["adcp:media_buying"]}]}`}))
	res, _ := (&GrantEvaluator{Records: r.eval, SellerAuthorization: yes}).Check(ctx, req)
	require.True(t, res.Authorized)
	res, _ = (&GrantEvaluator{Records: r.eval, SellerAuthorization: fail}).Check(ctx, req)
	require.False(t, res.Authorized, "a hook error fails closed")

	// exclusive_grants makes grants[] exhaustive for the scopes it names...
	r = grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"x-7k3q.com","scopes":["adcp:media_buying"]}],"exclusive_grants":true}`}))
	res, _ = (&GrantEvaluator{Records: r.eval, SellerAuthorization: yes}).Check(ctx, req)
	require.False(t, res.Authorized)
	require.Equal(t, GrantReasonAbsent, res.Reason)
	// ...and only those.
	creative := req
	creative.Scope = ScopeCreativeGeneration
	res, _ = (&GrantEvaluator{Records: r.eval, SellerAuthorization: yes}).Check(ctx, creative)
	require.True(t, res.Authorized)

	// A 200 from the brand's trust.json disables the legacy brand.json hook.
	legacyCalled := false
	legacy := func(context.Context, GrantRequest) (bool, error) { legacyCalled = true; return true, nil }
	r = grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[]}`}))
	res, _ = (&GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy}).Check(ctx, req)
	require.False(t, res.Authorized)
	require.False(t, legacyCalled)

	// Absent (404 before any 200): the legacy hook decides.
	r = grantRig(t, baseRecords)
	res, _ = (&GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy}).Check(ctx, req)
	require.True(t, res.Authorized)
	require.True(t, res.LegacyFallbackAllowed)
	require.True(t, legacyCalled)

	// A fetch failure invokes neither hook.
	legacyCalled = false
	sellerCalled := false
	r = grantRig(t, baseRecords)
	r.server.fail(trustURL("brand-7k3q.com"), errors.New("timeout"))
	res, _ = (&GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy, SellerAuthorization: func(context.Context, GrantRequest) (bool, error) {
		sellerCalled = true
		return true, nil
	}}).Check(ctx, req)
	require.False(t, res.Authorized)
	require.False(t, legacyCalled)
	require.False(t, sellerCalled)
}

// TestGrantRFC9421Signer: an RFC 9421 signer keeps its established path
// while the brand has never published a trust record; once the brand
// serves 200, the signer's operator must list it too.
func TestGrantRFC9421Signer(t *testing.T) {
	ctx := context.Background()
	legacy := func(context.Context, GrantRequest) (bool, error) { return true, nil }
	req := GrantRequest{AgentOrigin: "https://legacy.agency-7k3q.com/mcp", BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", SigningProfile: signing.ProfileTagRequestSigning}

	r := grantRig(t, nil) // the agency publishes no trust record either
	res, _ := (&GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy}).Check(ctx, req)
	require.True(t, res.Authorized, "no new operator record is needed before the brand publishes")

	r = grantRig(t, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`})
	res, _ = (&GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy}).Check(ctx, req)
	require.False(t, res.Authorized)
	require.Equal(t, GrantReasonSignerUnlisted, res.Reason, "the agency publishes no record, so it lists no agent")

	// A Web Bot Auth signer never reaches the legacy path without its own
	// operator record.
	wba := req
	wba.SigningProfile = signing.ProfileTagWebBotAuth
	r = grantRig(t, nil)
	res, _ = (&GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy}).Check(ctx, wba)
	require.False(t, res.Authorized)

	_, err := (&GrantEvaluator{Records: r.eval}).Check(ctx, GrantRequest{AgentOrigin: agencyAgent, SigningProfile: "bearer"})
	require.Error(t, err)
}

// permissionDetails renders err the way a seller does, by returning it
// from SyncAccountsAuthorizer through a registered MCP server, and returns
// the wire code and details.
func permissionDetails(t *testing.T, err error) (string, map[string]any) {
	t.Helper()
	require.Error(t, err)
	handlerCalled := false
	result := callSyncAccounts(t, adcp.Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		SyncAccounts: func(context.Context, *adcp.SyncAccountsRequest) ([]adcp.AccountResult, error) {
			handlerCalled = true
			return nil, nil
		},
		SyncAccountsAuthorizer: func(context.Context, *mcp.CallToolRequest, []adcp.AccountInput) error { return err },
	})
	require.True(t, result.IsError)
	require.False(t, handlerCalled, "no account is created when the gate denies")
	b, _ := json.Marshal(result.StructuredContent)
	var p struct {
		ADCPError struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"adcp_error"`
	}
	require.NoError(t, json.Unmarshal(b, &p))
	return p.ADCPError.Code, p.ADCPError.Details
}

func callSyncAccounts(t *testing.T, cfg adcp.Config) *mcp.CallToolResult {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "identity-test", Version: "v0"}, nil)
	adcp.Register(server, cfg)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "sync_accounts", Arguments: map[string]any{
		"idempotency_key": "4f6c1a0e-8d2b-4b7e-9c3a-2e5d7f1b0a96",
		"accounts":        []map[string]any{{"brand": map[string]any{"domain": "brand-7k3q.com"}, "operator": "agency-7k3q.com", "billing": "operator"}},
	}})
	require.NoError(t, err)
	return res
}

type stubKeys map[string]signing.JWK

func (s stubKeys) ResolveWBAKey(_ context.Context, origin, kid string) (*signing.JWK, error) {
	if k, ok := s[origin]; ok && k.Kid == kid {
		return &k, nil
	}
	return nil, nil
}

type noRevocations struct{}

func (noRevocations) CheckWBAKey(context.Context, string, string) (signing.RevocationState, error) {
	return signing.RevocationCurrent, nil
}

type stubAuthorizer map[string]string

func (s stubAuthorizer) AuthorizeWBA(_ context.Context, signer *signing.VerifiedSigner, _ string) (string, error) {
	return s[signer.AgentURL], nil
}

// wbaAgent is a Web Bot Auth signer for tests: an origin and its key.
type wbaAgent struct {
	origin string
	key    ed25519.PrivateKey
	jwk    signing.JWK
}

func newWBAAgent(t *testing.T, origin string) wbaAgent {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	jwk, err := signing.PublicDirectoryKey(key)
	require.NoError(t, err)
	return wbaAgent{origin: origin, key: key, jwk: jwk}
}

// signingTransport WBA-signs every outbound request as the current agent.
type signingTransport struct {
	mu      sync.Mutex
	current wbaAgent
}

func (s *signingTransport) use(a wbaAgent) { s.mu.Lock(); s.current = a; s.mu.Unlock() }

func (s *signingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	a := s.current
	s.mu.Unlock()
	clone := r.Clone(r.Context())
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		clone.Body = io.NopCloser(bytes.NewReader(b))
	}
	if err := signing.SignWebBotAuthRequest(clone, a.key, signing.WBASignOptions{AgentOrigin: a.origin}); err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(clone)
}

// TestSyncAccountsGateUsesThePerRequestSigner drives a stateful MCP
// session over HTTP: initialize is signed by one agent and tools/call by
// another. The gate must evaluate the agent that signed the call, never the
// session's initialize request.
func TestSyncAccountsGateUsesThePerRequestSigner(t *testing.T) {
	agentA := newWBAAgent(t, "https://buy.agency-7k3q.com")
	agentB := newWBAAgent(t, "https://agent.platform-7k3q.com")
	var seen []*signing.VerifiedSigner
	var ctxSigners []*signing.VerifiedSigner
	server := mcp.NewServer(&mcp.Implementation{Name: "seller", Version: "v0"}, nil)
	adcp.Register(server, adcp.Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		SyncAccounts: func(context.Context, *adcp.SyncAccountsRequest) ([]adcp.AccountResult, error) {
			return []adcp.AccountResult{{AccountID: "acc_1", Status: "active"}}, nil
		},
		SyncAccountsAuthorizer: func(ctx context.Context, req *mcp.CallToolRequest, _ []adcp.AccountInput) error {
			seen = append(seen, signing.VerifiedSignerFromToolRequest(req))
			ctxSigners = append(ctxSigners, signing.VerifiedSignerFromContext(ctx))
			return nil
		},
	})
	handler := signing.Middleware(signing.MiddlewareOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		WebBotAuth: &signing.WebBotAuthOptions{
			Keys:       stubKeys{agentA.origin: agentA.jwk, agentB.origin: agentB.jwk},
			Revocation: noRevocations{},
			Authorizer: stubAuthorizer{agentA.origin: "principal_a", agentB.origin: "principal_b"},
		},
	})(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	rt := &signingTransport{current: agentA}
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "buyer", Version: "v0"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: rt}}, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	call := func() {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "sync_accounts", Arguments: map[string]any{
			"idempotency_key": "4f6c1a0e-8d2b-4b7e-9c3a-2e5d7f1b0a9" + strconv.Itoa(len(seen)),
			"accounts":        []map[string]any{{"brand": map[string]any{"domain": "brand-7k3q.com"}, "operator": "agency-7k3q.com", "billing": "operator"}},
		}})
		require.NoError(t, err)
		require.False(t, res.IsError)
	}
	rt.use(agentB)
	call()
	rt.use(agentA)
	call()
	require.Len(t, seen, 2)
	require.Equal(t, agentB.origin, seen[0].AgentURL)
	require.Equal(t, "principal_b", seen[0].PrincipalID)
	require.Equal(t, agentA.origin, seen[1].AgentURL)
	require.Equal(t, "principal_a", seen[1].PrincipalID)
	// The handler's ctx is the session's initialize request's (signed by A),
	// so a ctx-based lookup would have evaluated the wrong agent for B.
	require.NotNil(t, ctxSigners[0])
	require.Equal(t, agentA.origin, ctxSigners[0].AgentURL, "the trap this API avoids")
}

// toolRequestWithSigner builds the CallToolRequest a tool handler receives
// after Middleware verified signer.
func toolRequestWithSigner(signer *signing.VerifiedSigner) *mcp.CallToolRequest {
	if signer == nil {
		return &mcp.CallToolRequest{}
	}
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: map[string]any{signing.TokenInfoSignerKey: signer}}}}
}

func TestGrantGate(t *testing.T) {
	ctx := context.Background()
	r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`}))
	gate := &GrantGate{
		Evaluator: &GrantEvaluator{Records: r.eval},
		ExistingAccountClaim: func(_ context.Context, id string) (GrantClaim, error) {
			if id == "acc_brand" {
				return GrantClaim{BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"}, nil
			}
			return GrantClaim{}, errors.New("unknown account")
		},
	}
	signer := &signing.VerifiedSigner{AgentURL: agencyAgent, Profile: signing.ProfileTagWebBotAuth, PrincipalID: "p_agency", Revocation: signing.RevocationCurrent}
	stranger := &signing.VerifiedSigner{AgentURL: platformAgent, Profile: signing.ProfileTagWebBotAuth, PrincipalID: "p_platform", Revocation: signing.RevocationCurrent}
	unbound := &signing.VerifiedSigner{AgentURL: agencyAgent, Profile: signing.ProfileTagWebBotAuth, Revocation: signing.RevocationCurrent}
	decl := adcp.AccountInput{Brand: &adcp.BrandReference{Domain: "brand-7k3q.com"}, Operator: "agency-7k3q.com"}

	require.NoError(t, gate.RequireDeclaration(ctx, decl, signer))
	require.NoError(t, gate.RequireDeclaration(ctx, adcp.AccountInput{AccountID: "acc_brand"}, signer))

	code, details := permissionDetails(t, gate.RequireDeclaration(ctx, decl, stranger))
	require.Equal(t, "PERMISSION_DENIED", code)
	require.Equal(t, map[string]any{"scope": "agent", "reason": "grant_absent"}, details)
	_, details = permissionDetails(t, gate.RequireDeclaration(ctx, adcp.AccountInput{AccountID: "acc_unknown"}, signer))
	require.Equal(t, "grant_absent", details["reason"])
	_, details = permissionDetails(t, gate.RequireDeclaration(ctx, adcp.AccountInput{Operator: "agency-7k3q.com"}, signer))
	require.Equal(t, "grant_absent", details["reason"], "an entry with no brand has no grant authority")

	// An identity-established (unbound) Web Bot Auth signer never
	// provisions accounts, even when a grant would cover it.
	code, details = permissionDetails(t, gate.RequireDeclaration(ctx, decl, unbound))
	require.Equal(t, "PERMISSION_DENIED", code)
	require.Nil(t, details)

	// No established identity: PERMISSION_DENIED without details (the
	// cross-tenant oracle clamp).
	code, details = permissionDetails(t, gate.RequireDeclaration(ctx, decl, nil))
	require.Equal(t, "PERMISSION_DENIED", code)
	require.Nil(t, details)

	// Operation scope: a media-buying grant covers media-buying operations
	// only, unless the seller names the operation's scope.
	require.NoError(t, gate.RequireAccountForOperation(ctx, "acc_brand", "create_media_buy", signer))
	_, details = permissionDetails(t, gate.RequireAccountForOperation(ctx, "acc_brand", "build_creative", signer))
	require.Equal(t, "grant_absent", details["reason"])
	gate.OperationClaim = func(_ context.Context, id, op string) (GrantClaim, error) {
		return GrantClaim{BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", Scope: ScopeCreativeGeneration}, nil
	}
	_, details = permissionDetails(t, gate.RequireAccountForOperation(ctx, "acc_brand", "build_creative", signer))
	require.Equal(t, "grant_absent", details["reason"], "the grant does not cover creative generation")

	gate.OperationClaim = nil
	// A stale or absent revocation list: the identity is not established
	// for spend, whatever the grants say; non-spend operations proceed.
	stale := *signer
	stale.Revocation = signing.RevocationStale
	code, _ = permissionDetails(t, gate.RequireAccountForOperation(ctx, "acc_brand", "create_media_buy", &stale))
	require.Equal(t, "SERVICE_UNAVAILABLE", code)
	code, _ = permissionDetails(t, gate.RequireAccount(ctx, "acc_brand", &stale))
	require.Equal(t, "SERVICE_UNAVAILABLE", code, "no operation counts as spend")
	require.NoError(t, gate.RequireAccountForOperation(ctx, "acc_brand", "get_media_buys", &stale))
	require.NoError(t, gate.RequireDeclaration(ctx, decl, &stale), "sync_accounts commits no spend")
	unavailable := *signer
	unavailable.Revocation = signing.RevocationUnavailable
	code, _ = permissionDetails(t, gate.RequireAccountForOperation(ctx, "acc_brand", "get_media_buys", &unavailable))
	require.Equal(t, "SERVICE_UNAVAILABLE", code, "a revocation-store outage blocks every bound call")
	noList := *signer
	noList.Revocation = signing.RevocationNoList
	code, details = permissionDetails(t, gate.RequireAccountForOperation(ctx, "acc_brand", "create_media_buy", &noList))
	require.Equal(t, "PERMISSION_DENIED", code, "no list is never bound: not retryable")
	require.Nil(t, details)
	callReq := toolRequestWithSigner(signer)
	callReq.Params = &mcp.CallToolParamsRaw{Name: "create_media_buy"}
	require.NoError(t, gate.RequireAccountForToolRequest(ctx, callReq, "acc_brand"))
	callReq.Params.Name = "build_creative"
	require.Error(t, gate.RequireAccountForToolRequest(ctx, callReq, "acc_brand"))
	require.Error(t, gate.RequireAccountForToolRequest(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "create_media_buy"}}, "acc_brand"))

	// The seller hook reads the per-request signer and rejects the whole
	// request before any upsert.
	authorize := gate.SyncAccountsAuthorizer()
	require.NoError(t, authorize(ctx, toolRequestWithSigner(signer), []adcp.AccountInput{decl}))
	require.Error(t, authorize(ctx, toolRequestWithSigner(signer), []adcp.AccountInput{
		decl, {Brand: &adcp.BrandReference{Domain: "elsewhere-7k3q.com"}, Operator: "agency-7k3q.com"},
	}))
	require.Error(t, authorize(ctx, toolRequestWithSigner(nil), []adcp.AccountInput{decl}))
	require.Error(t, authorize(ctx, nil, []adcp.AccountInput{decl}))

	_, err := (&GrantEvaluator{}).Check(ctx, GrantRequest{})
	require.Error(t, err, "a GrantEvaluator without Records")
}

// TestGrantOutageRules: a grantor record that cannot be read, including a
// 404 or 410 after it was read, is a discovery failure, never grant_absent
// and never a reason to fall back to brand.json. The last records read stand
// for non-spend claims within the OutageContinuation bound; a spend claim
// gets SERVICE_UNAVAILABLE.
func TestGrantOutageRules(t *testing.T) {
	ctx := context.Background()
	req := GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"}
	req.NonSpend = true
	spend := req
	spend.NonSpend = false
	brandGrants := `{"grants":[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"]}]}`
	requireUnavailable := func(t *testing.T, res GrantAuthorization) {
		t.Helper()
		require.False(t, res.Authorized)
		require.True(t, res.Unavailable)
		code, _ := permissionDetails(t, res.Err())
		require.Equal(t, "SERVICE_UNAVAILABLE", code)
	}

	for name, breakBrand := range map[string]func(*evalRig){
		"outage": func(r *evalRig) { r.server.fail(trustURL("brand-7k3q.com"), errors.New("connection refused")) },
		"410":    func(r *evalRig) { r.server.set(trustURL("brand-7k3q.com"), http.StatusGone, "") },
		"404":    func(r *evalRig) { r.server.set(trustURL("brand-7k3q.com"), http.StatusNotFound, "") },
	} {
		t.Run("brand record "+name, func(t *testing.T) {
			legacyCalled := false
			legacy := func(context.Context, GrantRequest) (bool, error) { legacyCalled = true; return true, nil }
			r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": brandGrants}))
			g := &GrantEvaluator{Records: r.eval, LegacyAuthorization: legacy}
			res, _ := g.Check(ctx, req)
			require.True(t, res.Authorized)

			breakBrand(r)
			r.now = r.now.Add(2 * time.Minute) // past the 1 min freshness, within 4 x 1 min
			res, _ = g.Check(ctx, req)
			require.True(t, res.Authorized, "the last-read grants stand for a non-spend claim")
			require.True(t, res.ExpiresAt.After(r.now))
			res, _ = g.Check(ctx, spend)
			requireUnavailable(t, res)
			r.now = r.now.Add(4 * time.Minute) // past the bound
			res, _ = g.Check(ctx, req)
			requireUnavailable(t, res)
			require.False(t, legacyCalled, "authorized_operators never comes back once the brand published a record")
		})
	}

	t.Run("intermediary record", func(t *testing.T) {
		r := grantRig(t, with(baseRecords, map[string]string{
			"brand-7k3q.com":  brandGrants,
			"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"]}]}`,
		}))
		g := &GrantEvaluator{Records: r.eval}
		chain := GrantRequest{AgentOrigin: platformAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com", NonSpend: true}
		res, _ := g.Check(ctx, chain)
		require.True(t, res.Authorized)
		r.server.set(trustURL("agency-7k3q.com"), http.StatusNotFound, "")
		r.now = r.now.Add(2 * time.Minute)
		res, _ = g.Check(ctx, chain)
		require.True(t, res.Authorized, "a 404 is not a removal: the second link stands within the bound")
		chain.NonSpend = false
		res, _ = g.Check(ctx, chain)
		requireUnavailable(t, res)
	})

	t.Run("signer's operator record 404 after a read", func(t *testing.T) {
		r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": brandGrants}))
		g := &GrantEvaluator{Records: r.eval}
		res, _ := g.Check(ctx, req)
		require.True(t, res.Authorized)
		r.server.set(trustURL("agency-7k3q.com"), http.StatusNotFound, "")
		r.now = r.now.Add(2 * time.Minute)
		res, _ = g.Check(ctx, req)
		require.True(t, res.Authorized, "the signer's last listing stands for a non-spend claim")
		res, _ = g.Check(ctx, spend)
		requireUnavailable(t, res)
	})

	t.Run("a claim not marked NonSpend fails closed", func(t *testing.T) {
		r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": brandGrants}))
		g := &GrantEvaluator{Records: r.eval}
		unmarked := GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"}
		res, _ := g.Check(ctx, unmarked)
		require.True(t, res.Authorized)
		r.server.fail(trustURL("brand-7k3q.com"), errors.New("connection refused"))
		r.now = r.now.Add(2 * time.Minute)
		res, _ = g.Check(ctx, unmarked)
		requireUnavailable(t, res)
	})

	t.Run("signer's operator unreachable with no history", func(t *testing.T) {
		r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[]}`}))
		r.server.fail(trustURL("agency-7k3q.com"), errors.New("timeout"))
		res, _ := (&GrantEvaluator{Records: r.eval}).Check(ctx, req)
		requireUnavailable(t, res)
	})

	t.Run("signer unlisted: PERMISSION_DENIED without details", func(t *testing.T) {
		r := grantRig(t, map[string]string{"agency-7k3q.com": `{"agents":[]}`, "brand-7k3q.com": brandGrants})
		res, _ := (&GrantEvaluator{Records: r.eval}).Check(ctx, req)
		require.False(t, res.Authorized)
		require.Equal(t, GrantReasonSignerUnlisted, res.Reason)
		code, details := permissionDetails(t, res.Err())
		require.Equal(t, "PERMISSION_DENIED", code)
		require.Empty(t, details)
	})
}

// TestHostGranteeCoversNoClaim pins L2 Grants in trust.json: a grantee MUST
// be a registrable domain, and a grant to a host covers no claim, so a grant
// to one tenant host of a multi-tenant vendor reaches no tenant, the named
// one included. The evaluator reads the record, logs a warning, and
// MarshalTrustRecord refuses to author it.
func TestHostGranteeCoversNoClaim(t *testing.T) {
	server := newTrustServer()
	server.set(trustURL("vendor-7k3q.com"), 200, `{"agents":[{"url":"https://t1.vendor-7k3q.com/mcp","roles":["adcp:buying"]},{"url":"https://t2.vendor-7k3q.com/mcp","roles":["adcp:buying"]}]}`)
	server.set(trustURL("brand-7k3q.com"), 200, `{"grants":[{"grantee":"t1.vendor-7k3q.com","scopes":["adcp:media_buying"]}]}`)
	var logs bytes.Buffer
	eval, err := NewTrustRecordEvaluator(TrustRecordEvaluatorOptions{Fetch: server.fetch, Clock: func() time.Time { return testNow },
		PollingInterval: time.Minute, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	require.NoError(t, err)
	g := &GrantEvaluator{Records: eval}
	for _, tenant := range []string{"https://t1.vendor-7k3q.com", "https://t2.vendor-7k3q.com"} {
		res, err := g.Check(context.Background(), GrantRequest{AgentOrigin: tenant, BrandDomain: "brand-7k3q.com", AccountOperator: "vendor-7k3q.com"})
		require.NoError(t, err)
		require.False(t, res.Authorized, tenant)
		require.Equal(t, GrantReasonAbsent, res.Reason, tenant)
	}
	require.Contains(t, logs.String(), "the grant covers no claim")
	require.Contains(t, logs.String(), "operator=brand-7k3q.com")
	require.Contains(t, logs.String(), "grantee=t1.vendor-7k3q.com")

	// A public-suffix grantee is warned about too.
	logs.Reset()
	server.set(trustURL("brand2-7k3q.com"), 200, `{"grants":[{"grantee":"github.io","scopes":["adcp:media_buying"]}]}`)
	res, err := g.Check(context.Background(), GrantRequest{AgentOrigin: "https://t1.vendor-7k3q.com", BrandDomain: "brand2-7k3q.com", AccountOperator: "vendor-7k3q.com"})
	require.NoError(t, err)
	require.False(t, res.Authorized)
	require.Contains(t, logs.String(), "grantee=github.io")

	_, err = MarshalTrustRecord(&TrustRecord{Grants: []TrustGrant{{Grantee: "t1.vendor-7k3q.com", Scopes: []string{ScopeMediaBuying}}}}, "brand-7k3q.com")
	require.ErrorContains(t, err, "agents[]", "an author is told to narrow with agents[]")
}

// TestNonRegistrableGranteeScopesStayExclusive pins the rule that keeps the
// grantee rule from failing open: a grant that covers no claim still counts
// for exclusive_grants, so the seller's discretion is not consulted for its
// scopes.
func TestNonRegistrableGranteeScopesStayExclusive(t *testing.T) {
	yes := func(context.Context, GrantRequest) (bool, error) { return true, nil }
	req := GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "brand-7k3q.com", AccountOperator: "agency-7k3q.com"}
	for _, grantee := range []string{"buy.agency-7k3q.com", "github.io"} {
		t.Run(grantee, func(t *testing.T) {
			r := grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"` + grantee + `","scopes":["adcp:media_buying"]}],"exclusive_grants":true}`}))
			hookCalled := false
			hook := func(ctx context.Context, req GrantRequest) (bool, error) { hookCalled = true; return yes(ctx, req) }
			res, err := (&GrantEvaluator{Records: r.eval, SellerAuthorization: hook}).Check(context.Background(), req)
			require.NoError(t, err)
			require.False(t, res.Authorized)
			require.Equal(t, GrantReasonAbsent, res.Reason)
			require.False(t, hookCalled, "the grant's scopes keep grants[] exhaustive")

			// Scopes the grant does not name stay at the seller's discretion.
			creative := req
			creative.Scope = ScopeCreativeGeneration
			res, err = (&GrantEvaluator{Records: r.eval, SellerAuthorization: yes}).Check(context.Background(), creative)
			require.NoError(t, err)
			require.True(t, res.Authorized)

			// Without exclusive_grants, the uncovered claim is at the
			// seller's discretion.
			r = grantRig(t, with(baseRecords, map[string]string{"brand-7k3q.com": `{"grants":[{"grantee":"` + grantee + `","scopes":["adcp:media_buying"]}]}`}))
			res, err = (&GrantEvaluator{Records: r.eval, SellerAuthorization: yes}).Check(context.Background(), req)
			require.NoError(t, err)
			require.True(t, res.Authorized)
		})
	}
}
