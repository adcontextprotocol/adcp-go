// Command adcp-signing-keygen generates an AdCP request-signing keypair and
// emits the PEM-encoded private key plus the public JWK.
//
// Usage:
//
//	adcp-signing-keygen --out path/to/key.pem [--alg ed25519|es256] [--kid KID]
//
// The PEM-encoded private key is written to --out (mode 0600). The public JWK
// is written to stdout as JSON — paste it into your agent's JWKS document
// served at jwks_uri. --out is required; the tool refuses to write the
// private key to stdout to avoid accidental secret leakage into terminal
// scrollback or pipelines.
//
// Default alg is ed25519.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/adcontextprotocol/adcp-go/adcp/signing"
)

func main() {
	var (
		alg  string
		kid  string
		out  string
		help bool
	)
	flag.StringVar(&alg, "alg", "ed25519", "signing algorithm: ed25519 or es256")
	flag.StringVar(&kid, "kid", "", "JWK kid (optional; generated when empty)")
	flag.StringVar(&out, "out", "", "path to write PEM-encoded private key (required)")
	flag.BoolVar(&help, "h", false, "show help")
	flag.Parse()

	if help {
		flag.Usage()
		os.Exit(0)
	}

	if out == "" {
		fmt.Fprintln(os.Stderr, "--out is required: the private key is a secret and must not be written to stdout")
		flag.Usage()
		os.Exit(2)
	}

	var algorithm signing.Algorithm
	switch alg {
	case "ed25519":
		algorithm = signing.AlgEd25519
	case "es256":
		algorithm = signing.AlgES256
	default:
		fmt.Fprintf(os.Stderr, "unsupported alg %q (want ed25519 or es256)\n", alg)
		os.Exit(2)
	}

	result, err := signing.GenerateSigningKey(algorithm, kid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keygen: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(out, result.PrivateKeyPEM, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "private key written to %s (permissions 0600)\n", out)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result.PublicJWK); err != nil {
		fmt.Fprintf(os.Stderr, "encode jwk: %v\n", err)
		os.Exit(1)
	}
}
