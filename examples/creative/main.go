package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var permissiveSchema = map[string]any{"type": "object"}

const agentURL = "http://localhost:3001/mcp"

type storedCreative struct {
	CreativeID string
	Name       string
	FormatID   adcp.CreativeFormatID
	Status     string
	Assets     map[string]any
}

type store struct {
	mu        sync.RWMutex
	creatives map[string]*storedCreative
}

func newStore() *store {
	return &store{creatives: make(map[string]*storedCreative)}
}

var formats = []adcp.CreativeFormat{
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250"},
		Name:     "Display 300x250",
		Renders:  []adcp.Render{{Width: 300, Height: 250}},
		Assets:   []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}}},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_728x90"},
		Name:     "Display 728x90",
		Renders:  []adcp.Render{{Width: 728, Height: 90}},
		Assets:   []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}}},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "video_30s"},
		Name:     "Video 30s Pre-Roll",
		Renders:  []adcp.Render{{Width: 1920, Height: 1080}},
		Assets:   []adcp.AssetSlot{{ItemType: "individual", AssetID: "video", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "native_content"},
		Name:     "Native Content",
		Renders:  []adcp.Render{{Width: 600, Height: 400}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}},
			{ItemType: "individual", AssetID: "headline", AssetType: "text", Required: true},
			{ItemType: "individual", AssetID: "description", AssetType: "text", Required: false},
		},
	},
}

func makeResult(data any, summary string) (*mcp.CallToolResult, error) {
	b, _ := json.Marshal(data)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: summary + "\n" + string(b)}},
		StructuredContent: data,
	}, nil
}

func makeErrorResult(code, message string) (*mcp.CallToolResult, error) {
	errData := map[string]any{"adcp_error": map[string]any{"code": code, "message": message, "recovery": "terminal"}}
	b, _ := json.Marshal(errData)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		IsError:           true,
		StructuredContent: errData,
	}, nil
}

func parseArgs(req *mcp.CallToolRequest) map[string]any {
	args := make(map[string]any)
	if len(req.Params.Arguments) > 0 {
		json.Unmarshal(req.Params.Arguments, &args)
	}
	return args
}

func stringVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func main() {
	s := newStore()

	createServer := func() *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "creative-agent", Version: "1.0.0"}, nil)

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "get_adcp_capabilities", Description: "Returns agent capabilities",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return makeResult(&adcp.CapabilitiesData{
				ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
				SupportedProtocols: []string{"creative"},
			}, "Agent capabilities retrieved")
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "list_creative_formats", Description: "List available creative formats",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return makeResult(map[string]any{"formats": formats}, fmt.Sprintf("Found %d formats", len(formats)))
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "sync_creatives", Description: "Accept and store creatives",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := parseArgs(req)
			creativesRaw, _ := args["creatives"].([]any)

			s.mu.Lock()
			defer s.mu.Unlock()

			results := make([]adcp.CreativeResult, 0)
			for _, raw := range creativesRaw {
				c, _ := raw.(map[string]any)
				if c == nil {
					continue
				}
				creativeID := stringVal(c, "creative_id")
				name := stringVal(c, "name")

				var fid adcp.CreativeFormatID
				if fidMap, ok := c["format_id"].(map[string]any); ok {
					fid.AgentURL = stringVal(fidMap, "agent_url")
					fid.ID = stringVal(fidMap, "id")
				}

				action := "created"
				if _, exists := s.creatives[creativeID]; exists {
					action = "updated"
				}
				assets, _ := c["assets"].(map[string]any)
				s.creatives[creativeID] = &storedCreative{
					CreativeID: creativeID, Name: name, FormatID: fid, Status: "accepted", Assets: assets,
				}
				results = append(results, adcp.CreativeResult{CreativeID: creativeID, Action: action, Status: "accepted"})
			}

			return makeResult(map[string]any{"creatives": results, "results": results}, fmt.Sprintf("Synced %d creatives", len(results)))
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "list_creatives", Description: "List creatives in the library",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := parseArgs(req)
			s.mu.RLock()
			defer s.mu.RUnlock()

			items := make([]adcp.CreativeListItem, 0)
			for _, c := range s.creatives {
				// Apply format filter if present
				if filtersMap, ok := args["filters"].(map[string]any); ok {
					if fids, ok := filtersMap["format_ids"].([]any); ok && len(fids) > 0 {
						match := false
						for _, fid := range fids {
							if fidMap, ok := fid.(map[string]any); ok {
								if stringVal(fidMap, "agent_url") == c.FormatID.AgentURL && stringVal(fidMap, "id") == c.FormatID.ID {
									match = true
									break
								}
							}
						}
						if !match {
							continue
						}
					}
				}
				items = append(items, adcp.CreativeListItem{
					CreativeID: c.CreativeID, Name: c.Name, FormatID: c.FormatID, Status: c.Status,
				})
			}
			return makeResult(map[string]any{"creatives": items}, fmt.Sprintf("Found %d creatives", len(items)))
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "preview_creative", Description: "Render a preview of a creative",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := parseArgs(req)
			creativeID := stringVal(args, "creative_id")

			s.mu.RLock()
			c, ok := s.creatives[creativeID]
			s.mu.RUnlock()

			if !ok {
				return makeErrorResult("CREATIVE_NOT_FOUND", fmt.Sprintf("Creative %s not found", creativeID))
			}

			var dims *adcp.Render
			for _, f := range formats {
				if f.FormatID.ID == c.FormatID.ID {
					if len(f.Renders) > 0 {
						dims = &f.Renders[0]
					}
					break
				}
			}

			result := &adcp.PreviewResult{
				ResponseType: "single",
				Previews: []adcp.Preview{{
					PreviewID: fmt.Sprintf("preview-%s", c.CreativeID),
					Input:     map[string]any{"format_id": c.FormatID, "name": c.Name, "assets": c.Assets},
					Renders: []adcp.PreviewRender{{
						RenderID: fmt.Sprintf("render-%s", c.CreativeID), OutputFormat: "url",
						PreviewURL: fmt.Sprintf("https://preview.example.com/%s", c.CreativeID),
						Role: "primary", Dimensions: dims,
					}},
				}},
				ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			}
			return makeResult(result, "Preview generated")
		})

		server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
			Name: "build_creative", Description: "Build a serving tag from a creative",
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := parseArgs(req)
			creativeID := stringVal(args, "creative_id")

			s.mu.RLock()
			c, ok := s.creatives[creativeID]
			s.mu.RUnlock()

			if !ok {
				return makeErrorResult("CREATIVE_NOT_FOUND", fmt.Sprintf("Creative %s not found", creativeID))
			}

			return makeResult(map[string]any{
				"creative_manifest": map[string]any{"format_id": c.FormatID, "name": c.Name, "assets": c.Assets},
				"sandbox":          true,
			}, "Creative built")
		})

		return server
	}

	log.Fatal(adcp.Serve(createServer))
}
