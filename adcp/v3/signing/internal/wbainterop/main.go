// Command wbainterop is the adcp-go adapter for the Web Bot Auth interop
// harness in the AdCP spec repository (tests/wba-interop/). The harness
// grades this SDK against Cloudflare's web-bot-auth reference in both
// directions: the SDK signs and the reference verifies, and the reference
// signs and the SDK verifies.
//
// The adapter reads one JSON operation on standard input and writes one JSON
// result on standard output. It calls only the signing package's public API:
//
//	sign_request      {key, origin, request, now}             -> the signed request
//	sign_directory    {keys, origin, now}                     -> {url, status, headers, body}
//	verify_request    {request, directories, now, prior}      -> {ok, identity, keyid}
//	verify_directory  {origin, response, now}                 -> {ok, keyids}
//
// key and keys are private JWKs. A request is {method, url, headers, body}
// and a response is {url, status, headers, body}, with headers as a map of
// lowercase field names. directories maps an origin to its signed directory
// response. now is a Unix time in seconds and is the only clock the SDK reads.
// The adapter verifies each request in prior with the same replay store
// before the graded request, so the harness builds replay state without
// knowing how the SDK stores it.
//
// A refusal is {ok: false, status, rule, error}, and the adapter writes one
// only for an SDK verification refusal: a *signing.WBAError from
// VerifyWebBotAuthRequest, or an error from VerifyDirectoryResponse. status is
// the HTTP status the SDK assigns (0 when it assigns none), and rule is
// "discovery" for a key discovery failure, the AdCP error code for an
// authorization failure, "signature" for any other request signature failure,
// and "directory" for a directory refusal. Any other error (an operation the
// adapter cannot read, a signer error, an untyped verifier error, or a prior
// request that does not verify) is an adapter failure: the adapter writes it
// to standard error and exits 1 with nothing on standard output, so the
// harness never reads it as a refusal.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

type message struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type operation struct {
	Op          string             `json:"op"`
	Request     message            `json:"request"`
	Response    message            `json:"response"`
	Directories map[string]message `json:"directories"`
	Origin      string             `json:"origin"`
	Now         int64              `json:"now"`
	Key         json.RawMessage    `json:"key"`
	Keys        []json.RawMessage  `json:"keys"`
	Prior       []message          `json:"prior"`
}

