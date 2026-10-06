package adcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mutatingTools lists every AdCP task whose request schema requires
// idempotency_key. TestMutatingToolsMatchSchemas keeps it in sync with the
// bundle. Register wraps these with WithIdempotency automatically.
var mutatingTools = map[string]bool{
	"accept_proposal": true, "acquire_rights": true, "activate_signal": true,
	"build_creative": true, "buy_products": true, "calibrate_content": true,
	"control_media_buy": true, "create_collection_list": true,
	"create_content_standards": true, "create_media_buy": true,
	"create_property_list": true, "creative_approval": true,
	"decline_proposals": true, "delete_collection_list": true,
	"delete_property_list": true, "log_event": true,
	"provide_performance_feedback": true, "refine_proposals": true,
	"report_plan_adjustment": true, "report_plan_outcome": true,
	"report_usage": true, "request_proposals": true,
	"si_initiate_session": true, "si_send_message": true,
	"sync_accounts": true, "sync_agent_notification_configs": true,
	"sync_audiences": true, "sync_catalogs": true, "sync_creatives": true,
	"sync_event_sources": true, "sync_governance": true, "sync_plans": true,
	"sync_principal": true, "sync_reporting_receipts": true,
	"sync_reporting_status": true, "update_collection_list": true,
	"update_content_standards": true, "update_media_buy": true,
	"update_property_list": true, "update_rights": true,
}

// errNotCached carries a tool-level error result out of Store.Wrap without
// caching it, so a retry with the same key re-executes.
var errNotCached = errors.New("adcp: error result is not cached")

// WithIdempotency wraps a mutating AddTool handler with AdCP replay
// semantics: a repeated idempotency_key with the same payload returns the
// cached response with replayed=true; a different payload returns
// IDEMPOTENCY_CONFLICT; a concurrent duplicate returns IDEMPOTENCY_IN_FLIGHT.
// Error results are never cached. A nil store returns handler unchanged.
//
// A store requires an authenticated principal: keys are scoped to the
// principal in ctx, injected by your auth middleware with
// idempotency.WithPrincipal. The MCP transport session ID is never used (a
// stateless transport accepts a client-chosen one). Without a principal the
// call is refused with SERVICE_UNAVAILABLE and the handler does not run.
//
// Checks run in this order: a malformed idempotency_key, then a missing one
// (when the store requires keys), is INVALID_REQUEST; a missing optional key
// runs the handler uncached; only then is the principal resolved.
//
// Register applies this to every mutating tool. Use it directly for tools
// you add with AddTool:
//
//	adcp.AddTool(server, "update_media_buy", "Update a media buy",
//	    adcp.WithIdempotency(store, updateMediaBuy))
func WithIdempotency[In any](store *idempotency.Store, handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	if store == nil {
		return handler
	}
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		raw := []byte(req.Params.Arguments)
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return Errorf("INVALID_REQUEST", ErrorOptions{Message: "Tool arguments must be a JSON object", Recovery: "correctable"})
		}
		present, err := checkKey(args)
		if err != nil {
			return idempotencyErrorResult(err)
		}
		if !present {
			if store.KeyRequired() {
				return Errorf("INVALID_REQUEST", ErrorOptions{Message: "idempotency_key is required on state-changing requests", Recovery: "correctable", Field: "idempotency_key"})
			}
			return handler(ctx, req, input)
		}
		hashed, err := bindToolName(args, req.Params.Name)
		if err != nil {
			return Errorf("INVALID_REQUEST", ErrorOptions{Message: "Tool arguments must be a JSON object", Recovery: "correctable"})
		}
		if idempotency.PrincipalFromContext(ctx) == "" {
			return serviceUnavailable("Idempotency principal could not be resolved; authenticate callers (e.g. bearer auth) before enabling idempotency.")
		}

		var fresh *mcp.CallToolResult
		var freshOut any
		var handlerErr error
		res, err := store.Wrap(func(ctx context.Context, _ []byte) ([]byte, error) {
			result, out, err := handler(ctx, req, input)
			if err != nil {
				handlerErr = err
				return nil, err
			}
			fresh, freshOut = result, out
			if result == nil || result.IsError {
				return nil, errNotCached
			}
			structured := result.StructuredContent
			if structured == nil {
				structured = jsonRoundTrip(out)
			}
			return json.Marshal(structured)
		})(ctx, hashed)
		switch {
		case errors.Is(err, idempotency.ErrReleaseFailed):
			// The key is stuck behind its claim; even a handler error is
			// reported as transient so the buyer reconciles before retrying.
			return serviceUnavailable("The idempotency claim could not be released. Reconcile by natural key before retrying.")
		case errors.Is(err, idempotency.ErrRecordFailed):
			return serviceUnavailable("The response could not be recorded in the idempotency store. Reconcile by natural key before retrying.")
		case errors.Is(err, errNotCached):
			return fresh, freshOut, nil
		case handlerErr != nil:
			return nil, nil, handlerErr
		case err != nil:
			return idempotencyErrorResult(err)
		case !res.Replayed:
			return fresh, freshOut, nil
		}

		var data map[string]any
		if err := json.Unmarshal(res.Response, &data); err != nil {
			return Errorf("INTERNAL_ERROR", ErrorOptions{Message: "cached response is not a JSON object"})
		}
		data["replayed"] = true
		delete(data, "context")
		return attachContext(buildResult("Replayed cached response", data), requestContext(raw)), data, nil
	}
}

