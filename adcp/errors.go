package adcp

import (
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrorOptions configures an AdCP error response.
type ErrorOptions struct {
	Message    string
	Recovery   string // "retry", "revise", "contact_support", "terminal"
	Field      string
	Suggestion string
	RetryAfter int
	Details    map[string]any
}

// NewError creates an error that Register handlers can return to produce a
// typed AdCP error response instead of a generic INTERNAL_ERROR.
//
// Usage in a handler:
//
//	return nil, adcp.NewError("BUDGET_TOO_LOW", adcp.ErrorOptions{
//	    Message: "Budget $500 is below the $1,000 minimum for video",
//	    Field:   "budget",
//	})
func NewError(code string, opts ErrorOptions) error {
	return &handlerError{code: code, opts: opts}
}

type handlerError struct {
	code string
	opts ErrorOptions
}

func (e *handlerError) Error() string {
	if e.opts.Message != "" {
		return e.opts.Message
	}
	return e.code
}

// errorToResult converts a handler error to an MCP CallToolResult. If the
// error is an AdCP typed error (from NewError), it uses the code and options.
// Otherwise it wraps as INTERNAL_ERROR.
func errorToResult(err error) (*mcp.CallToolResult, any, error) {
	var he *handlerError
	if errors.As(err, &he) {
		return Errorf(he.code, he.opts)
	}
	// Don't leak internal error details to the calling agent.
	return Errorf("INTERNAL_ERROR", ErrorOptions{Message: "An internal error occurred"})
}

type adcpErrorPayload struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Recovery   string         `json:"recovery"`
	Field      string         `json:"field,omitempty"`
	Suggestion string         `json:"suggestion,omitempty"`
	RetryAfter int            `json:"retry_after,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

type adcpErrorWrapper struct {
	ADCPError adcpErrorPayload `json:"adcp_error"`
}

// Error builds an L3-compliant AdCP error response.
// Returns isError: true + structuredContent.adcp_error.
func Error[T any](code string, opts ErrorOptions) (*mcp.CallToolResult, T, error) {
	recovery := opts.Recovery
	if recovery == "" {
		recovery = defaultRecovery(code)
	}

	payload := adcpErrorPayload{
		Code:       code,
		Message:    opts.Message,
		Recovery:   recovery,
		Field:      opts.Field,
		Suggestion: opts.Suggestion,
		RetryAfter: opts.RetryAfter,
		Details:    opts.Details,
	}

	wrapper := adcpErrorWrapper{ADCPError: payload}
	b, _ := json.Marshal(wrapper)

	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
		IsError:           true,
		StructuredContent: jsonRoundTrip(wrapper),
	}

	var zero T
	return result, zero, nil
}

// Errorf is a convenience wrapper for Error that returns (*mcp.CallToolResult, any, error),
// matching the adcp.AddTool handler signature without requiring a type parameter.
func Errorf(code string, opts ErrorOptions) (*mcp.CallToolResult, any, error) {
	return Error[any](code, opts)
}

func defaultRecovery(code string) string {
	switch code {
	case "RATE_LIMITED":
		return "retry"
	case "BUDGET_TOO_LOW", "INVALID_REQUEST", "MISSING_FIELD", "INVALID_FIELD",
		"ACCOUNT_NOT_FOUND", "TERMS_REJECTED":
		return "revise"
	case "INTERNAL_ERROR", "SERVICE_UNAVAILABLE":
		return "contact_support"
	default:
		return "terminal"
	}
}
