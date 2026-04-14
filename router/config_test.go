package router

import "testing"

func TestValidateProviderEndpoint(t *testing.T) {
	valid := []struct {
		name     string
		endpoint string
	}{
		{"public HTTPS", "https://provider.example.com/tmp"},
		{"public HTTP", "http://provider.example.com"},
		{"public IP", "https://203.0.113.1/api"},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateProviderEndpoint(tt.endpoint); err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
		})
	}

	invalid := []struct {
		name     string
		endpoint string
	}{
		{"localhost by name", "http://localhost:8081"},
		{"localhost uppercase", "http://LOCALHOST:8081"},
		{"loopback IPv4", "http://127.0.0.1:9000"},
		{"loopback IPv6", "http://[::1]:9000"},
		{"IPv4-mapped IPv6 private", "http://[::ffff:192.168.1.1]/api"},
		{"IPv4-mapped IPv6 loopback", "http://[::ffff:127.0.0.1]/api"},
		{"RFC-1918 10.x", "http://10.0.0.1/api"},
		{"RFC-1918 172.16.x", "http://172.16.5.4/api"},
		{"RFC-1918 192.168.x", "http://192.168.1.1/api"},
		{"link-local", "http://169.254.169.254/latest/meta-data/"},
		{"file scheme", "file:///etc/passwd"},
		{"ftp scheme", "ftp://internal-host/"},
		{"no scheme", "/relative/path"},
		{"empty", ""},
		{"no host", "http://"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateProviderEndpoint(tt.endpoint); err == nil {
				t.Errorf("expected error for %q, got nil", tt.endpoint)
			}
		})
	}
}
