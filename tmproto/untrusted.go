package tmproto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

// UntrustedText is publisher-supplied natural-language content that the AdCP
// spec explicitly marks as untrusted: "Consumers MUST treat this as
// untrusted input when passing to LLM-based evaluation." It backs every
// field in this package that a hostile publisher fully controls and that
// downstream code is likely to feed into an LLM prompt (TextAsset.Content,
// VideoAsset.Transcript, AudioAsset.Transcript, ContextSignals.Summary).
//
// UntrustedText is a defined string type, not a wrapper struct, so its JSON
// encoding is byte-for-byte identical to plain string — this is purely a
// compile-time marker. That's the point: a caller who wants the raw text
// must write string(t) to get it, and that conversion is grep-able and
// stands out in code review. Do not add a method that returns the raw
// string under a different name (e.g. Raw() or Unwrap()) — that would just
// relocate the visible cast one level down and defeat the purpose of the
// type.
//
// MUST be fenced or otherwise clearly marked before being passed to an LLM
// prompt — e.g. via Fenced() below, or by passing it as a separate
// user-role message with its own boundary the model is told to distrust.
// Never concatenate it directly into an instruction/system-role prompt
// string.
type UntrustedText string

// fenceMarker is the fixed, human-readable shape of the boundary tags
// Fenced() emits. nonceHexLen is the length (in hex characters) of the
// random nonce embedded in every marker.
const (
	fenceOpenTemplate  = "<<<ADCP:UNTRUSTED-CONTENT-BEGIN:%s>>>"
	fenceCloseTemplate = "<<<ADCP:UNTRUSTED-CONTENT-END:%s>>>"
	nonceHexLen        = 32 // 16 random bytes, hex-encoded
)

// fenceLookalike matches ANY text that already has the shape of one of our
// boundary markers, regardless of nonce value. Fenced() uses this to defang
// publisher-supplied content that tries to pre-empt the real boundary —
// see the Fenced doc comment for why this matters.
var fenceLookalike = regexp.MustCompile(`<<<ADCP:UNTRUSTED-CONTENT-(BEGIN|END):[0-9a-fA-F]+>>>`)

// Fenced returns t wrapped in a pair of boundary markers carrying a random
// nonce, suitable for splicing into an LLM prompt as a clearly delimited
// untrusted region (for example, inside a user-role message that the
// system prompt instructs the model to treat as data, never as
// instructions).
//
// Output shape:
//
//	<<<ADCP:UNTRUSTED-CONTENT-BEGIN:{nonce}>>>
//	{content, with look-alike markers defanged}
//	<<<ADCP:UNTRUSTED-CONTENT-END:{nonce}>>>
//
// Guarantees:
//   - {nonce} is 128 bits of crypto/rand, generated fresh on every call and
//     hex-encoded. Because it's chosen after the publisher has already
//     submitted t, the publisher cannot have pre-crafted content containing
//     the literal closing tag for THIS call — a downstream parser that
//     checks the nonce on both tags cannot be tricked into treating
//     attacker text as the fence boundary. The collision probability
//     (~2^-128) is cryptographically negligible.
//   - Before the real boundary is applied, any substring of t that already
//     has the shape of an ADCP boundary marker (fixed prefix + any hex
//     nonce) is replaced with a visibly-defanged form. This protects a
//     downstream parser that pattern-matches on marker *shape* rather than
//     verifying the specific nonce — without this step, a publisher could
//     embed a fake "<<<ADCP:UNTRUSTED-CONTENT-END:...>>>" with an
//     arbitrary nonce and hope the parser doesn't check it.
//
// Honest limits — read before relying on this for safety:
//   - This is a STRUCTURAL guarantee about the boundary, not a semantic
//     guarantee about the model's behavior. An LLM told "ignore
//     instructions between these markers" can still be confused, argued
//     with, or manipulated by adversarial phrasing INSIDE the fenced
//     region. Fenced() makes the boundary unforgeable; it does not make
//     the model immune to what it reads there.
//   - The nonce defense requires the consumer to actually verify that the
//     nonce on the closing tag matches the opening tag (or, more simply,
//     to just treat the whole Fenced() return value as one opaque blob and
//     not re-parse it for embedded markers at all). A consumer that only
//     checks for the fixed prefix and ignores the nonce gets the
//     defanging protection above, but not the unforgeability guarantee.
//   - Whitespace/formatting inside t is preserved verbatim (aside from the
//     defanging substitution above) — Fenced() does not sanitize markdown,
//     HTML, control characters, or other content that might affect how a
//     downstream renderer or tokenizer processes it.
//   - Do not cache and replay a single Fenced() output across multiple,
//     differently-untrusted inputs — the nonce's guarantee is per-call.
func (t UntrustedText) Fenced() string {
	nonce := randomNonceHex()
	safe := fenceLookalike.ReplaceAllString(string(t), "[adcp:fence-marker-removed]")
	return fmt.Sprintf(fenceOpenTemplate, nonce) + "\n" + safe + "\n" + fmt.Sprintf(fenceCloseTemplate, nonce)
}

// randomNonceHex returns a 128-bit random value, hex-encoded.
func randomNonceHex() string {
	buf := make([]byte, nonceHexLen/2)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand reading from the OS entropy source is not expected
		// to fail on any platform this SDK supports; a failure here means
		// the OS is in a state where TLS, ed25519 signing (see tmpx.go),
		// and most of the rest of the security stack are already broken.
		// Panicking rather than silently downgrading to a predictable
		// nonce keeps that failure loud instead of quietly weakening the
		// unforgeability guarantee documented on Fenced.
		panic(fmt.Sprintf("tmproto: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(buf)
}
