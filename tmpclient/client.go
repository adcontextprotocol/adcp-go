// Package tmpclient provides a publisher-side TMP client library.
//
// It handles the full activation flow: fire context match and identity match
// in parallel, join results locally, and report exposures.
package tmpclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

const maxResponseBody = 1 << 20 // 1 MB

// Client calls TMP router and identity agent endpoints on behalf of a publisher.
type Client struct {
	routerURL    string
	http         *http.Client
	genRequestID func() string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.http = c }
}

// WithRequestIDFunc overrides the request ID generator.
func WithRequestIDFunc(f func() string) Option {
	return func(cl *Client) { cl.genRequestID = f }
}

// NewClient creates a TMP publisher client.
// routerURL is the base URL of the TMP router (e.g., "http://localhost:8080").
func NewClient(routerURL string, opts ...Option) *Client {
	c := &Client{
		routerURL:    strings.TrimRight(routerURL, "/"),
		genRequestID: generateRequestID,
		http: &http.Client{
			Timeout: 200 * time.Millisecond,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ContextMatch sends a context match request to the router.
// Populates RequestID if empty.
func (c *Client) ContextMatch(ctx context.Context, req *tmproto.ContextMatchRequest) (*tmproto.ContextMatchResponse, error) {
	if req.RequestID == "" {
		req.RequestID = c.genRequestID()
	}
	if err := tmproto.ValidateContextRequest(req); err != nil {
		return nil, fmt.Errorf("validate context request: %w", err)
	}

	var resp tmproto.ContextMatchResponse
	if err := c.post(ctx, c.routerURL+"/tmp/context", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// IdentityMatch sends an identity match request to the router.
// Populates RequestID if empty.
func (c *Client) IdentityMatch(ctx context.Context, req *tmproto.IdentityMatchRequest) (*tmproto.IdentityMatchResponse, error) {
	if req.RequestID == "" {
		req.RequestID = c.genRequestID()
	}
	if err := tmproto.ValidateIdentityRequest(req); err != nil {
		return nil, fmt.Errorf("validate identity request: %w", err)
	}

	var resp tmproto.IdentityMatchResponse
	if err := c.post(ctx, c.routerURL+"/tmp/identity", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Activate runs the full TMP flow:
//  1. Fire ContextMatch and IdentityMatch in parallel
//  2. Join results locally (intersect context offers with identity eligibility)
//  3. Return activations sorted by intent score (descending)
func (c *Client) Activate(ctx context.Context, params *ActivateParams) (*ActivateResult, error) {
	ctxReqID := c.genRequestID()
	idReqID := c.genRequestID()

	ctxReq := &tmproto.ContextMatchRequest{
		RequestID:    ctxReqID,
		PropertyID:   params.PropertyID,
		PropertyType: params.PropertyType,
		PlacementID:  params.PlacementID,
		Geo:          params.Geo,
		ArtifactRefs: params.ArtifactRefs,
		PackageIDs:   params.PackageIDs,
	}

	idReq := &tmproto.IdentityMatchRequest{
		RequestID:  idReqID,
		UserToken:  params.UserToken,
		UIDType:    params.UIDType,
		PackageIDs: params.PackageIDs,
		Consent:    params.Consent,
		Country:    params.Country,
	}

	// Validate both before firing either.
	if err := tmproto.ValidateContextRequest(ctxReq); err != nil {
		return nil, fmt.Errorf("validate context request: %w", err)
	}
	if err := tmproto.ValidateIdentityRequest(idReq); err != nil {
		return nil, fmt.Errorf("validate identity request: %w", err)
	}

	// Fire both in parallel.
	var (
		ctxResp *tmproto.ContextMatchResponse
		idResp  *tmproto.IdentityMatchResponse
		ctxErr  error
		idErr   error
		wg      sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		ctxResp, ctxErr = c.ContextMatch(ctx, ctxReq)
	}()
	go func() {
		defer wg.Done()
		idResp, idErr = c.IdentityMatch(ctx, idReq)
	}()
	wg.Wait()

	if ctxErr != nil {
		return nil, fmt.Errorf("context match: %w", ctxErr)
	}
	if idErr != nil {
		return nil, fmt.Errorf("identity match: %w", idErr)
	}

	// Join results.
	activations := joinResults(ctxResp, idResp)

	return &ActivateResult{
		Activations: activations,
		Signals:     ctxResp.Signals,
		Tmpx:        idResp.Tmpx,
		Context:     ctxResp,
		Identity:    idResp,
	}, nil
}

// joinResults intersects context offers with identity eligibility.
func joinResults(ctxResp *tmproto.ContextMatchResponse, idResp *tmproto.IdentityMatchResponse) []Activation {
	// Build eligibility set from flat list.
	eligSet := make(map[string]bool, len(idResp.EligiblePackageIDs))
	for _, id := range idResp.EligiblePackageIDs {
		eligSet[id] = true
	}

	var activations []Activation
	for _, offer := range ctxResp.Offers {
		if !eligSet[offer.PackageID] {
			continue
		}
		activations = append(activations, Activation{
			PackageID: offer.PackageID,
			Offer:     offer,
		})
	}

	return activations
}

// post sends a JSON POST request and unmarshals the response.
func (c *Client) post(ctx context.Context, url string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http post %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseTMPError(resp.StatusCode, respBody)
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

func parseTMPError(statusCode int, body []byte) error {
	var errResp tmproto.ErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Code != "" {
		return &TMPError{
			StatusCode: statusCode,
			Code:       errResp.Code,
			Message:    errResp.Message,
			RequestID:  errResp.RequestID,
		}
	}
	return &TMPError{
		StatusCode: statusCode,
		Message:    string(body),
	}
}

func generateRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 2
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
