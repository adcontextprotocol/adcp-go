//go:build mock_e2e

// Package uid2client mock_e2e_test.go exercises the client end-to-end
// against a locally-launched public UID2 operator running in
// storage_mock mode. The build tag AND the MOCK_E2E env var are both
// required — the tag stops accidental compilation, the env var stops
// accidental execution once the tag is on.
//
// Run:
//
//	MOCK_E2E=1 go test -tags mock_e2e -v -run TestMockE2E ./uid2client/...
//
// See uid2client/README.md ("Mock-operator e2e tests") for background and
// uid2client/testdata/mock/README.md for what the fixtures do.

package uid2client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uid2OperatorImage pins the mock-operator container image the test boots.
// The tag is intentionally floating on `latest` so the test picks up
// upstream fixes; if the classpath resource layout drifts and the test
// breaks, pin to a known-good digest (e.g. sha256:...) here and update
// testdata/mock/README.md.
const uid2OperatorImage = "ghcr.io/iabtechlab/uid2-operator:latest"

// Mock-operator throwaway credentials. Sourced verbatim from
// uid2-operator/src/main/resources/com.uid2.core/test/clients/clients.json
// in the public IABTechLab repo — see testdata/mock/README.md. These
// keys unlock only the local mock stack; they have no meaning against a
// real operator.
const (
	mockDSPAPIKey       = "UID2-C-L-123-t32pCM.5NCX1E94UgOd2f8zhsKmxzCoyhXohHYSSWR8U=" //nolint:gosec // Public mock fixture, safe by design.
	mockDSPClientSecret = "FsD4bvtjMkeTonx6HvQp6u0EiI1ApGH4pIZzZ5P7UcQ="              //nolint:gosec // Public mock fixture, safe by design.

	mockGeneratorAPIKey       = "UID2-C-L-124-H8VwqX.l2G4TCuUWYAqdqkeG/UqtFoPEoXirKn4kHWxc=" //nolint:gosec // Public mock fixture, safe by design.
	mockGeneratorClientSecret = "NcMgi6Y8C80SlxvV7pYlfcvEIo+2b0508tYQ3pKK8HM="              //nolint:gosec // Public mock fixture, safe by design.

	mockMapperAPIKey       = "UID2-C-L-125-E5w9L8.T5og45yFqQeoj4ubh9IVqXcaSVwk7A5XyG958=" //nolint:gosec // Public mock fixture, safe by design.
	mockMapperClientSecret = "3YAgjckHGQyBgSFj64ZsLf8WlUnvrQhLKuG7rljp6W4="              //nolint:gosec // Public mock fixture, safe by design.
)

