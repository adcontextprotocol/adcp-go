// Command context-agent runs a production TMP context-match service.
// Configuration is supplied entirely via environment variables; see the
// adjacent example.standalone.env file for the full list.
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/adcontextprotocol/adcp-go/targeting/contextagent"
)

var version = "dev"

func main() {
	logger := newLogger(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(logger)

	cfg, err := contextagent.LoadConfigFromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	regCfg, err := loadRegistryConfigFromEnv()
	if err != nil {
		logger.Error("invalid registry configuration", "error", err)
		os.Exit(1)
	}
	if err := regCfg.validate(); err != nil {
		logger.Error("invalid registry configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var opts []contextagent.Option
	var regBundle *registryBundle
	if regCfg.Enabled {
		regBundle, err = buildRegistry(ctx, regCfg, logger)
		if err != nil {
			logger.Error("registry bundle build failed", "error", err)
			os.Exit(1)
		}
		opts = append(opts,
			contextagent.WithPropertyGlobal(regBundle.PropertyBitmap()),
			contextagent.WithLivenessChecks(regBundle.LivenessCheck()),
		)
		defer regBundle.Shutdown()
	}

	if err := contextagent.Run(ctx, cfg, logger, version, opts...); err != nil {
		logger.Error("context agent terminated", "error", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
