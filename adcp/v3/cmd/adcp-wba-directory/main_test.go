package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

func TestRunWritesAVerifiableDirectory(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, alg := range []signing.Algorithm{signing.AlgEd25519, signing.AlgEd25519, signing.AlgES256} {
		res, err := signing.GenerateSigningKey(alg, "")
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "key"+string(rune('a'+i))+".pem")
		if err := os.WriteFile(p, res.PrivateKeyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	now := time.Unix(1_790_841_600, 0)
	body, headers := filepath.Join(dir, "directory.json"), filepath.Join(dir, "directory.headers")
	var stdout, stderr bytes.Buffer
	args := []string{"--origin", "https://buyer.example.com", "--key", paths[0], "--key", paths[1], "--lifetime", "2h", "--out-body", body, "--out-headers", headers}
	if err := run(args, &stdout, &stderr, func() time.Time { return now }); err != nil {
		t.Fatalf("run: %v (%s)", err, stderr.String())
	}
	b, _ := os.ReadFile(body)    // #nosec G304 -- paths created by this test
	h, _ := os.ReadFile(headers) // #nosec G304 -- paths created by this test
	resp := &signing.DirectoryResponse{Status: http.StatusOK, Header: http.Header{}, Body: b}
	for _, line := range strings.Split(strings.TrimSpace(string(h)), "\n") {
		name, value, _ := strings.Cut(line, ": ")
		resp.Header.Add(name, value)
	}
	keys, err := signing.VerifyDirectoryResponse("https://buyer.example.com", resp, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys", len(keys))
	}
	if _, err := signing.VerifyDirectoryResponse("https://buyer.example.com", resp, now.Add(3*time.Hour)); err == nil {
		t.Fatal("expired directory accepted")
	}

	stdout.Reset()
	if err := run([]string{"--thumbprint", paths[0]}, &stdout, &stderr, time.Now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), strings.TrimSpace(stdout.String())) {
		t.Fatalf("thumbprint %q not among published keys: %s", stdout.String(), stderr.String())
	}
	if err := run([]string{"--origin", "https://buyer.example.com"}, &stdout, &stderr, time.Now); err == nil {
		t.Fatal("missing flags accepted")
	}
	// ES256 is not a Web Bot Auth algorithm: a P-256 key is refused.
	args = []string{"--origin", "https://buyer.example.com", "--key", paths[2], "--out-body", body, "--out-headers", headers}
	if err := run(args, &stdout, &stderr, time.Now); err == nil {
		t.Fatal("P-256 key accepted")
	}
}