// TestMockE2E validates wire correctness of uid2client.Client against a
// real UID2 operator (as opposed to hand-rolled fixtures in the other
// tests in this package). It mints a token with the operator's own
// /v2/token/generate, asks the operator what raw UID that token should
// decode to via /v2/identity/map, and confirms Client.Decrypt returns
// the same raw bytes.
//
// This is the wire test; the crypto unit tests in envelope_test.go /
// token_test.go stay authoritative on component behavior. A regression
// here means our envelope framing, key-refresh parsing, or token decode
// no longer matches what the operator emits.
func TestMockE2E(t *testing.T) {
	if os.Getenv("MOCK_E2E") == "" {
		t.Skip("MOCK_E2E env var not set; skipping (set MOCK_E2E=1 to run — requires Docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not on PATH: %v", err)
	}
	if err := exec.Command("docker", "ps").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	op := startMockOperator(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", op.hostPort)

	// The operator needs a moment after container start before it binds
	// port 8080 and its stores finish loading. Poll /ops/healthcheck.
	if err := waitForHealthcheck(ctx, baseURL, 60*time.Second); err != nil {
		// Include the container's last log lines to make diagnosis
		// actionable without another manual `docker logs` round-trip.
		tail, _ := exec.Command("docker", "logs", "--tail", "40", op.name).CombinedOutput() //nolint:gosec // op.name is minted from crypto/rand in this file, not user input.
		t.Fatalf("operator did not become healthy: %v\ncontainer logs (tail 40):\n%s", err, tail)
	}
	t.Logf("operator up at %s", baseURL)

	genCtx, genCancel := context.WithTimeout(ctx, 10*time.Second)
	defer genCancel()
	token, err := generateAdvertisingToken(genCtx, baseURL, "test@example.com")
	if err != nil {
		t.Fatalf("generate advertising token: %v", err)
	}
	t.Logf("generated advertising token (%d bytes)", len(token))

	mapCtx, mapCancel := context.WithTimeout(ctx, 10*time.Second)
	defer mapCancel()
	wantRaw, err := fetchExpectedRawUID(mapCtx, baseURL, "test@example.com")
	if err != nil {
		t.Fatalf("fetch expected raw UID: %v", err)
	}
	t.Logf("expected raw UID: %s", base64.StdEncoding.EncodeToString(wantRaw))

	// Construct our client pointing at the mock operator. New() blocks
	// on the initial key refresh so any wire-level mismatch surfaces
	// here, not on the first Decrypt.
	cfg := NewUID2Config(mockDSPAPIKey, mockDSPClientSecret)
	cfg.OperatorURL = baseURL
	// A long refresh interval keeps the background goroutine quiet for
	// the duration of this test — we don't need it to fire.
	cfg.KeyRefreshInterval = 10 * time.Minute
	clientCtx, clientCancel := context.WithCancel(ctx)
	defer clientCancel()
	client, err := New(clientCtx, cfg)
	if err != nil {
		t.Fatalf("uid2client.New: %v", err)
	}

	decryptCtx, decryptCancel := context.WithTimeout(ctx, 5*time.Second)
	defer decryptCancel()
	gotRaw, err := client.Decrypt(decryptCtx, token)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(gotRaw, wantRaw) {
		t.Fatalf("raw UID mismatch:\n got: %s\nwant: %s\n(hex got=%s want=%s)",
			base64.StdEncoding.EncodeToString(gotRaw),
			base64.StdEncoding.EncodeToString(wantRaw),
			hex.EncodeToString(gotRaw),
			hex.EncodeToString(wantRaw),
		)
	}
	t.Logf("Decrypt round-trip matched operator's own identity/map result")
}

// operator holds the bits the test needs to reference an already-running
// operator container: its Docker name (for cleanup / log tail) and the
// host port bound to the container's 8080.
type operator struct {
	name     string
	hostPort int
}

// startMockOperator boots the operator container and registers cleanup.
// The container name and host port are randomized so parallel test runs
// (or a stuck container from a previous run) don't collide.
func startMockOperator(t *testing.T) *operator {
	t.Helper()

	hostPort, err := freeTCPPort()
	if err != nil {
		t.Fatalf("pick host port: %v", err)
	}

	name := fmt.Sprintf("uid2-op-mockE2E-%s", randHex(t, 6))

	// The docker image ships without a top-level local-config.json; we
	// supply one that (a) puts the operator in storage_mock mode, (b)
	// enables the V2 envelope on /v2/key/bidstream, and (c) points every
	// *_metadata_path at the classpath resource that stub-mode reads
	// from. Our patched keysets.json wins over the jar-shipped one by
	// virtue of appearing first on the classpath.
	testdataDir, err := abs("testdata/mock/patched")
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}
	if _, err := os.Stat(testdataDir); err != nil {
		t.Fatalf("testdata not found (run from module root?): %v", err)
	}

	javaCmd := strings.Join([]string{
		"exec java",
		"-XX:MaxRAMPercentage=60",
		"-XX:-UseCompressedOops",
		"-Djava.security.egd=file:/dev/./urandom",
		"-Dvertx.logger-delegate-factory-class-name=io.vertx.core.logging.SLF4JLogDelegateFactory",
		"-Dlogback.configurationFile=/app/conf/logback.xml",
		"-cp /patched:/app/${JAR_NAME}-${JAR_VERSION}.jar",
		"com.uid2.operator.Main",
	}, " ")

	args := []string{
		"run", "-d", "--rm",
		"--platform", "linux/amd64",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", hostPort),
		"-e", "VERTX_CONFIG_PATH=/patched/conf/local-config.json",
		"-v", testdataDir + ":/patched:ro",
		"--entrypoint", "sh",
		uid2OperatorImage,
		"-c", "cd /app && " + javaCmd,
	}
	out, err := exec.Command("docker", args...).CombinedOutput() //nolint:gosec // args are all constants or values minted in this file (random name, os.Stat'd testdata dir, image constant); nothing user-provided.
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		// `docker rm -f` succeeds whether the container is running,
		// stopped, or already-gone; ignore its output. Runs on failure
		// too because it's a t.Cleanup, not a defer inside the happy path.
		_ = exec.Command("docker", "rm", "-f", name).Run() //nolint:gosec // `name` is minted from crypto/rand in this file.
	})

	return &operator{name: name, hostPort: hostPort}
}

// waitForHealthcheck polls the operator's /ops/healthcheck until it
// returns HTTP 200 or the timeout elapses.
func waitForHealthcheck(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := baseURL + "/ops/healthcheck"
	client := &http.Client{Timeout: 2 * time.Second}

	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("%w (last probe error: %v)", err, lastErr)
			}
			return err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timeout after %s (last probe error: %v)", timeout, lastErr)
			}
			return fmt.Errorf("timeout after %s", timeout)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// generateAdvertisingToken calls /v2/token/generate on the operator using
