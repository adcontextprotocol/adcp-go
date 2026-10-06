package adcp

import (
	"context"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PrincipalFromContext returns the principal for the current tool call: the
// verified TokenInfo.UserID from WithBearerAuth, or a value set with
// idempotency.WithPrincipal in MCP receiving middleware; "" when anonymous. ResolveAccount should use it to check that the principal may
// act for the requested account — the SDK does not know your account model.
func PrincipalFromContext(ctx context.Context) string {
	return idempotency.PrincipalFromContext(ctx)
}

// withRequestPrincipal copies the bearer token's UserID into ctx.
func withRequestPrincipal(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return ctx
	}
	return idempotency.WithPrincipal(ctx, req.Extra.TokenInfo.UserID)
}

// requirePrincipal rejects tools/call without an authenticated principal,
// except get_adcp_capabilities, which buyers call before authenticating.
func requirePrincipal(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && p.Name == "get_adcp_capabilities" {
			return next(ctx, method, req)
		}
		if extra := req.GetExtra(); extra != nil && extra.TokenInfo != nil && extra.TokenInfo.UserID != "" {
			return next(ctx, method, req)
		}
		result, _, _ := Errorf("AUTH_REQUIRED", ErrorOptions{
			Message:    "This tool requires an authenticated principal",
			Suggestion: "Send an Authorization: Bearer token.",
		})
		return result, nil
	}
}
