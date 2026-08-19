// Package uid2client is a native Go client for the UID2 and EUID operator
// services. It performs the AES-GCM request/response envelope handshake
// against a UID2-compatible operator, caches the returned keys in memory,
// and decrypts advertising tokens locally into their raw 32-byte identity
// bytes.
//
// This client speaks the wire protocol documented at
// https://unifiedid.com/docs/getting-started/gs-encryption-decryption and
// matches the behavior of the reference Java client
// (https://github.com/IABTechLab/uid2-client-java) for the token
// decryption path (V2 / V3 / V4).
//
// The client is scope-parameterized: one Client instance decrypts tokens
// for one identity scope (UID2 or EUID). Use [NewUID2Config] or
// [NewEUIDConfig] to construct the appropriate configuration, then [New]
// to build a Client. Callers wanting both scopes construct two clients.
//
// Concurrency: all Client methods are safe for concurrent use. Token
// decryption is a local operation (no HTTP calls on the hot path); key
// refresh happens on a background goroutine bound to the context passed
// to [New].
package uid2client
