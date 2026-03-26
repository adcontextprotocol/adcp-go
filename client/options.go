package client

import (
	"net/http"
	"time"
)

// Option configures the Client.
type Option func(*Client)

// WithTimeout sets the overall HTTP request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithDecorrelationMax sets the maximum random delay added to the identity
// request to prevent timing correlation attacks. Default: 5ms.
func WithDecorrelationMax(d time.Duration) Option {
	return func(c *Client) {
		c.decorrelationMax = d
	}
}

// WithHTTPClient replaces the default HTTP client entirely.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithRetry enables retry with exponential backoff for transient errors.
func WithRetry(maxAttempts int, baseDelay time.Duration) Option {
	return func(c *Client) {
		c.maxRetries = maxAttempts
		c.retryBaseDelay = baseDelay
	}
}
