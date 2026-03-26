package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// MatchRequest is a publisher-friendly request combining context and identity.
type MatchRequest struct {
	RequestID    string
	PropertyID   string
	PropertyType tmp.PropertyType
	PlacementID  string
	Artifacts    []string
	Geo          *tmp.Geo
	UserToken    string
	UIDType      tmp.UIDType
	PackageIDs   []string
	MediaBuyIDs  map[string]string // package_id -> media_buy_id
	FormatIDs    map[string][]string // package_id -> format_ids
}

// EligiblePackage is a package that passed both context and identity checks.
type EligiblePackage struct {
	PackageID   string
	Offer       tmp.Offer
	IntentScore *float64
}

// MatchResult is the combined result of context + identity match.
type MatchResult struct {
	RequestID        string
	EligiblePackages []EligiblePackage
	Signals          *tmp.Signals
	Timing           Timing
}

// Timing captures latency for observability.
type Timing struct {
	ContextLatency      time.Duration
	IdentityLatency     time.Duration
	TotalLatency        time.Duration
	DecorrelationDelay  time.Duration
}

// Match fires context and identity requests in parallel, joins results locally.
func (c *Client) Match(ctx context.Context, req *MatchRequest) (*MatchResult, error) {
	totalStart := time.Now()

	// Build requests
	ctxReq := c.buildContextRequest(req)
	idReq := c.buildIdentityRequest(req)

	ctxBody, err := json.Marshal(ctxReq)
	if err != nil {
		return nil, fmt.Errorf("tmp client: marshal context request: %w", err)
	}
	idBody, err := json.Marshal(idReq)
	if err != nil {
		return nil, fmt.Errorf("tmp client: marshal identity request: %w", err)
	}

	// Fire both in parallel
	var (
		mu          sync.Mutex
		ctxResp     *tmp.ContextMatchResponse
		idResp      *tmp.IdentityMatchResponse
		ctxErr      error
		idErr       error
		ctxLatency  time.Duration
		idLatency   time.Duration
		decorDelay  time.Duration
		wg          sync.WaitGroup
	)

	wg.Add(2)

	// Context request — fires immediately
	go func() {
		defer wg.Done()
		start := time.Now()
		data, err := c.doWithRetry(ctx, c.contextURL, ctxBody)
		mu.Lock()
		ctxLatency = time.Since(start)
		mu.Unlock()
		if err != nil {
			ctxErr = fmt.Errorf("context match: %w", err)
			return
		}
		var resp tmp.ContextMatchResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			ctxErr = fmt.Errorf("unmarshal context response: %w", err)
			return
		}
		ctxResp = &resp
	}()

	// Identity request — delayed by temporal decorrelation
	go func() {
		defer wg.Done()
		delay := c.randDelay(c.decorrelationMax)
		mu.Lock()
		decorDelay = delay
		mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				idErr = ctx.Err()
				return
			}
		}
		start := time.Now()
		data, err := c.doWithRetry(ctx, c.identityURL+"/identity", idBody)
		mu.Lock()
		idLatency = time.Since(start)
		mu.Unlock()
		if err != nil {
			idErr = fmt.Errorf("identity match: %w", err)
			return
		}
		var resp tmp.IdentityMatchResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			idErr = fmt.Errorf("unmarshal identity response: %w", err)
			return
		}
		idResp = &resp
	}()

	wg.Wait()

	// Both must succeed
	if ctxErr != nil {
		return nil, fmt.Errorf("tmp client: %w", ctxErr)
	}
	if idErr != nil {
		return nil, fmt.Errorf("tmp client: %w", idErr)
	}

	// Join: package is eligible only if context offers it AND identity says eligible
	result := c.joinResults(req.RequestID, ctxResp, idResp)
	result.Timing = Timing{
		ContextLatency:     ctxLatency,
		IdentityLatency:    idLatency,
		TotalLatency:       time.Since(totalStart),
		DecorrelationDelay: decorDelay,
	}

	return result, nil
}

func (c *Client) buildContextRequest(req *MatchRequest) *tmp.ContextMatchRequest {
	var pkgs []tmp.AvailablePackage
	for _, pid := range req.PackageIDs {
		pkg := tmp.AvailablePackage{
			PackageID:  pid,
			MediaBuyID: req.MediaBuyIDs[pid],
			FormatIDs:  req.FormatIDs[pid],
		}
		pkgs = append(pkgs, pkg)
	}
	return &tmp.ContextMatchRequest{
		RequestID:     req.RequestID,
		PropertyID:    req.PropertyID,
		PropertyType:  req.PropertyType,
		PlacementID:   req.PlacementID,
		Artifacts:     req.Artifacts,
		Geo:           req.Geo,
		AvailablePkgs: pkgs,
	}
}

func (c *Client) buildIdentityRequest(req *MatchRequest) *tmp.IdentityMatchRequest {
	return &tmp.IdentityMatchRequest{
		RequestID:  req.RequestID,
		UserToken:  req.UserToken,
		UIDType:    req.UIDType,
		PackageIDs: req.PackageIDs,
	}
}

func (c *Client) joinResults(requestID string, ctx *tmp.ContextMatchResponse, id *tmp.IdentityMatchResponse) *MatchResult {
	// Build eligibility map
	eligMap := make(map[string]*tmp.PackageEligibility, len(id.Eligibility))
	for i := range id.Eligibility {
		eligMap[id.Eligibility[i].PackageID] = &id.Eligibility[i]
	}

	// Intersect: only packages that have an offer AND are eligible
	var eligible []EligiblePackage
	for _, offer := range ctx.Offers {
		e, ok := eligMap[offer.PackageID]
		if !ok || !e.Eligible {
			continue
		}
		eligible = append(eligible, EligiblePackage{
			PackageID:   offer.PackageID,
			Offer:       offer,
			IntentScore: e.IntentScore,
		})
	}

	return &MatchResult{
		RequestID:        requestID,
		EligiblePackages: eligible,
		Signals:          ctx.Signals,
	}
}
