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

	if err := contextagent.Run(context.Background(), cfg, logger, version); err != nil {
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
