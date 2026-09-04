package tmproto

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFetchableURL_Valid(t *testing.T) {
	valid := []string{
		"https://example.com/article/42",
		"http://example.com/article/42", // http allowed — matches the AdCP webhook rules (scheme allowlist is http+https)
		"https://cdn.example.com/pasta.jpg?w=800",
		"https://example.com:8443/path",
		"https://93.184.216.34/path",                    // public IPv4 literal
		"https://[2606:2800:220:1:248:1893:25c8:1946]/", // public IPv6 literal
		"https://100.63.255.255/",                       // just below CGNAT range
		"https://100.128.0.0/",                          // just above CGNAT range
		"https://198.17.255.255/",                       // just below benchmark range
		"https://198.20.0.0/",                           // just above benchmark range
	}
	for _, raw := range valid {
		t.Run(raw, func(t *testing.T) {
			assert.NoError(t, ValidateFetchableURL(raw), "expected %q to be accepted", raw)
		})
	}
}

func TestValidateFetchableURL_RejectsBadSchemes(t *testing.T) {
	cases := []string{
		"file:///etc/passwd",
		"ftp://example.com/file",
		"javascript:alert(1)",
		"data:text/plain;base64,aGVsbG8=",
		"gopher://example.com/",
		"",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			err := ValidateFetchableURL(raw)
			require.Error(t, err, "expected %q to be rejected", raw)
		})
	}
}

func TestValidateFetchableURL_RejectsCredentials(t *testing.T) {
	cases := []string{
		"https://user:pass@example.com/",
		"https://token@example.com/",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			err := ValidateFetchableURL(raw)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnsafeURL))
			assert.Contains(t, err.Error(), "userinfo")
		})
	}
}

// TestValidateFetchableURL_RejectsPrivateAndReservedIPv4 exercises the IPv4
// range list, with CGNAT (100.64.0.0/10) called out explicitly: the sibling
// TypeScript SSRF validator (adcontextprotocol/adcp server/src/utils/url-security.ts)
// was found missing this exact range in adcontextprotocol/adcp#7091. This
// suite pins it here so the same class of gap can't reopen in this port.
func TestValidateFetchableURL_RejectsPrivateAndReservedIPv4(t *testing.T) {
	cases := map[string]string{
		"loopback":               "https://127.0.0.1/",
		"loopback high":          "https://127.255.255.255/",
		"this-network":           "https://0.0.0.0/",
		"rfc1918 10/8":           "https://10.1.2.3/",
		"rfc1918 172.16/12 low":  "https://172.16.0.1/",
		"rfc1918 172.16/12 high": "https://172.31.255.255/",
		"rfc1918 192.168/16":     "https://192.168.1.1/",
		"link-local":             "https://169.254.1.1/",
		"cloud metadata":         "https://169.254.169.254/latest/meta-data/",
		"cgnat low":              "https://100.64.0.0/",
		"cgnat mid":              "https://100.100.100.100/",
		"cgnat high":             "https://100.127.255.255/",
		"benchmark":              "https://198.18.0.1/",
		"unspecified":            "https://0.0.0.0/",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateFetchableURL(raw)
			require.Error(t, err, "expected %q to be rejected", raw)
			assert.True(t, errors.Is(err, ErrUnsafeURL), "expected ErrUnsafeURL for %q, got %v", raw, err)
		})
	}
}

