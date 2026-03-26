// Package client provides a TMP client for publishers. It handles the full
// match flow: fire context and identity requests in parallel with temporal
// decorrelation, join results locally, and report exposure.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// Client is a TMP client for publishers.
type Client struct {
	contextURL  string
	identityURL string

	httpClient       *http.Client
	decorrelationMax time.Duration
	maxRetries       int
	retryBaseDelay   time.Duration

	// randDelay returns a random duration in [0, max) for temporal decorrelation.
	randDelay func(max time.Duration) time.Duration
}

// New creates a TMP client. contextURL is the router's context match endpoint
// (e.g., "http://router:8080/tmp/context"). identityURL is the base URL for
// identity match and expose (e.g., "http://router:8080/tmp").
func New(contextURL, identityURL string, opts ...Option) *Client {
	c := &Client{
		contextURL:  contextURL,
		identityURL: identityURL,
		httpClient: &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   1 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
		decorrelationMax: 5 * time.Millisecond,
		randDelay:        defaultRandDelay,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func defaultRandDelay(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}

// Expose notifies the identity provider that a user was exposed to a package.
func (c *Client) Expose(ctx context.Context, req *tmp.ExposeRequest) (*tmp.ExposeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("tmp client: marshal expose: %w", err)
	}

	data, err := c.doWithRetry(ctx, c.identityURL+"/expose", body)
	if err != nil {
		return nil, fmt.Errorf("tmp client: expose: %w", err)
	}

	var resp tmp.ExposeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("tmp client: unmarshal expose response: %w", err)
	}
	return &resp, nil
}

func (c *Client) doPost(ctx context.Context, url string, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		var errResp tmp.ErrorResponse
		if json.Unmarshal(data, &errResp) == nil && errResp.Code != "" {
			return nil, &RequestError{StatusCode: resp.StatusCode, Response: errResp}
		}
		return nil, &RequestError{
			StatusCode: resp.StatusCode,
			Response: tmp.ErrorResponse{
				Code:    tmp.ErrorCodeInternalError,
				Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(data)),
			},
		}
	}

	return data, nil
}

func (c *Client) doWithRetry(ctx context.Context, url string, body []byte) ([]byte, error) {
	if c.maxRetries <= 0 {
		return c.doPost(ctx, url, body)
	}

	var lastErr error
	for i := range c.maxRetries {
		data, err := c.doPost(ctx, url, body)
		if err == nil {
			return data, nil
		}
		lastErr = err

		if !isRetryable(err) {
			return nil, err
		}

		if i < c.maxRetries-1 {
			delay := c.retryBaseDelay * time.Duration(1<<uint(i))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, lastErr
}

func isRetryable(err error) bool {
	if reqErr, ok := err.(*RequestError); ok {
		return reqErr.StatusCode >= 500 || reqErr.StatusCode == 429
	}
	return true // network errors are retryable
}

// RequestError represents an error response from a TMP endpoint.
type RequestError struct {
	StatusCode int
	Response   tmp.ErrorResponse
}

func (e *RequestError) Error() string {
	if e.Response.Message != "" {
		return fmt.Sprintf("tmp %s (HTTP %d): %s", e.Response.Code, e.StatusCode, e.Response.Message)
	}
	return fmt.Sprintf("tmp %s (HTTP %d)", e.Response.Code, e.StatusCode)
}