// toolHashField is a reserved top-level field WithIdempotency adds to the
// payload it hashes (never to the handler input or the response), so the same
// key and arguments on two different tools conflict instead of replaying the
// other tool's response.
const toolHashField = "$adcp_tool"

// bindToolName returns args with the tool name added under toolHashField.
// It copies args so the caller's map is unchanged.
func bindToolName(args map[string]json.RawMessage, tool string) ([]byte, error) {
	name, err := json.Marshal(tool)
	if err != nil {
		return nil, err
	}
	m := make(map[string]json.RawMessage, len(args)+1)
	for k, v := range args {
		m[k] = v
	}
	m[toolHashField] = name
	return json.Marshal(m)
}

// checkKey reports whether args carries idempotency_key and, if so, whether
// it is well formed.
func checkKey(args map[string]json.RawMessage) (present bool, err error) {
	raw, ok := args["idempotency_key"]
	if !ok {
		return false, nil
	}
	var key string
	if err := json.Unmarshal(raw, &key); err != nil {
		return true, &idempotency.InvalidKeyError{Reason: "not a string"}
	}
	return true, idempotency.Validate(key)
}

// withKeyFormatCheck rejects a present but malformed idempotency_key when
// replay is disabled, so buyers learn of bad keys before relying on them.
func withKeyFormatCheck[In any](handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		var args map[string]json.RawMessage
		_ = json.Unmarshal(req.Params.Arguments, &args)
		if _, err := checkKey(args); err != nil {
			return idempotencyErrorResult(err)
		}
		return handler(ctx, req, input)
	}
}

// requestContext returns the caller's context object so replays echo the
// current request's context, not the one cached with the first response.
func requestContext(raw []byte) any {
	var env struct {
		Context any `json:"context"`
	}
	_ = json.Unmarshal(raw, &env)
	return env.Context
}

// serviceUnavailable is a transient AdCP error that never carries backend
// error text or the idempotency key.
func serviceUnavailable(msg string) (*mcp.CallToolResult, any, error) {
	return Errorf("SERVICE_UNAVAILABLE", ErrorOptions{Message: msg, Recovery: "transient"})
}

// idempotencyErrorResult maps idempotency store errors onto AdCP error
// results. Anything unrecognized is a store failure (backend, scope or hash)
// and becomes SERVICE_UNAVAILABLE without its raw text.
func idempotencyErrorResult(err error) (*mcp.CallToolResult, any, error) {
	var (
		inFlight *idempotency.InFlightError
		conflict *idempotency.ConflictError
		expired  *idempotency.ExpiredError
		missing  *idempotency.MissingKeyError
		invalid  *idempotency.InvalidKeyError
	)
	switch {
	case errors.As(err, &inFlight):
		return Errorf(idempotency.CodeIdempotencyInFlight, ErrorOptions{
			Message:    "A request with this idempotency_key is still being processed",
			Recovery:   "transient",
			RetryAfter: int(inFlight.RetryAfter / time.Second),
			Suggestion: "Retry after retry_after seconds with the SAME idempotency_key.",
		})
	case errors.As(err, &conflict):
		return Errorf(idempotency.CodeIdempotencyConflict, ErrorOptions{
			Message:    "idempotency_key was already used with a different payload",
			Recovery:   "correctable",
			Field:      "idempotency_key",
			Suggestion: "Resend the original payload, or use a new idempotency_key for a new request.",
		})
	case errors.As(err, &expired):
		return Errorf(idempotency.CodeIdempotencyExpired, ErrorOptions{
			Message:    "idempotency_key is past the seller's replay window",
			Recovery:   "correctable",
			Field:      "idempotency_key",
			Suggestion: "Check whether the original request took effect before retrying with a new idempotency_key.",
		})
	case errors.As(err, &missing), errors.As(err, &invalid):
		return Errorf("INVALID_REQUEST", ErrorOptions{Message: err.Error(), Recovery: "correctable", Field: "idempotency_key"})
	}
	return serviceUnavailable("Idempotency check failed")
}
