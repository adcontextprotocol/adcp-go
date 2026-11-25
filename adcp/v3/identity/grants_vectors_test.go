package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Cross-SDK delegation-chain vectors (L2 Grants in trust records), vendored
// from adcontextprotocol/adcp at the commit in UPSTREAM.json. Each vector is
// an acts-for claim, the house the brand resolves to, and the grants each
// registrable domain publishes; GrantEvaluator must reach the expected
// outcome.

const delegationChainDir = "testdata/delegation-chains"

type delegationChainVector struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Input       struct {
		Brand struct {
			Domain    string   `json:"domain"`
			BrandID   string   `json:"brand_id"`
			Countries []string `json:"countries"`
		} `json:"brand"`
		ParentOrganization *string `json:"parent_organization"`
		AccountOperator    string  `json:"account_operator"`
		Signer             struct {
			Operator string `json:"operator"`
			Origin   string `json:"origin"`
		} `json:"signer"`
		TrustRecords map[string]map[string]any `json:"trust_records"`
		CheckTime    time.Time                 `json:"check_time"`
		Scope        string                    `json:"scope"`
	} `json:"input"`
	Expected struct {
		Outcome string `json:"outcome"`
	} `json:"expected"`
}

// The vectors use .example names, which have no registrable domain under
// the PSL. Each maps to a registrable placeholder; domains are opaque labels.
var exampleTLD = regexp.MustCompile(`\.example\b`)

func loadDelegationChainVectors(t *testing.T) []delegationChainVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(delegationChainDir, "delegation-chains.json"))
	require.NoError(t, err)
	var file struct {
		Vectors []delegationChainVector `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(exampleTLD.ReplaceAll(raw, []byte("-dcv.com")), &file))
	require.NotEmpty(t, file.Vectors)
	return file.Vectors
}

// TestDelegationChainVectorsArePinned: the vendored vectors match the
// checksum recorded with their provenance in UPSTREAM.json.
func TestDelegationChainVectorsArePinned(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(delegationChainDir, "UPSTREAM.json"))
	require.NoError(t, err)
	var up struct {
		Files map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(b, &up))
	entries, err := os.ReadDir(delegationChainDir)
	require.NoError(t, err)
	require.Len(t, up.Files, len(entries)-1, "every vendored file is pinned")
	for name, want := range up.Files {
		data, err := os.ReadFile(filepath.Join(delegationChainDir, name)) // #nosec G304 -- names from the committed pin file.
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		require.Equal(t, want, "sha256:"+hex.EncodeToString(sum[:]), name)
	}
}

func TestDelegationChainVectorsCoverEveryOutcome(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range loadDelegationChainVectors(t) {
		seen[v.Expected.Outcome] = true
	}
	var outcomes []string
	for o := range seen {
		outcomes = append(outcomes, o)
	}
	sort.Strings(outcomes)
	require.Equal(t, []string{"authorized", "grant_absent", "implicit", "seller_discretion"}, outcomes)
}

func TestDelegationChainVectors(t *testing.T) {
	sources := map[string]string{"implicit": GrantSourceImplicit, "authorized": GrantSourceGrant, "seller_discretion": GrantSourceSeller}
	for _, v := range loadDelegationChainVectors(t) {
		t.Run(v.ID, func(t *testing.T) {
			in := v.Input
			records := map[string]map[string]any{}
			for domain, rec := range in.TrustRecords {
				records[domain] = rec
			}
			// Every operator a chain reaches lists the signer, so the
			// listing is never the reason a vector fails.
			signerRec := map[string]any{}
			for k, val := range records[in.Signer.Operator] {
				signerRec[k] = val
			}
			signerRec["agents"] = []map[string]any{{"url": in.Signer.Origin, "roles": []string{RoleBuying}}}
			records[in.Signer.Operator] = signerRec

			r := newEvalRig(t)
			r.now = in.CheckTime
			for domain, rec := range records {
				body, err := json.Marshal(rec)
				require.NoError(t, err)
				r.server.set(trustURL(domain), 200, string(body))
			}
			sellerCalls := 0
			g := &GrantEvaluator{
				Records: r.eval,
				ResolveHouse: func(context.Context, string, string) (*BrandHouse, error) {
					if in.ParentOrganization == nil {
						return nil, nil
					}
					return &BrandHouse{Domain: *in.ParentOrganization, BrandID: in.Brand.BrandID}, nil
				},
				SellerAuthorization: func(context.Context, GrantRequest) (bool, error) {
					sellerCalls++
					return true, nil
				},
			}
			res, err := g.Check(context.Background(), GrantRequest{
				AgentOrigin:     in.Signer.Origin,
				BrandDomain:     in.Brand.Domain,
				AccountOperator: in.AccountOperator,
				Scope:           in.Scope,
				BrandID:         in.Brand.BrandID,
				Countries:       in.Brand.Countries,
			})
			require.NoError(t, err)
			if v.Expected.Outcome == "grant_absent" {
				require.False(t, res.Authorized, v.Description)
				require.Equal(t, GrantReasonAbsent, res.Reason, v.Description)
				require.Zero(t, sellerCalls, "an exclusive record never defers to the seller")
				return
			}
			require.True(t, res.Authorized, "%s: %s", v.Description, res.Reason)
			require.Equal(t, sources[v.Expected.Outcome], res.Source, v.Description)
		})
	}
}
