package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"  info ", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},         // default
		{"nonsense", slog.LevelInfo}, // default on unrecognized
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseLevel(tt.in); got != tt.want {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		in   string
		want Format
	}{
		{"json", FormatJSON},
		{"JSON", FormatJSON},
		{"text", FormatText},
		{"", FormatText},     // default
		{"yaml", FormatText}, // default on unrecognized
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseFormat(tt.in); got != tt.want {
				t.Errorf("parseFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("LOG_FORMAT", "json")

	cfg := FromEnv()
	if cfg.Level != slog.LevelWarn {
		t.Errorf("Level = %v, want %v", cfg.Level, slog.LevelWarn)
	}
	if cfg.Format != FormatJSON {
		t.Errorf("Format = %q, want %q", cfg.Format, FormatJSON)
	}
}

func TestNewTextHandlerWritesStructured(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Format: FormatText, Writer: &buf})

	logger.Info("starting", "component", "routing-server")

	out := buf.String()
	if !strings.Contains(out, "msg=starting") {
		t.Errorf("text output missing message: %q", out)
	}
	if !strings.Contains(out, "component=routing-server") {
		t.Errorf("text output missing structured field: %q", out)
	}
}

func TestNewJSONHandlerWritesStructured(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Format: FormatJSON, Writer: &buf})

	logger.Info("starting", "component", "benchmark")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\n%s", err, buf.String())
	}
	if rec["msg"] != "starting" {
		t.Errorf("msg = %v, want starting", rec["msg"])
	}
	if rec["component"] != "benchmark" {
		t.Errorf("component = %v, want benchmark", rec["component"])
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Writer: &buf})

	logger.Debug("suppressed below threshold")
	if buf.Len() != 0 {
		t.Errorf("debug log emitted at info level: %q", buf.String())
	}

	logger.Warn("kept at or above threshold")
	if !strings.Contains(buf.String(), "kept at or above threshold") {
		t.Errorf("warn log dropped at info level: %q", buf.String())
	}
}

func TestNewZeroConfigIsUsable(t *testing.T) {
	// A zero Config must not panic and must produce a usable logger
	// (info-level text to stderr). We assert it is non-nil without emitting,
	// to avoid writing to the real stderr during the test run.
	if logger := New(Config{}); logger == nil {
		t.Fatal("New(Config{}) returned nil")
	}
}

func TestSetupConfiguresDefaultFromEnv(t *testing.T) {
	// Setup mutates slog's process-global default; restore it afterward.
	// (t.Setenv already forbids t.Parallel here.)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("LOG_FORMAT", "json")

	logger := Setup()
	if logger == nil {
		t.Fatal("Setup() returned nil")
	}

	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelInfo) {
		t.Error("info should be disabled when LOG_LEVEL=error")
	}
	if !logger.Enabled(ctx, slog.LevelError) {
		t.Error("error should be enabled when LOG_LEVEL=error")
	}
	if !slog.Default().Enabled(ctx, slog.LevelError) {
		t.Error("Setup did not install the configured logger as the slog default")
	}
}
