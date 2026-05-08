package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestLoadTmpxConfigDisabled(t *testing.T) {
	cfg, err := loadTmpxConfig("", "", "")
	if err != nil || cfg != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", cfg, err)
	}
}

func TestLoadTmpxConfigPartialFails(t *testing.T) {
	cases := []struct{ kid, path, country string }{
		{"k1", "", "US"},
		{"k1", "/tmp/x", ""},
		{"", "/tmp/x", "US"},
	}
	for _, c := range cases {
		_, err := loadTmpxConfig(c.kid, c.path, c.country)
		if err == nil {
			t.Errorf("partial config %+v should fail", c)
		}
	}
}

func TestLoadTmpxConfigHexAndBase64(t *testing.T) {
	skR, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubBytes := skR.PublicKey().Bytes()

	dir := t.TempDir()
	for _, enc := range []struct{ name, content string }{
		{"hex.key", hex.EncodeToString(pubBytes)},
		{"b64url.key", base64.RawURLEncoding.EncodeToString(pubBytes)},
		{"b64std.key", base64.StdEncoding.EncodeToString(pubBytes)},
		{"hex_with_ws.key", "  " + hex.EncodeToString(pubBytes) + "\n"},
	} {
		path := filepath.Join(dir, enc.name)
		if err := os.WriteFile(path, []byte(enc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadTmpxConfig("k1", path, "US")
		if err != nil {
			t.Errorf("%s: %v", enc.name, err)
			continue
		}
		if cfg == nil || cfg.Kid != "k1" || cfg.Country != "US" || cfg.PublicKey == nil {
			t.Errorf("%s: unexpected config %+v", enc.name, cfg)
		}
	}
}

func TestBuildTmpxTokenRoundtrip(t *testing.T) {
	skR, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tmpxConfig{Kid: "k1", Country: "US", PublicKey: skR.PublicKey()}

	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeUID2, UserToken: "uid2-token-source-string"},
		{UIDType: tmproto.UIDTypeMAID, UserToken: "maid-source-string"},
		{UIDType: tmproto.UIDTypeOther, UserToken: "skipped-no-mapping"},
	}
	wire, err := buildTmpxToken(cfg, ids)
	if err != nil {
		t.Fatalf("buildTmpxToken: %v", err)
	}
	kid, payload, ok := strings.Cut(wire, ".")
	if !ok || kid != "k1" {
		t.Fatalf("wire format: %q", wire)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(raw) <= 32+16 {
		t.Fatalf("payload suspiciously short (%d bytes)", len(raw))
	}
	// We don't roundtrip-decrypt here — that's covered by TestSealTmpxRoundtrip
	// in the tmproto package. We just want to confirm the recipient kid and
	// envelope are well-formed and non-empty when there's at least one
	// mappable identity.
}

func TestBuildTmpxTokenEmptyWhenNoMappableIdentities(t *testing.T) {
	skR, _ := ecdh.X25519().GenerateKey(rand.Reader)
	cfg := &tmpxConfig{Kid: "k1", Country: "US", PublicKey: skR.PublicKey()}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeOther, UserToken: "x"},
	}
	wire, err := buildTmpxToken(cfg, ids)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if wire != "" {
		t.Errorf("expected empty wire when no mappable identities, got %q", wire)
	}
}

func TestStubBinaryTokenSizes(t *testing.T) {
	cases := []struct {
		typeID tmproto.TmpxTypeID
		want   int
	}{
		{tmproto.TmpxTypeUID2, 32},
		{tmproto.TmpxTypeMAID, 16},
		{tmproto.TmpxTypeRampIDDerived, 48},
	}
	for _, c := range cases {
		bin, err := stubBinaryToken(c.typeID, "any-input-string")
		if err != nil {
			t.Errorf("type %d: %v", c.typeID, err)
			continue
		}
		if len(bin) != c.want {
			t.Errorf("type %d: got %d bytes, want %d", c.typeID, len(bin), c.want)
		}
	}
}

func TestStubBinaryTokenDeterministic(t *testing.T) {
	a, _ := stubBinaryToken(tmproto.TmpxTypeUID2, "same-input")
	b, _ := stubBinaryToken(tmproto.TmpxTypeUID2, "same-input")
	if !bytes.Equal(a, b) {
		t.Fatal("stub must be deterministic for same input")
	}
}

func TestBuildTmpxTokenFreshNonceEachCall(t *testing.T) {
	skR, _ := ecdh.X25519().GenerateKey(rand.Reader)
	cfg := &tmpxConfig{Kid: "k1", Country: "US", PublicKey: skR.PublicKey()}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeUID2, UserToken: "tok"},
	}
	a, _ := buildTmpxToken(cfg, ids)
	// The HPKE encapsulated key is fresh per call; differing wire output
	// confirms ephemeral key generation. This is the closest behavioural
	// proof of replay protection without buyer-master-side decryption here.
	time.Sleep(time.Millisecond)
	b, _ := buildTmpxToken(cfg, ids)
	if a == b {
		t.Fatal("two seal calls must produce distinct wire output")
	}
}
