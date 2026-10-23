// Command adcp-wba-directory builds the signed key directory an AdCP 3.3
// Web Bot Auth agent serves at /.well-known/http-message-signatures-directory
// (EXPERIMENTAL).
//
// Usage:
//
//	adcp-wba-directory --origin https://buyer.example.com --key key.pem [--key next.pem] \
//	    [--lifetime 24h] --out-body directory.json --out-headers directory.headers
//	adcp-wba-directory --thumbprint key.pem
//
// Keys are PEM-encoded PKCS#8 Ed25519 private keys, such as those
// adcp-signing-keygen -alg ed25519 writes. The Web Bot Auth profile signs
// only with ed25519, so a P-256 key is refused. Each key is published under its RFC 7638
// thumbprint and signs the response once (WBA Appendix B.1). Serve
// --out-body byte for byte with the headers in --out-headers, one
// "Name: value" per line, and run the command again before the signatures
// expire (--lifetime). Publish a new key here before signing with it.
//
// --thumbprint prints a key's thumbprint, the keyid it signs with.
//
// In-process servers can use signing.DirectoryPublisher instead, which
// re-signs automatically.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

type keyFlags []string

func (k *keyFlags) String() string     { return strings.Join(*k, ",") }
func (k *keyFlags) Set(v string) error { *k = append(*k, v); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "adcp-wba-directory:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("adcp-wba-directory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		keys       keyFlags
		origin     string
		lifetime   time.Duration
		outBody    string
		outHeaders string
		thumbprint string
	)
	fs.Var(&keys, "key", "PEM private key to publish (repeatable, 1 to 100)")
	fs.StringVar(&origin, "origin", "", "the agent's HTTPS origin")
	fs.DurationVar(&lifetime, "lifetime", signing.DefaultDirectoryLifetime, "validity of the directory signatures")
	fs.StringVar(&outBody, "out-body", "", "path to write the directory body")
	fs.StringVar(&outHeaders, "out-headers", "", "path to write the response headers")
	fs.StringVar(&thumbprint, "thumbprint", "", "print the RFC 7638 thumbprint of this PEM private key and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if thumbprint != "" {
		key, err := loadKey(thumbprint)
		if err != nil {
			return err
		}
		jwk, err := signing.PublicDirectoryKey(key)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, jwk.Kid)
		return err
	}
	if origin == "" || len(keys) == 0 || outBody == "" || outHeaders == "" {
		fs.Usage()
		return errors.New("--origin, at least one --key, --out-body, and --out-headers are required")
	}
	var privateKeys []any
	for _, path := range keys {
		key, err := loadKey(path)
		if err != nil {
			return err
		}
		privateKeys = append(privateKeys, key)
	}
	resp, err := signing.SignKeyDirectory(origin, privateKeys, now(), lifetime)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outBody, resp.Body, 0o644); err != nil { // #nosec G306 -- a public key directory
		return err
	}
	var names []string
	for name := range resp.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		for _, v := range resp.Header[name] {
			fmt.Fprintf(&b, "%s: %s\n", name, v)
		}
	}
	if err := os.WriteFile(outHeaders, []byte(b.String()), 0o644); err != nil { // #nosec G306 -- public headers
		return err
	}
	for _, k := range privateKeys {
		jwk, _ := signing.PublicDirectoryKey(k)
		_, _ = fmt.Fprintf(stderr, "published %s; re-sign before %s\n", jwk.Kid, now().Add(lifetime).UTC().Format(time.RFC3339))
	}
	return nil
}

func loadKey(path string) (any, error) {
	pemBytes, err := os.ReadFile(path) // #nosec G304 -- operator-supplied key path
	if err != nil {
		return nil, err
	}
	key, _, err := signing.LoadPrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}
