package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/identityagent"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig/scope3"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

var version = "dev"

func main() {
	addr := flag.String("addr", "", "Listen address")
	registryURL := flag.String("registry-url", "", "URL of the router's /registry/snapshot endpoint for signing-key discovery")
	allowUnsigned := flag.Bool("allow-unsigned", false, "Accept /identity requests without a TMP signature. Default is deny — TMP signing is normative in the spec. Use only for migration windows or local dev.")
	ownEndpointURL := flag.String("own-endpoint-url", "", "This provider's registered endpoint URL (must match the router's provider registration). Required when --registry-url is set.")
	tmpxEncryptJWKSURL := flag.String("tmpx-encrypt-jwks-url", "", "URL of the buyer's JWKS endpoint that publishes the active TMPX recipient key (X25519, adcp_use=tmpx-encrypt). Enables TMPX token generation when set.")
	tmpxEncryptJWKSTTL := flag.Duration("tmpx-encrypt-jwks-ttl", 5*time.Minute, "How often to re-poll the TMPX encryption JWKS for key rotation.")
	tmpxCountry := flag.String("tmpx-country", "", "ISO 3166-1 alpha-2 country code stamped into the TMPX header. Required when TMPX is enabled.")
	tmpxPriority := flag.String("tmpx-priority", "", "Comma-separated UID type ordering used to truncate identities when the TMPX wire size would exceed 255 bytes (e.g. 'uid2,rampid,id5'). Spec requires this list be configured before any truncation; without it, an over-budget identity set returns an error.")
	configSourceURL := flag.String("config-source-url", "", "URL of the Scope3 identity-config endpoint. When set, the agent loads PackageIdentityConfig entries from this URL keyed by (seller_agent_url, package_id) and refreshes them periodically.")
	configSourceToken := flag.String("config-source-token", "", "Bearer token sent as Authorization on identity-config requests. Required when --config-source-url is set.")
	configSourceTimeout := flag.Duration("config-source-timeout", 30*time.Second, "Total HTTP timeout for each identity-config request.")
	configRefreshInterval := flag.Duration("config-refresh-interval", 5*time.Minute, "Interval between identity-config delta refreshes.")
	flag.Parse()

	flagSet := setFlags()

	listenAddr := resolveAddr(*addr)
	regURL := resolveString(*registryURL, flagSet["registry-url"], "TMP_IDENTITY_REGISTRY_URL")
	ownURL := resolveString(*ownEndpointURL, flagSet["own-endpoint-url"], "TMP_IDENTITY_ENDPOINT_URL")
	if !flagSet["allow-unsigned"] {
		if envValue, ok := os.LookupEnv("TMP_IDENTITY_ALLOW_UNSIGNED"); ok {
			*allowUnsigned = envValue == "1" || envValue == "true"
		}
	}
	requireSig := !*allowUnsigned

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	metrics := prommetrics.New()

	cfgURL := resolveString(*configSourceURL, flagSet["config-source-url"], "TMP_IDENTITY_CONFIG_SOURCE_URL")
	cfgToken := resolveString(*configSourceToken, flagSet["config-source-token"], "TMP_IDENTITY_CONFIG_SOURCE_TOKEN")
	if cfgURL == "" {
		slog.Error("--config-source-url is required (Scope3 identity-config endpoint)")
		os.Exit(1)
	}
	if cfgToken == "" {
		slog.Error("--config-source-token is required when --config-source-url is set")
		os.Exit(1)
	}
	cfgSource, err := scope3.New(cfgURL, cfgToken, scope3.WithHTTPTimeout(*configSourceTimeout))
	if err != nil {
		slog.Error("config source init failed", "error", err)
		os.Exit(1)
	}
	configSvc, err := identityconfig.New(cfgSource, *configRefreshInterval,
		identityconfig.WithStartConfig(identityconfig.StartConfig{
			Mode: identityconfig.StartModeRetry,
			Retry: identityconfig.RetryConfig{
				Initial:  time.Second,
				Max:      30 * time.Second,
				Backoff:  identityconfig.BackoffExponential,
				Deadline: 5 * time.Minute,
			},
		}),
		identityconfig.WithLogger(slog.Default()),
	)
	if err != nil {
		slog.Error("config service init failed", "error", err)
		os.Exit(1)
	}
	if err := configSvc.Start(context.Background()); err != nil {
		slog.Error("config service initial load failed", "error", err)
		os.Exit(1)
	}
	defer configSvc.Stop()

	engine := targeting.NewIdentityEngine(targeting.IdentityEngineConfig{
		Metrics: metrics,
	})

	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	keystore, ksErr := identityagent.BuildKeyStore(bgCtx, regURL, requireSig, slog.Default(), nil)
	if ksErr != nil {
		slog.Error("keystore init failed", "error", ksErr)
		os.Exit(1)
	}
	if requireSig && ownURL == "" {
		slog.Error("--own-endpoint-url is required when signature verification is enabled (default)")
		os.Exit(1)
	}
	if !requireSig {
		slog.Warn("/identity accepts unsigned requests — TMP signing should be required in production")
	}

	// The reference agent preserves the TMP_IDENTITY_TMPX_REFERENCE_STUB_ACK
	// env name for backwards compatibility; the library accepts the ack as
	// a struct field, so translate at the boundary.
	ack := os.Getenv("TMP_IDENTITY_TMPX_REFERENCE_STUB_ACK")
	// The reference agent does not wire a LiveRamp sidecar; RampID and
	// RampID-derived identities are silently dropped from the TMPX wire.
	// Pass a nil interface — not a typed-nil pointer — so the sealer's
	// nil check sees genuine absence rather than the typed-nil-trap.
	var lrSidecar identityagent.LiveRampSidecar
	tmpxSealer, err := identityagent.NewTMPXSealer(bgCtx, identityagent.TMPXConfig{
		EncryptJWKSURL:   resolveString(*tmpxEncryptJWKSURL, flagSet["tmpx-encrypt-jwks-url"], "TMP_IDENTITY_TMPX_ENCRYPT_JWKS_URL"),
		EncryptJWKSTTL:   *tmpxEncryptJWKSTTL,
		Country:          resolveString(*tmpxCountry, flagSet["tmpx-country"], "TMP_IDENTITY_TMPX_COUNTRY"),
		Priority:         resolveString(*tmpxPriority, flagSet["tmpx-priority"], "TMP_IDENTITY_TMPX_PRIORITY"),
		ReferenceStubAck: ack == "1" || ack == "true",
	}, lrSidecar, slog.Default(), nil)
	if err != nil {
		slog.Error("tmpx config load failed", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()

	identityHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Type: tmproto.TypeError, Code: tmproto.ErrorCodeInvalidRequest, Message: "failed to read request body"})
			return
		}
		var req tmproto.IdentityMatchRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Type: tmproto.TypeError, Code: tmproto.ErrorCodeInvalidRequest, Message: "request body is not valid JSON"})
			return
		}
		if err := tmproto.ValidateIdentityRequest(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Type: tmproto.TypeError, RequestID: req.RequestID, Code: tmproto.ErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		effectivePkgIDs, idConfigs := identityconfig.ResolveRequest(configSvc, req.SellerAgentURL, req.PackageIDs)
		req.PackageIDs = effectivePkgIDs
		resolved := &targeting.ResolvedPackages{IdentityConfigs: idConfigs}
		result, err := engine.EvaluateIdentityResolved(r.Context(), resolved, &req)
		if err != nil {
			slog.Error("EvaluateIdentityResolved failed", "request_id", req.RequestID, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Type: tmproto.TypeError, RequestID: req.RequestID, Code: tmproto.ErrorCodeInternalError, Message: "internal error"})
			return
		}
		eligible := make([]string, 0, len(result.Eligibility))
		for _, e := range result.Eligibility {
			if e.Eligible {
				eligible = append(eligible, e.PackageID)
			}
		}
		resp := &tmproto.IdentityMatchResponse{
			Type:               tmproto.TypeIdentityMatchResponse,
			RequestID:          result.RequestID,
			EligiblePackageIDs: eligible,
			TTLSec:             60,
		}
		if tmpxSealer != nil && len(eligible) > 0 {
			if token, terr := tmpxSealer.Seal(r.Context(), req.Identities); terr != nil {
				slog.Warn("tmpx generation failed, response will omit tmpx", "request_id", req.RequestID, "error", terr)
			} else {
				resp.Tmpx = token
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		slog.Debug("identity match", "request_id", req.RequestID, "packages", len(req.PackageIDs), "latency_ms", time.Since(start).Milliseconds())
	})

	// Wrap with TMP signature verification when configured. Without a
	// keystore, signed requests still pass through unverified — operators
	// who care about authenticated fan-outs MUST set --registry-url and
	// --require-signature (or TMP_IDENTITY_REQUIRE_SIGNATURE=1).
	if keystore != nil {
		mux.Handle("POST /identity", tmproto.VerifyIdentityMatchHandler(identityHandler, tmproto.VerifyOptions{
			KeyStore:         keystore,
			OwnEndpointURL:   ownURL,
			RequireSignature: requireSig,
		}))
	} else {
		mux.Handle("POST /identity", identityHandler)
	}

	mux.Handle("GET /metrics", metrics.Registry.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	slog.Info("Identity Agent starting", "addr", listenAddr, "version", version)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("listen error", "error", err)
		os.Exit(1)
	}
}

func resolveAddr(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("TMP_IDENTITY_ADDR"); env != "" {
		return env
	}
	return ":8082"
}

// resolveString picks the configured value for a string flag with the
// precedence flag > env > default. flagSet says whether the flag was passed
// on the command line; only when it wasn't may an env var override.
func resolveString(flagVal string, flagSet bool, envName string) string {
	if flagSet {
		return flagVal
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return flagVal
}

// setFlags returns the set of flag names that were explicitly passed on the
// command line. Used to enforce flag > env > default precedence per AGENTS.md.
func setFlags() map[string]bool {
	out := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { out[f.Name] = true })
	return out
}
