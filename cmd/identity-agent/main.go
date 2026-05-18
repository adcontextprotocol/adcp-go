// Command identity-agent runs a production TMP identity-match service.
// Configuration is supplied entirely via environment variables; see the
// adjacent example.{standalone,cluster,shadow}.env files for the full list.
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/adcontextprotocol/adcp-go/targeting/identityagent"
)

var version = "dev"

func main() {
	logger := newLogger(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(logger)

	cfg, err := identityagent.LoadConfigFromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	if err := identityagent.Run(context.Background(), cfg, logger, version); err != nil {
		logger.Error("identity agent terminated", "error", err)
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
