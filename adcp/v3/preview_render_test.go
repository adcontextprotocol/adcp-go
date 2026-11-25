package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// PreviewRender (creative/preview-render.json) once tagged the markup "html",
// which the schema does not have (preview_html), and dropped embedding and
// renderer (adcontextprotocol/adcp-go#611). Each fixture is valid against the
// bundled schema and sets every property it names, so a field that stops being
// encoded or decoded fails the round trip.

const previewRenderSchema = "creative/preview-render.json"

const previewEmbeddingJSON = `{
	"recommended_sandbox":"",
	"requires_https":true,
	"supports_fullscreen":false,
	"csp_policy":"default-src 'none'"
}`

const previewRendererJSON = `{
	"renderer_id":"nova-preview",
	"version":"1.4.0",
	"export":"renderBanner",
	"rendering_origin":"agent_approximation",
	"tracking_suppressed":false
}`

func TestPreviewRenderRoundTrip(t *testing.T) {
	t.Run("url", func(t *testing.T) {
		r := requireRoundTrip[PreviewRender](t, previewRenderSchema, `{
			"render_id":"r1",
			"output_format":"url",
			"preview_url":"https://preview.nova-athletics.example/r1",
			"role":"primary",
			"dimensions":{"width":300,"height":250},
			"embedding":`+previewEmbeddingJSON+`,
			"renderer":`+previewRendererJSON+`
		}`)
		require.Equal(t, "https://preview.nova-athletics.example/r1", r.PreviewURL)
		require.Empty(t, r.PreviewHTML)
	})

	t.Run("html", func(t *testing.T) {
		r := requireRoundTrip[PreviewRender](t, previewRenderSchema, `{
			"render_id":"r2",
			"output_format":"html",
			"preview_html":"<p>x</p>",
			"role":"companion",
			"dimensions":{"width":728,"height":90},
			"embedding":`+previewEmbeddingJSON+`,
			"renderer":`+previewRendererJSON+`
		}`)
		require.Equal(t, "<p>x</p>", r.PreviewHTML)
		require.Empty(t, r.PreviewURL)
	})

	t.Run("both", func(t *testing.T) {
		r := requireRoundTrip[PreviewRender](t, previewRenderSchema, `{
			"render_id":"r3",
			"output_format":"both",
			"preview_url":"https://preview.nova-athletics.example/r3",
			"preview_html":"<p>y</p>",
			"role":"primary"
		}`)
		require.Equal(t, "https://preview.nova-athletics.example/r3", r.PreviewURL)
		require.Equal(t, "<p>y</p>", r.PreviewHTML)
	})
}

func TestPreviewRenderDecodesEmbeddingAndRenderer(t *testing.T) {
	r := requireRoundTrip[PreviewRender](t, previewRenderSchema, `{
		"render_id":"r1",
		"output_format":"html",
		"preview_html":"<p>x</p>",
		"role":"primary",
		"embedding":`+previewEmbeddingJSON+`,
		"renderer":`+previewRendererJSON+`
	}`)

	require.NotNil(t, r.Embedding)
	require.NotNil(t, r.Embedding.RecommendedSandbox, "the only allowed value is the empty string, which must survive")
	require.Empty(t, *r.Embedding.RecommendedSandbox)
	require.NotNil(t, r.Embedding.RequiresHTTPS)
	require.True(t, *r.Embedding.RequiresHTTPS)
	require.NotNil(t, r.Embedding.SupportsFullscreen)
	require.False(t, *r.Embedding.SupportsFullscreen, "an explicit false must survive")
	require.Equal(t, "default-src 'none'", r.Embedding.CSPPolicy)

	require.Equal(t, PreviewRendererMetadata{
		RendererID:         "nova-preview",
		Version:            "1.4.0",
		Export:             "renderBanner",
		RenderingOrigin:    "agent_approximation",
		TrackingSuppressed: false,
	}, *r.Renderer)
}

func TestPreviewRenderAbsentMetadataStaysAbsent(t *testing.T) {
	r := requireRoundTrip[PreviewRender](t, previewRenderSchema,
		`{"render_id":"r1","output_format":"url","preview_url":"https://preview.nova-athletics.example/r1","role":"primary"}`)
	require.Nil(t, r.Embedding)
	require.Nil(t, r.Renderer)
	require.Nil(t, r.Dimensions)
}