func TestValidateFetchableURL_RejectsPrivateAndReservedIPv6(t *testing.T) {
	cases := map[string]string{
		"loopback":              "https://[::1]/",
		"unspecified":           "https://[::]/",
		"link-local":            "https://[fe80::1]/",
		"unique-local fc00":     "https://[fc00::1]/",
		"unique-local fd":       "https://[fd12:3456:789a::1]/",
		"deprecated site-local": "https://[fec0::1]/",
		"6to4 embeds private":   "https://[2002:0a00:0001::]/", // embeds 10.0.0.1
		"nat64 embeds private":  "https://[64:ff9b::a00:1]/",   // embeds 10.0.0.1
		"loopback zone-id":      "http://[::1%25lo0]/",
		"link-local zone-id":    "http://[fe80::1%25eth0]/",
		"ula zone-id":           "http://[fd00::1%25eth0]/",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateFetchableURL(raw)
			require.Error(t, err, "expected %q to be rejected", raw)
			assert.True(t, errors.Is(err, ErrUnsafeURL), "expected ErrUnsafeURL for %q, got %v", raw, err)
		})
	}
}

func TestValidateFetchableURL_AllowsPublicTunneledIPv6(t *testing.T) {
	// 6to4/NAT64 forms that embed a PUBLIC v4 address must not be rejected —
	// only the embedded-private case is disallowed.
	cases := map[string]string{
		"6to4 embeds public":  "https://[2002:0808:0808::]/", // embeds 8.8.8.8
		"nat64 embeds public": "https://[64:ff9b::808:808]/", // embeds 8.8.8.8
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, ValidateFetchableURL(raw), "expected %q to be accepted", raw)
		})
	}
}

func TestValidateFetchableURL_RejectsInternalHostnames(t *testing.T) {
	cases := []string{
		"https://localhost/",
		"https://localhost:8080/",
		"https://foo.localhost/",
		"https://metadata.google.internal/",
		"https://service.internal/",
		"https://printer.local/",
		"https://example..com/", // empty label
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			err := ValidateFetchableURL(raw)
			require.Error(t, err, "expected %q to be rejected", raw)
		})
	}
}

func TestValidateFetchableURL_RejectsOversizedURL(t *testing.T) {
	raw := "https://example.com/" + strings.Repeat("a", MaxContentURLLength)
	err := ValidateFetchableURL(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum length")
}

func TestValidateFetchableURL_RejectsMalformed(t *testing.T) {
	err := ValidateFetchableURL("ht!tp://[::1")
	require.Error(t, err)
}

func TestValidateFetchableURL_ErrorsAreGeneric(t *testing.T) {
	// Error text must not echo the raw URL back — consistent with the rest
	// of this package's Validate() error messages (see validateSafeID,
	// validateSellerAgentURL): describe the rule violated, not the value.
	sentinelHost := "169.254.169.254"
	raw := "https://" + sentinelHost + "/latest/meta-data/iam/security-credentials/"
	err := ValidateFetchableURL(raw)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), sentinelHost)
	assert.NotContains(t, err.Error(), "security-credentials")
}

