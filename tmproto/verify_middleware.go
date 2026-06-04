package tmproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// VerifyOptions configures a TMP-signature verifier middleware.
type VerifyOptions struct {
	// KeyStore resolves kids carried by incoming requests. Required.
	KeyStore KeyStore

	// OwnEndpointURL is this provider's registered endpoint URL — verifier
	// rejects signatures that don't bind to it.
	OwnEndpointURL string

	// RequireSignature, when true, rejects requests that arrive without a
	// signature. When false, unsigned requests pass through to the inner
	// handler with a warning log line — useful only for migration windows.
	RequireSignature bool

	// BodyLimit caps the bytes the verifier will read off the wire
	// before computing the signature. Zero falls back to a 64 KiB
	// default sized for identity-match payloads; agents whose downstream
	// handler accepts larger bodies (context-match's 256 KiB artifact
	// payloads) must raise this to match — io.LimitReader silently
	// truncates on overflow and the truncated body fails JSON decode,
	// which surfaces as "invalid request body" rather than "too large".
	BodyLimit int64

	// Logger receives verification outcomes. Defaults to slog.Default().
	Logger *slog.Logger

	// Now optionally returns the wall-clock time the verifier compares against
	// the daily epoch. Defaults to time.Now.
	Now func() time.Time
}

func (o *VerifyOptions) bodyLimit() int64 {
	if o.BodyLimit > 0 {
		return o.BodyLimit
	}
	return 64 * 1024
}

func (o *VerifyOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *VerifyOptions) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// VerifyContextMatchHandler wraps an HTTP handler with TMP context-match
// signature verification. The handler is invoked with the original request
// body re-attached so it can decode normally.
func VerifyContextMatchHandler(next http.Handler, opts VerifyOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, opts.bodyLimit()))
		if err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				writeVerifierError(w, http.StatusRequestEntityTooLarge, ErrorCodeInvalidRequest, "request body too large")
				return
			}
			writeVerifierError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "failed to read request body")
			return
		}
		_ = r.Body.Close()

		var parsed ContextMatchRequest
		if err := decodeStrict(body, &parsed); err != nil {
			writeVerifierError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "request body is not valid JSON")
			return
		}

		sig, kid, headerErr := ExtractSignatureHeaders(r.Header)
		if headerErr != nil {
			if !opts.RequireSignature {
				opts.logger().Warn("tmp signature missing — accepting unsigned",
					"path", r.URL.Path, "request_id", parsed.RequestID)
				replayBody(r, body)
				next.ServeHTTP(w, r)
				return
			}
			writeVerifierError(w, http.StatusUnauthorized, ErrorCodeInvalidRequest, "signature required")
			return
		}

		if err := VerifyContextMatch(&parsed, opts.OwnEndpointURL, sig, kid, opts.KeyStore, opts.now()); err != nil {
			opts.logger().Warn("tmp context-match signature rejected",
				"path", r.URL.Path, "request_id", parsed.RequestID, "kid", kid, "error", err)
			writeVerifierError(w, http.StatusUnauthorized, ErrorCodeInvalidRequest, "signature verification failed")
			return
		}

		replayBody(r, body)
		next.ServeHTTP(w, r)
	})
}

// VerifyIdentityMatchHandler wraps an HTTP handler with TMP identity-match
// signature verification.
func VerifyIdentityMatchHandler(next http.Handler, opts VerifyOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, opts.bodyLimit()))
		if err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				writeVerifierError(w, http.StatusRequestEntityTooLarge, ErrorCodeInvalidRequest, "request body too large")
				return
			}
			writeVerifierError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "failed to read request body")
			return
		}
		_ = r.Body.Close()

		var parsed IdentityMatchRequest
		if err := decodeStrict(body, &parsed); err != nil {
			writeVerifierError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "request body is not valid JSON")
			return
		}

		sig, kid, headerErr := ExtractSignatureHeaders(r.Header)
		if headerErr != nil {
			if !opts.RequireSignature {
				opts.logger().Warn("tmp signature missing — accepting unsigned",
					"path", r.URL.Path, "request_id", parsed.RequestID)
				replayBody(r, body)
				next.ServeHTTP(w, r)
				return
			}
			writeVerifierError(w, http.StatusUnauthorized, ErrorCodeInvalidRequest, "signature required")
			return
		}

		if err := VerifyIdentityMatch(&parsed, opts.OwnEndpointURL, sig, kid, opts.KeyStore, opts.now()); err != nil {
			opts.logger().Warn("tmp identity-match signature rejected",
				"path", r.URL.Path, "request_id", parsed.RequestID, "kid", kid, "error", err)
			writeVerifierError(w, http.StatusUnauthorized, ErrorCodeInvalidRequest, "signature verification failed")
			return
		}

		replayBody(r, body)
		next.ServeHTTP(w, r)
	})
}

func replayBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
}

// decodeStrict parses body into v while rejecting fields the receiver doesn't
// know about. The verifier recomputes the signing input from the parsed
// struct, so silently dropping unknown fields would let a future-protocol
// extension produce a signature the verifier could never reproduce. Failing
// loudly forces operators to update their build before accepting traffic.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeVerifierError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Code:    code,
		Message: message,
	})
}