// The deprecated HTML field is never a wire key. A render built with it encodes
// the markup as preview_html, so the output validates against the schema.
func TestPreviewRenderDeprecatedHTMLEncodesAsPreviewHTML(t *testing.T) {
	render := PreviewRender{RenderID: "r1", OutputFormat: "html", HTML: "<p>x</p>", Role: "primary"}
	raw, err := json.Marshal(render)
	require.NoError(t, err)
	assertSchemaValid(t, previewRenderSchema, raw)
	require.JSONEq(t, `{"render_id":"r1","output_format":"html","preview_html":"<p>x</p>","role":"primary"}`, string(raw))

	var onWire map[string]any
	require.NoError(t, json.Unmarshal(raw, &onWire))
	require.NotContains(t, onWire, "html")

	t.Run("PreviewHTML wins when both are set", func(t *testing.T) {
		both := PreviewRender{RenderID: "r1", OutputFormat: "html", PreviewHTML: "<p>new</p>", HTML: "<p>old</p>", Role: "primary"}
		raw, err := json.Marshal(both)
		require.NoError(t, err)
		assertSchemaValid(t, previewRenderSchema, raw)
		require.JSONEq(t, `{"render_id":"r1","output_format":"html","preview_html":"<p>new</p>","role":"primary"}`, string(raw))
	})

	t.Run("a url render with no markup has neither key", func(t *testing.T) {
		raw, err := json.Marshal(PreviewRender{RenderID: "r1", OutputFormat: "url", PreviewURL: "https://preview.nova-athletics.example/r1", Role: "primary"})
		require.NoError(t, err)
		assertSchemaValid(t, previewRenderSchema, raw)
		require.NotContains(t, string(raw), "html")
	})

	t.Run("through a pointer and a slice", func(t *testing.T) {
		raw, err := json.Marshal(Preview{
			PreviewID: "p1",
			Input:     map[string]any{},
			Renders:   []PreviewRender{render},
		})
		require.NoError(t, err)
		var decoded struct {
			Renders []map[string]any `json:"renders"`
		}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.Len(t, decoded.Renders, 1)
		require.NotContains(t, decoded.Renders[0], "html")
		require.Equal(t, "<p>x</p>", decoded.Renders[0]["preview_html"])

		ptr, err := json.Marshal(&render)
		require.NoError(t, err)
		assertSchemaValid(t, previewRenderSchema, ptr)
	})
}

// Decoding fills the deprecated HTML field from preview_html, so a reader of
// either field sees the markup. A legacy "html" key is not a property of the
// schema and is ignored.
func TestPreviewRenderDecodeFillsDeprecatedHTML(t *testing.T) {
	var r PreviewRender
	require.NoError(t, json.Unmarshal([]byte(
		`{"render_id":"r1","output_format":"html","preview_html":"<p>x</p>","role":"primary"}`), &r))
	require.Equal(t, "<p>x</p>", r.PreviewHTML)
	require.Equal(t, "<p>x</p>", r.HTML)

	var legacy PreviewRender
	require.NoError(t, json.Unmarshal([]byte(
		`{"render_id":"r1","output_format":"html","html":"<p>x</p>","role":"primary"}`), &legacy))
	require.Empty(t, legacy.PreviewHTML)
	require.Empty(t, legacy.HTML)
}

func TestPreviewRenderRejectsMalformedJSON(t *testing.T) {
	var r PreviewRender
	require.Error(t, json.Unmarshal([]byte(`{"render_id":1}`), &r))
}

// AdCP 3.3 opens every variant, the embedding object and the renderer metadata
// (additionalProperties: true), so a seller may send keys this SDK does not
// know. The types are plain structs that ignore unknown keys on decode: the
// decode succeeds and every known field is kept. The payload is not run through
// assertSchemaValid because it is written against the 3.3 rc.3 shape.
func TestPreviewRenderIgnoresUnknownKeys(t *testing.T) {
	var r PreviewRender
	require.NoError(t, json.Unmarshal([]byte(`{
		"render_id":"r1",
		"output_format":"both",
		"preview_url":"https://preview.nova-athletics.example/r1",
		"preview_html":"<p>x</p>",
		"role":"primary",
		"x_render_future":{"a":1},
		"dimensions":{"width":300,"height":250},
		"embedding":{"requires_https":true,"x_embedding_future":"e"},
		"renderer":{
			"renderer_id":"nova-preview",
			"version":"1.4.0",
			"export":"renderBanner",
			"rendering_origin":"platform_native",
			"tracking_suppressed":true,
			"x_renderer_future":["a"]
		}
	}`), &r))

	require.Equal(t, "both", r.OutputFormat)
	require.Equal(t, "https://preview.nova-athletics.example/r1", r.PreviewURL)
	require.Equal(t, "<p>x</p>", r.PreviewHTML)
	require.Equal(t, 300, r.Dimensions.Width)
	require.True(t, *r.Embedding.RequiresHTTPS)
	require.Equal(t, "renderBanner", r.Renderer.Export)
	require.True(t, r.Renderer.TrackingSuppressed)
}
