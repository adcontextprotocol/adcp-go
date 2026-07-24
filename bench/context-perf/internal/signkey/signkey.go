// Package signkey loads and persists the Ed25519 signing keypair shared by
// the bench loadgen (which signs requests) and the mock tmpregistry (which
// publishes the corresponding public JWK on the snapshot endpoint).
//
// The file lives on a shared volume so both containers converge on the same
// kid + public key without a bootstrap protocol.
//
// Ownership convention: tmpregistry is the sole caller of LoadOrGenerate —
// it may write. loadgen calls WaitFor (read-only) and blocks until the file
// appears. This asymmetry is the reason there is no cross-process race on
// key generation; do not switch loadgen to LoadOrGenerate.
//
// A byte-identical copy lives at
// bench/identity-perf/internal/signkey; changes here must land there too
// (deferred deduplication into a shared bench module — see AI-4641).
package signkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// File is the on-disk shape at Path. private_key is base64-std-encoded raw
// ed25519 private key material (64 bytes).
type File struct {
	Kid           string `json:"kid"`
	PrivateKeyB64 string `json:"private_key"`
	IssuedAtUnix  int64  `json:"iat"`
}

// KeyPair is the parsed form callers use at runtime.
type KeyPair struct {
	Kid        string
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
	IssuedAt   time.Time
}

// LoadOrGenerate reads path if present; otherwise it generates a fresh
// Ed25519 keypair under kid and writes it atomically via os.Rename. Only
// tmpregistry should call this — see the package doc for the ownership
// convention that keeps generation single-writer.
func LoadOrGenerate(path, kid string) (*KeyPair, error) {
	if kid == "" {
		return nil, errors.New("signkey: kid must not be empty")
	}
	if kp, err := loadFile(path); err == nil {
		return kp, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("signkey: mkdir %s: %w", filepath.Dir(path), err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("signkey: generate: %w", err)
	}
	now := time.Now().UTC()
	f := File{
		Kid:           kid,
		PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		IssuedAtUnix:  now.Unix(),
	}
	buf, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("signkey: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return nil, fmt.Errorf("signkey: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("signkey: rename %s: %w", path, err)
	}
	return &KeyPair{Kid: kid, PrivateKey: priv, PublicKey: pub, IssuedAt: now}, nil
}

// Load reads a previously-persisted keypair. Returns os.ErrNotExist wrapped
// when the file is missing, so callers can decide whether to wait or fail.
func Load(path string) (*KeyPair, error) {
	return loadFile(path)
}

// WaitFor polls Load until it succeeds or the deadline elapses. Used by the
// loadgen when the tmpregistry starts up first and writes the file.
func WaitFor(path string, timeout, interval time.Duration) (*KeyPair, error) {
	deadline := time.Now().Add(timeout)
	for {
		kp, err := Load(path)
		if err == nil {
			return kp, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("signkey: %s not present after %s", path, timeout)
		}
		time.Sleep(interval)
	}
}

func loadFile(path string) (*KeyPair, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("signkey: parse %s: %w", path, err)
	}
	if f.Kid == "" {
		return nil, fmt.Errorf("signkey: %s missing kid", path)
	}
	raw, err := base64.StdEncoding.DecodeString(f.PrivateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("signkey: decode private_key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signkey: private_key has %d bytes, expected %d", len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	pub := priv.Public().(ed25519.PublicKey)
	issued := time.Unix(f.IssuedAtUnix, 0).UTC()
	return &KeyPair{Kid: f.Kid, PrivateKey: priv, PublicKey: pub, IssuedAt: issued}, nil
}