func main() {
	out, err := runInput(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wbainterop:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

func runInput(r io.Reader) (any, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var op operation
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return run(op)
}

// run returns the operation's result, or an adapter failure as the error.
func run(op operation) (any, error) {
	switch op.Op {
	case "sign_request":
		return signRequest(op)
	case "sign_directory":
		return signDirectory(op)
	case "verify_request":
		return verifyRequest(op)
	case "verify_directory":
		return verifyDirectory(op)
	default:
		return nil, fmt.Errorf("unknown operation %q", op.Op)
	}
}

func refusal(status int, rule string, err error) map[string]any {
	return map[string]any{"ok": false, "status": status, "rule": rule, "error": err.Error()}
}

// requestRefusal reports a request verification refusal with the status and
// reason the SDK assigned. An error that is not a *signing.WBAError is not a
// refusal, and is returned as an adapter failure.
func requestRefusal(err error) (any, error) {
	we := signing.AsWBAError(err)
	switch {
	case we == nil:
		return nil, fmt.Errorf("verifier returned an error that is not a refusal: %w", err)
	case we.DiscoveryFailure:
		return refusal(we.Status, "discovery", err), nil
	case we.Code != "":
		return refusal(we.Status, we.Code, err), nil
	default:
		return refusal(we.Status, "signature", err), nil
	}
}

func clock(now int64) func() time.Time { return func() time.Time { return time.Unix(now, 0) } }

// privateKey decodes a private JWK into the crypto key the SDK signs with.
func privateKey(raw json.RawMessage) (any, error) {
	var k jose.JSONWebKey
	if err := k.UnmarshalJSON(raw); err != nil {
		return nil, err
	}
	if k.IsPublic() {
		return nil, errors.New("key is not a private JWK")
	}
	return k.Key, nil
}

func header(fields map[string]string) http.Header {
	h := http.Header{}
	for name, value := range fields {
		h.Set(name, value)
	}
	return h
}

func flatten(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return out
}

func directoryURL(origin string) string {
	return strings.TrimSuffix(origin, "/") + signing.DirectoryPath
}

func signRequest(op operation) (any, error) {
	key, err := privateKey(op.Key)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if op.Request.Body != "" {
		body = strings.NewReader(op.Request.Body)
	}
	req, err := http.NewRequest(op.Request.Method, op.Request.URL, body)
	if err != nil {
		return nil, err
	}
	req.Header = header(op.Request.Headers)
	if err := signing.SignWebBotAuthRequest(req, key, signing.WBASignOptions{AgentOrigin: op.Origin, Clock: clock(op.Now)}); err != nil {
		return nil, err
	}
	return message{Method: op.Request.Method, URL: op.Request.URL, Headers: flatten(req.Header), Body: op.Request.Body}, nil
}

func signDirectory(op operation) (any, error) {
	keys := make([]any, 0, len(op.Keys))
	for _, raw := range op.Keys {
		key, err := privateKey(raw)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	// created is backdated by a minute, as the reference signer's is, so a
	// verifier that allows no skew on directory signatures accepts it.
	d, err := signing.SignKeyDirectory(op.Origin, keys, time.Unix(op.Now-60, 0), 24*time.Hour)
	if err != nil {
		return nil, err
	}
	return message{URL: directoryURL(op.Origin), Status: d.Status, Headers: flatten(d.Header), Body: string(d.Body)}, nil
}

func verifyRequest(op operation) (any, error) {
	fetch := func(_ context.Context, url string) (*signing.DirectoryResponse, error) {
		for origin, d := range op.Directories {
			if directoryURL(origin) == url {
				return &signing.DirectoryResponse{Status: d.Status, Header: header(d.Headers), Body: []byte(d.Body)}, nil
			}
		}
		return nil, fmt.Errorf("no directory at %s", url)
	}
	opts := signing.WBAVerifyOptions{
		Keys:   signing.NewDirectoryResolver(signing.DirectoryResolverOptions{Fetch: fetch, Clock: clock(op.Now)}),
		Replay: signing.NewMemoryReplayStore(0),
		Clock:  clock(op.Now),
	}
	verify := func(m message) (*signing.VerifiedSigner, error) {
		req, err := http.NewRequest(m.Method, m.URL, bytes.NewBufferString(m.Body))
		if err != nil {
			return nil, err
		}
		req.Header = header(m.Headers)
		req.Host = req.URL.Host
		return signing.VerifyWebBotAuthRequest(req, opts)
	}
	// A prior request builds replay state, so it must verify: one that does
	// not is a broken setup, not the SDK's answer to the graded request.
	for i, m := range op.Prior {
		if _, err := verify(m); err != nil {
			return nil, fmt.Errorf("prior request %d did not verify: %w", i, err)
		}
	}
	signer, err := verify(op.Request)
	if err != nil {
		return requestRefusal(err)
	}
	return map[string]any{"ok": true, "identity": signer.AgentURL, "keyid": signer.KeyID}, nil
}

func verifyDirectory(op operation) (any, error) {
	keys, err := signing.VerifyDirectoryResponse(op.Origin, &signing.DirectoryResponse{
		Status: op.Response.Status, Header: header(op.Response.Headers), Body: []byte(op.Response.Body),
	}, time.Unix(op.Now, 0))
	if err != nil {
		return refusal(0, "directory", err), nil
	}
	ids := make([]string, 0, len(keys))
	for kid := range keys {
		ids = append(ids, kid)
	}
	return map[string]any{"ok": true, "keyids": ids}, nil
}
