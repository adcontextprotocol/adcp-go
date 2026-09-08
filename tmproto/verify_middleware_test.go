package tmproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mkVerifier(t *testing.T, requireSig bool, ownEndpoint string) (http.Handler, *Signer, *bytes.Buffer) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner("kid-mw", priv)
	if err != nil {
		t.Fatal(err)
	}
	ks := NewStaticKeyStore([]SigningKey{PublicSigningKey(signer.KeyID, pub)})

	innerCalls := &bytes.Buffer{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		innerCalls.Write(body)
		w.WriteHeader(http.StatusOK)
	})
	mw := VerifyContextMatchHandler(inner, VerifyOptions{
		KeyStore:         ks,
		OwnEndpointURL:   ownEndpoint,
		RequireSignature: requireSig,
	})
	return mw, signer, innerCalls
}

func TestMiddleware_ContextMatchHappyPath(t *testing.T) {
	mw, signer, innerCalls := mkVerifier(t, true, "https://provider.example.com")

	body := []byte(`{"request_id":"r1","property_id":"p","property_rid":"rid","property_type":"website","placement_id":"sb","package_ids":["a"]}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	parsed := &ContextMatchRequest{
		RequestID:    "r1",
		PropertyID:   "p",
		PropertyRID:  "rid",
		PropertyType: "website",
		PlacementID:  "sb",
		PackageIDs:   []string{"a"},
	}
	sig := signer.SignContextMatch(parsed, "https://provider.example.com", CurrentEpoch())
	req.Header.Set(HeaderTMPSignature, sig)
	req.Header.Set(HeaderTMPKeyID, signer.KeyID)

	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Inner handler must have received the original body intact.
	if !bytes.Equal(innerCalls.Bytes(), body) {
		t.Fatalf("inner body = %q, want %q", innerCalls.Bytes(), body)
	}
}

func TestMiddleware_RequireSignatureMissing(t *testing.T) {
	mw, _, innerCalls := mkVerifier(t, true, "https://provider.example.com")
	req, _ := http.NewRequest("POST", "/tmp/context",
		bytes.NewReader([]byte(`{"request_id":"r","property_rid":"p","placement_id":"s"}`)))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if innerCalls.Len() != 0 {
		t.Fatal("inner handler should not have been called")
	}
}

func TestMiddleware_AllowUnsigned(t *testing.T) {
	mw, _, innerCalls := mkVerifier(t, false, "https://provider.example.com")
	body := []byte(`{"request_id":"r","property_rid":"p","placement_id":"s"}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(innerCalls.Bytes(), body) {
		t.Fatalf("inner body = %q, want %q", innerCalls.Bytes(), body)
	}
}

func TestMiddleware_BadSignatureRejects(t *testing.T) {
	mw, _, _ := mkVerifier(t, true, "https://provider.example.com")
	body := []byte(`{"request_id":"r","property_rid":"p","placement_id":"s"}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	req.Header.Set(HeaderTMPSignature, "AAAAAA")
	req.Header.Set(HeaderTMPKeyID, "kid-mw")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var resp ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Code == "" {
		t.Fatal("expected error code in response body")
	}
}

// TestMiddleware_ErrorEnvelope_PostParse pins the two mandatory
// error.json fields on verifier-rejected responses: type MUST be
// the "error" discriminator const and request_id MUST echo the parsed
// value. Post-parse means the JSON decoded but signature verification
// failed, so the request_id is known and MUST be surfaced back for
// client-side correlation.
func TestMiddleware_ErrorEnvelope_PostParse(t *testing.T) {
	mw, _, _ := mkVerifier(t, true, "https://provider.example.com")
	body := []byte(`{"request_id":"echoed-req-id","property_rid":"p","placement_id":"s"}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	req.Header.Set(HeaderTMPSignature, "AAAAAA")
	req.Header.Set(HeaderTMPKeyID, "kid-mw")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != TypeError {
		t.Errorf("Type = %q, want %q", resp.Type, TypeError)
	}
	if resp.RequestID != "echoed-req-id" {
		t.Errorf("RequestID = %q, want %q", resp.RequestID, "echoed-req-id")
	}
	if resp.Code != ErrorCodeInvalidRequest {
		t.Errorf("Code = %q, want %q", resp.Code, ErrorCodeInvalidRequest)
	}
}

// TestMiddleware_ErrorEnvelope_PreParse pins the same error-envelope
// fields on failures that occur before the request body is decoded.
// The request_id is unknown at that point so MUST be empty — but the
// type discriminator MUST still be "error" so downstream decoders can
// dispatch on shape without special-casing pre-parse errors.
func TestMiddleware_ErrorEnvelope_PreParse(t *testing.T) {
	mw, _, _ := mkVerifier(t, true, "https://provider.example.com")
	body := []byte(`this is not valid JSON`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != TypeError {
		t.Errorf("Type = %q, want %q", resp.Type, TypeError)
	}
	if resp.RequestID != "" {
		t.Errorf("RequestID = %q, want empty (pre-parse failure)", resp.RequestID)
	}
	if resp.Code != ErrorCodeInvalidRequest {
		t.Errorf("Code = %q, want %q", resp.Code, ErrorCodeInvalidRequest)
	}
}

// TestDecodeStrict_AcceptsSchemaAndFormatKind pins the primary fix:
// clients that pre-validate their payload by including a top-level
// $schema pointer, and artifacts that carry a format_kind hint from
// the content-standards schema, MUST NOT be rejected by the strict
// decoder. A regression that drops either field (e.g. a regen that
// re-skips $schema in the generator, or a hand-edit removing
// FormatKind) reintroduces a 400 on schema-valid traffic and breaks
// pre-validating clients silently.
func TestDecodeStrict_AcceptsSchemaAndFormatKind(t *testing.T) {
	t.Run("context match request with $schema and artifact.format_kind", func(t *testing.T) {
		body := []byte(`{
			"$schema": "https://adcontextprotocol.org/schemas/latest/trusted-match/context-match-request.json",
			"type": "context_match_request",
			"request_id": "r1",
			"property_rid": "p",
			"placement_id": "s",
			"artifact": {
				"property_rid": "p",
				"artifact_id": "a1",
				"format_kind": "article",
				"assets": []
			}
		}`)
		var parsed ContextMatchRequest
		if err := decodeStrict(body, &parsed); err != nil {
			t.Fatalf("decodeStrict rejected schema-valid body: %v", err)
		}
		if parsed.Schema == "" {
			t.Error("Schema field not populated from wire $schema")
		}
		if parsed.Artifact == nil {
			t.Fatal("Artifact not decoded")
		}
		if parsed.Artifact.FormatKind != "article" {
			t.Errorf("Artifact.FormatKind = %q, want %q", parsed.Artifact.FormatKind, "article")
		}
	})

	t.Run("identity match request with $schema", func(t *testing.T) {
		body := []byte(`{
			"$schema": "https://adcontextprotocol.org/schemas/latest/trusted-match/identity-match-request.json",
			"type": "identity_match_request",
			"request_id": "r1",
			"seller_agent_url": "https://seller.example.com/agent",
			"identities": [{"user_token": "tok", "uid_type": "uid2"}]
		}`)
		var parsed IdentityMatchRequest
		if err := decodeStrict(body, &parsed); err != nil {
			t.Fatalf("decodeStrict rejected schema-valid body: %v", err)
		}
		if parsed.Schema == "" {
			t.Error("Schema field not populated from wire $schema")
		}
	})
}