// GENERATOR (site 124) credentials and returns the resulting advertising
// token string. Encoding uses the in-package request envelope helpers so
// this call cross-tests our own request-side crypto as well.
func generateAdvertisingToken(ctx context.Context, baseURL, email string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"email":        email,
		"optout_check": 1,
	})
	if err != nil {
		return "", err
	}
	resp, err := postV2(ctx, baseURL+"/v2/token/generate",
		mockGeneratorAPIKey, mockGeneratorClientSecret, body)
	if err != nil {
		return "", err
	}
	token, ok := jsonPath[string](resp, "body", "advertising_token")
	if !ok {
		return "", fmt.Errorf("advertising_token not in response: %v", resp)
	}
	return token, nil
}

// fetchExpectedRawUID calls /v2/identity/map on the operator using MAPPER
// (site 125) credentials and returns the base64-decoded advertising_id
// for the supplied email. That advertising_id is the same 32-byte raw
// UID that Client.Decrypt should surface.
func fetchExpectedRawUID(ctx context.Context, baseURL, email string) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"email": []string{email},
	})
	if err != nil {
		return nil, err
	}
	resp, err := postV2(ctx, baseURL+"/v2/identity/map",
		mockMapperAPIKey, mockMapperClientSecret, body)
	if err != nil {
		return nil, err
	}
	mapped, ok := jsonPath[[]any](resp, "body", "mapped")
	if !ok || len(mapped) == 0 {
		return nil, fmt.Errorf("identity/map returned empty mapped: %v", resp)
	}
	entry, ok := mapped[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("identity/map first entry not object: %T", mapped[0])
	}
	advertisingID, ok := entry["advertising_id"].(string)
	if !ok {
		return nil, fmt.Errorf("advertising_id missing/wrong type: %v", entry)
	}
	return base64.StdEncoding.DecodeString(advertisingID)
}

// postV2 sends a v2 envelope-wrapped request and returns the decoded JSON
// response body. Uses the same sealRequestEnvelope / openResponseEnvelope
// helpers the production client uses, so this cross-tests our own request
// crypto against the operator's decrypter.
func postV2(ctx context.Context, url, apiKey, clientSecretB64 string, payload []byte) (map[string]any, error) {
	secret, err := base64.StdEncoding.DecodeString(clientSecretB64)
	if err != nil {
		return nil, fmt.Errorf("decode client secret: %w", err)
	}
	envelope, nonce, err := sealRequestEnvelope(secret, payload, time.Now())
	if err != nil {
		return nil, fmt.Errorf("seal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(base64.StdEncoding.EncodeToString(envelope)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Do NOT log the API key on failure; include only the URL / status
		// and the (already-encrypted-if-the-operator-is-well-behaved) body.
		return nil, fmt.Errorf("POST %s: http %d: %s", url, resp.StatusCode, respBody)
	}

	plain, err := openResponseEnvelope(secret, string(respBody), nonce)
	if err != nil {
		return nil, fmt.Errorf("open response envelope: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if status, _ := out["status"].(string); status != "success" {
		return nil, fmt.Errorf("operator returned status=%q: %v", status, out)
	}
	return out, nil
}

// jsonPath walks a nested map[string]any and returns the value at the
// given key path, typed as T. Zero and false on any missing key or type
// mismatch — callers decide how loud to be about that.
func jsonPath[T any](m map[string]any, path ...string) (T, bool) {
	var zero T
	cur := any(m)
	for i, p := range path {
		mp, ok := cur.(map[string]any)
		if !ok {
			return zero, false
		}
		v, ok := mp[p]
		if !ok {
			return zero, false
		}
		if i == len(path)-1 {
			out, ok := v.(T)
			return out, ok
		}
		cur = v
	}
	return zero, false
}

// freeTCPPort asks the kernel to pick an unused port by binding to :0
// then closing. There's a small race between close and the docker
// container acquiring the port, but for a hand-run test that's fine —
// the container will fail fast if the port becomes unavailable.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("listener address is not TCP")
	}
	return addr.Port, nil
}

// randHex returns n bytes of hex-encoded randomness for suffixing
// container names. Uses crypto/rand so parallel runs on the same host
// don't collide.
func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random name suffix: %v", err)
	}
	return hex.EncodeToString(b)
}

// abs resolves a testdata path against the test's working directory
// (the package's source dir when `go test` runs). Kept as a helper so
// the intent is obvious at the call site.
func abs(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, rel), nil
}
