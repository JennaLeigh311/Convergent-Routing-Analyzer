// Package logging is the engine's structured-logging foundation: a thin wrapper
// over log/slog so every binary configures logs identically and future
// components emit structured, queryable logs instead of ad-hoc prints.
//
// Typical use at process start:
//
//	logger := logging.Setup()           // configured from the environment
//	logger.Info("starting", "addr", addr)
//
// Setup also installs the logger as the slog default, so packages that log via
// slog.Info/Warn/etc. (or libraries that use the default logger) inherit the
// same configuration.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config controls how logs are emitted.
type Config struct {
	// Level is the minimum level to emit. The zero value is slog.LevelInfo.
	Level slog.Level
	// Format selects the handler: "text" (default, dev-friendly) or "json"
	// (machine-readable, for aggregation).
	Format string
	// Writer is the log destination; nil defaults to os.Stderr.
	Writer io.Writer
}

// FromEnv builds a Config from the environment:
//
//	LOG_LEVEL   debug | info | warn | error   (default: info)
//	LOG_FORMAT  text | json                    (default: text)
//
// Unrecognized or empty values fall back to the defaults, so a missing .env is
// never an error.
func FromEnv() Config {
	return Config{
		Level:  parseLevel(os.Getenv("LOG_LEVEL")),
		Format: parseFormat(os.Getenv("LOG_FORMAT")),
	}
}

// New builds a *slog.Logger from cfg. A zero Config yields info-level text logs
// to stderr.
func New(cfg Config) *slog.Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: cfg.Level}

	var h slog.Handler
	switch cfg.Format {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

// Setup builds a logger from the environment, installs it as the slog default,
// and returns it for explicit use. Call once at process start.
func Setup() *slog.Logger {
	logger := New(FromEnv())
	slog.SetDefault(logger)
	return logger
}

// parseLevel maps a LOG_LEVEL string to a slog.Level, defaulting to Info.
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

// parseFormat normalizes a LOG_FORMAT string, defaulting to "text".
func parseFormat(s string) string {
	if strings.ToLower(strings.TrimSpace(s)) == "json" {
		return "json"
	}
	return "text"
}
