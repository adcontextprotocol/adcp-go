package psl

import "testing"

func TestRegistrableDomain(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"buyer.example.com", "example.com", true},
		{"https://ads.brand.co.uk/mcp", "brand.co.uk", true},
		{"https://Buyer.Example.COM.:443/x", "example.com", true},
		{"example.com", "example.com", true},
		// PRIVATE section: tenants of a platform do not share an eTLD+1.
		{"attacker.github.io", "attacker.github.io", true},
		{"victim.vercel.app", "victim.vercel.app", true},
		{"a.b.blogspot.com", "b.blogspot.com", true},
		// No registrable domain.
		{"co.uk", "", false},
		{"github.io", "", false},
		{"com", "", false},
		{"localhost", "", false},
		{"intranet", "", false},
		{"192.0.2.1", "", false},
		{"https://[2001:db8::1]/x", "", false},
		{"buyer.example", "", false},     // unlisted TLD
		{"buyer.notarealtld", "", false}, // unlisted TLD
		{"under_score.example.com", "", false},
		{"-lead.example.com", "", false},
		{"a..example.com", "", false},
		{"bücher.example.com", "", false}, // U-label: callers pass A-labels
		{"xn--bcher-kva.example.com", "example.com", true},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := RegistrableDomain(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("RegistrableDomain(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestQuotaKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://agent.brand.example.com": "example.com",
		"https://a.tenant1.github.io":     "github.io",
		"https://tenant2.github.io":       "github.io",
		"https://x.brand.co.uk":           "brand.co.uk",
	} {
		got, ok := QuotaKey(in)
		if !ok || got != want {
			t.Errorf("QuotaKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := QuotaKey("https://192.0.2.1"); ok {
		t.Error("IP literal has a quota key")
	}
}
