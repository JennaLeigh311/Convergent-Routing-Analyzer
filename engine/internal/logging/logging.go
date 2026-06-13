package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Format selects the log output handler.
type Format string

const (
	// FormatText is dev-friendly key=value output (the default).
	FormatText Format = "text"
	// FormatJSON is machine-readable output for log aggregation.
	FormatJSON Format = "json"
)

// Config controls how logs are emitted.
type Config struct {
	// Level is the minimum level to emit. The zero value is slog.LevelInfo.
	Level slog.Level
	// Format selects the handler. The zero value ("") is treated as FormatText.
	Format Format
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
	writer := cfg.Writer
	if writer == nil {
		writer = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: cfg.Level}

	var handler slog.Handler
	switch cfg.Format {
	case FormatJSON:
		handler = slog.NewJSONHandler(writer, opts)
	default:
		handler = slog.NewTextHandler(writer, opts)
	}
	return slog.New(handler)
}

// Setup builds a logger from the environment, installs it as the slog default,
// and returns it for explicit use. Call once at process start.
func Setup() *slog.Logger {
	logger := New(FromEnv())
	slog.SetDefault(logger)
	return logger
}

// parseLevel maps a LOG_LEVEL string to a slog.Level, defaulting to Info.
func parseLevel(text string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(text)) {
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

// parseFormat maps a LOG_FORMAT string to a Format, defaulting to FormatText.
func parseFormat(text string) Format {
	if strings.ToLower(strings.TrimSpace(text)) == "json" {
		return FormatJSON
	}
	return FormatText
}
