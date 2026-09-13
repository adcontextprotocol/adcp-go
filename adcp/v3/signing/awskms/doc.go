// Package awskms implements signing.SigningProvider (from
// adcp-go/adcp/v3/signing) against an AWS KMS asymmetric signing key, so an
// AdCP agent's private signing key never has to leave AWS KMS / the
// CloudHSM cluster backing it.
//
// This package is its own Go module — isolated from adcp/v3/signing's zero
// third-party dependency tree — for the same reason
// adcp-go/registry/redisstore and adcp-go/registry/glidestore are separate
// modules from adcp-go/registry: this repo runs in TEEs and is embedded in
// existing ad tech infrastructure (see the repo's AGENTS.md, "Zero
// unnecessary dependencies"), and importing adcp/v3/signing must never pull
// in aws-sdk-go-v2 for callers who don't want AWS KMS. Only a caller who
// explicitly imports this awskms module pays for that dependency.
//
// # Setup
//
// Provision an asymmetric KMS key with KeyUsage=SIGN_VERIFY and
// KeySpec=ECC_NIST_P256 (the AWS console, `aws kms create-key
// --key-usage SIGN_VERIFY --key-spec ECC_NIST_P256`, or Terraform's
// aws_kms_key resource all support this). AWS KMS does not support
// Ed25519/EdDSA asymmetric signing keys as of this writing — only RSA and
// NIST/SECG elliptic curves — so this provider always reports
// signing.AlgES256 (ECDSA-P256).
//
// # Usage
//
//	cfg, err := config.LoadDefaultConfig(ctx)
//	if err != nil {
//		log.Fatal(err)
//	}
//	provider, err := awskms.New(awskms.Options{
//		Client:   kms.NewFromConfig(cfg),
//		KMSKeyID: "arn:aws:kms:us-east-1:111122223333:key/1234abcd-...",
//		KeyID:    "buyer-agent-2026", // the AdCP `kid` published in your JWKS
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	signer, err := signing.NewSigner(signing.SignerOptions{Provider: provider})
//	if err != nil {
//		log.Fatal(err)
//	}
//	if err := signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}); err != nil {
//		log.Fatal(err)
//	}
//
// New does not call KMS — Provider fetches and lazily caches its public key
// (via GetPublicKey) only on first use of PublicKey, and only caches a
// successful result. Calling KMS eagerly before your listener binds is the
// one lifecycle mistake this package deliberately avoids: a KMS call
// blocked in the AWS SDK retryer's backoff can hang process startup
// indefinitely with no visible error, and a listener that never binds fails
// its health check with no diagnostic pointing at KMS.
//
// # Publishing the JWK and pinning against silent rotation
//
// Publish this key's JWK once, and re-verify it hasn't silently rotated on
// every deploy:
//
//	jwk, err := signing.NewPublicJWKFromProvider(ctx, provider, "buyer-agent-2026", signing.ProfileRequestSigning.AdcpUse)
//	// serve jwk in the JWKS document at your agent's jwks_uri
//
//	// At startup, after your listener binds: fail loudly if KMS's alias
//	// now points at a different key than the one this deploy expects,
//	// instead of quietly signing with a key no verifier will accept.
//	err = signing.AssertProviderPublicKeyMatchesSPKI(ctx, provider, expectedSPKIBytes)
//
// # Testing without live AWS infrastructure
//
// Provider calls out through the SignAPI interface — the narrow subset of
// *kms.Client's Sign and GetPublicKey calls this package actually uses —
// rather than the concrete *kms.Client type. This is the pattern the AWS
// SDK for Go v2 documents for unit-testing service clients: define the
// narrow interface your code needs and substitute a fake in tests.
// provider_test.go exercises the real request-construction and
// response-mapping logic (which fields Sign sends, how the DER-encoded
// signature KMS returns is converted to the AdCP profile's fixed-width
// IEEE P1363 wire format, the GetPublicKey cache-on-success-only behavior,
// and the *signing.SigningError contract on failure) against such a fake —
// no AWS credentials, network access, or LocalStack needed.
//
// What is NOT covered by these tests: an actual round trip against live
// KMS (IAM permissions, key policy, throttling/retry behavior under real
// network conditions, KMS's actual GetPublicKey DER encoding for a real
// key). That requires an AWS account and is intentionally out of scope for
// this package's automated test suite. To verify manually against a real
// key: create an ECC_NIST_P256/SIGN_VERIFY key, wire it into the Usage
// example above with a real kms.NewFromConfig client, sign a request, and
// verify it with signing.VerifyRequestSignature using the JWK from
// NewPublicJWKFromProvider — a successful VerifyRequestSignature call is
// the end-to-end proof that KMS's actual response shapes (DER encoding,
// SubjectPublicKeyInfo layout) match what this package assumes.
package awskms
