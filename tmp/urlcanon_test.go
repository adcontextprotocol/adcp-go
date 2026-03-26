package tmp

import "testing"

func TestCanonicalizeURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://www.oakwood.example.com/2026/03/kitchen-trends", "oakwood.example.com/2026/03/kitchen-trends"},
		{"http://m.oakwood.example.com/2026/03/kitchen-trends", "oakwood.example.com/2026/03/kitchen-trends"},
		{"https://oakwood.example.com/2026/03/kitchen-trends/", "oakwood.example.com/2026/03/kitchen-trends"},
		{"https://WWW.Oakwood.Example.COM/Path", "oakwood.example.com/path"},
		{"https://amp.oakwood.example.com/article?utm_source=twitter#top", "oakwood.example.com/article"},
		{"oakwood.example.com/page", "oakwood.example.com/page"},
		{"https://example.com", "example.com"},
		{"https://example.com/", "example.com"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CanonicalizeURL(tt.input)
			if got != tt.expected {
				t.Errorf("CanonicalizeURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestHashURL_SameURLsDifferentForms(t *testing.T) {
	urls := []string{
		"https://www.oakwood.example.com/2026/03/kitchen-trends",
		"http://m.oakwood.example.com/2026/03/kitchen-trends",
		"https://oakwood.example.com/2026/03/kitchen-trends/",
		"https://WWW.OAKWOOD.EXAMPLE.COM/2026/03/kitchen-trends",
	}
	hash0 := HashURL(urls[0])
	for _, u := range urls[1:] {
		if HashURL(u) != hash0 {
			t.Errorf("HashURL(%q) != HashURL(%q)", u, urls[0])
		}
	}
}

func TestHashURL_DifferentURLs(t *testing.T) {
	h1 := HashURL("https://oakwood.example.com/page-a")
	h2 := HashURL("https://oakwood.example.com/page-b")
	if h1 == h2 {
		t.Error("different URLs should produce different hashes")
	}
}