func TestArtifactRef_Validate_URLType(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{"valid canonical url", "https://example.com/article/42", ""},
		{"query rejected", "https://example.com/article/42?user=alice", "query string"},
		{"fragment rejected", "https://example.com/article/42#section-2", "fragment"},
		{"userinfo rejected", "https://user:pass@example.com/article/42", "userinfo"},
		{"private ip rejected", "https://169.254.169.254/article/42", "disallowed"},
		{"non-http scheme rejected", "file:///etc/passwd", "scheme"},
		{"localhost rejected", "https://localhost/article/42", "localhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := ArtifactRef{Type: ArtifactRefTypeURL, Value: tc.value}
			err := ref.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestArtifactRef_Validate_NonURLTypesUnaffected pins that the new URL
// checks only apply when Type == ArtifactRefTypeURL — every other ref type
// (including url_hash, whose value is a base64 digest, not a URL) must be
// unaffected.
func TestArtifactRef_Validate_NonURLTypesUnaffected(t *testing.T) {
	cases := []ArtifactRef{
		{Type: ArtifactRefTypeURLHash, Value: "bXlfaGFzaA=="},
		{Type: ArtifactRefTypeEIDR, Value: "10.5240/7791-8534-2C23-9030-8107-4"},
		{Type: ArtifactRefTypeCustom, Value: "not a url at all: has? a #fragment-lookalike"},
	}
	for _, ref := range cases {
		t.Run(string(ref.Type), func(t *testing.T) {
			assert.NoError(t, ref.Validate())
		})
	}
}

func TestArtifact_Validate_URLField(t *testing.T) {
	base := func() *Artifact {
		return &Artifact{
			PropertyRID: "p",
			ArtifactID:  "a",
			Assets:      Assets{&TextAsset{Content: "hi"}},
		}
	}

	t.Run("empty URL is fine (optional field)", func(t *testing.T) {
		art := base()
		assert.NoError(t, art.Validate())
	})

	t.Run("valid URL passes", func(t *testing.T) {
		art := base()
		art.URL = "https://example.com/article/42"
		assert.NoError(t, art.Validate())
	})

	t.Run("SSRF-unsafe URL rejected", func(t *testing.T) {
		art := base()
		art.URL = "http://169.254.169.254/latest/meta-data/"
		err := art.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "artifact.url")
	})

	t.Run("file scheme rejected", func(t *testing.T) {
		art := base()
		art.URL = "file:///etc/passwd"
		require.Error(t, art.Validate())
	})
}

func TestImageAsset_Validate_SSRF(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"valid", "https://cdn.example.com/pic.jpg", ""},
		{"loopback", "http://127.0.0.1/pic.jpg", "disallowed"},
		{"cgnat", "http://100.64.1.1/pic.jpg", "disallowed"},
		{"file scheme", "file:///etc/passwd", "scheme"},
		{"credentials", "https://user:pass@cdn.example.com/pic.jpg", "userinfo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &ImageAsset{URL: tc.url}
			err := a.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "image_asset.url")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestVideoAsset_Validate_SSRF(t *testing.T) {
	t.Run("valid url and thumbnail", func(t *testing.T) {
		a := &VideoAsset{URL: "https://cdn.example.com/v.mp4", ThumbnailURL: "https://cdn.example.com/thumb.jpg"}
		assert.NoError(t, a.Validate())
	})
	t.Run("bad primary url", func(t *testing.T) {
		a := &VideoAsset{URL: "http://169.254.169.254/v.mp4"}
		err := a.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "video_asset.url")
	})
	t.Run("bad thumbnail url", func(t *testing.T) {
		a := &VideoAsset{URL: "https://cdn.example.com/v.mp4", ThumbnailURL: "http://10.0.0.1/thumb.jpg"}
		err := a.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "video_asset.thumbnail_url")
	})
	t.Run("empty thumbnail is fine (optional field)", func(t *testing.T) {
		a := &VideoAsset{URL: "https://cdn.example.com/v.mp4"}
		assert.NoError(t, a.Validate())
	})
}

func TestAudioAsset_Validate_SSRF(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		a := &AudioAsset{URL: "https://cdn.example.com/a.mp3"}
		assert.NoError(t, a.Validate())
	})
	t.Run("private ip rejected", func(t *testing.T) {
		a := &AudioAsset{URL: "http://192.168.1.1/a.mp3"}
		err := a.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "audio_asset.url")
	})
}

// TestArtifact_Validate_RecursesIntoAssetURLs ensures a malicious asset URL
// buried inside Artifact.Assets is caught by the top-level Validate() call a
// handler would actually make (ValidateContextRequest -> Artifact.Validate()
// -> per-asset Validate()), not just by calling the asset's Validate()
// directly.
func TestArtifact_Validate_RecursesIntoAssetURLs(t *testing.T) {
	art := &Artifact{
		PropertyRID: "p",
		ArtifactID:  "a",
		Assets: Assets{
			&TextAsset{Content: "hi"},
			&ImageAsset{URL: "http://169.254.169.254/steal-creds.jpg"},
		},
	}
	err := art.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact.assets[1]")
	assert.Contains(t, err.Error(), "image_asset.url")
}
