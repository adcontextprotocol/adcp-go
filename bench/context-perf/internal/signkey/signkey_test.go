package signkey

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrGenerate_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "signer.json")

	first, err := LoadOrGenerate(path, "test-kid")
	if err != nil {
		t.Fatalf("first LoadOrGenerate: %v", err)
	}
	if first.Kid != "test-kid" {
		t.Fatalf("kid = %q, want test-kid", first.Kid)
	}
	if len(first.PrivateKey) == 0 {
		t.Fatal("PrivateKey empty")
	}
	if len(first.PublicKey) == 0 {
		t.Fatal("PublicKey empty")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist after generate: %v", path, err)
	}

	second, err := LoadOrGenerate(path, "test-kid")
	if err != nil {
		t.Fatalf("second LoadOrGenerate: %v", err)
	}
	if second.Kid != first.Kid {
		t.Errorf("kid changed on reload: %q vs %q", second.Kid, first.Kid)
	}
	for i := range first.PrivateKey {
		if first.PrivateKey[i] != second.PrivateKey[i] {
			t.Fatalf("PrivateKey byte %d changed on reload", i)
		}
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.json")
	if _, err := Load(path); !os.IsNotExist(err) {
		t.Fatalf("Load missing file err = %v, want os.IsNotExist", err)
	}
}
