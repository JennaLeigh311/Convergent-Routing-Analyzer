package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(test1 *testing.T) {
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
	for _, testCase := range tests {
		test1.Run(testCase.in, func(test2 *testing.T) {
			if got := parseLevel(testCase.in); got != testCase.want {
				test2.Errorf("parseLevel(%q) = %v, want %v", testCase.in, got, testCase.want)
			}
		})
	}
}

func TestParseFormat(test1 *testing.T) {
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
	for _, testCase := range tests {
		test1.Run(testCase.in, func(test2 *testing.T) {
			if got := parseFormat(testCase.in); got != testCase.want {
				test2.Errorf("parseFormat(%q) = %q, want %q", testCase.in, got, testCase.want)
			}
		})
	}
}

func TestFromEnv(test *testing.T) {
	test.Setenv("LOG_LEVEL", "warn")
	test.Setenv("LOG_FORMAT", "json")

	cfg := FromEnv()
	if cfg.Level != slog.LevelWarn {
		test.Errorf("Level = %v, want %v", cfg.Level, slog.LevelWarn)
	}
	if cfg.Format != FormatJSON {
		test.Errorf("Format = %q, want %q", cfg.Format, FormatJSON)
	}
}

func TestNewTextHandlerWritesStructured(test *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Format: FormatText, Writer: &buf})

	logger.Info("starting", "component", "routing-server")

	out := buf.String()
	if !strings.Contains(out, "msg=starting") {
		test.Errorf("text output missing message: %q", out)
	}
	if !strings.Contains(out, "component=routing-server") {
		test.Errorf("text output missing structured field: %q", out)
	}
}

func TestNewJSONHandlerWritesStructured(test *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Format: FormatJSON, Writer: &buf})

	logger.Info("starting", "component", "benchmark")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		test.Fatalf("JSON output is not valid JSON: %v\n%s", err, buf.String())
	}
	if rec["msg"] != "starting" {
		test.Errorf("msg = %v, want starting", rec["msg"])
	}
	if rec["component"] != "benchmark" {
		test.Errorf("component = %v, want benchmark", rec["component"])
	}
}

func TestLevelFiltering(test *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Writer: &buf})

	logger.Debug("suppressed below threshold")
	if buf.Len() != 0 {
		test.Errorf("debug log emitted at info level: %q", buf.String())
	}

	logger.Warn("kept at or above threshold")
	if !strings.Contains(buf.String(), "kept at or above threshold") {
		test.Errorf("warn log dropped at info level: %q", buf.String())
	}
}

func TestNewZeroConfigIsUsable(test *testing.T) {
	// A zero Config must not panic and must produce a usable logger
	// (info-level text to stderr). We assert it is non-nil without emitting,
	// to avoid writing to the real stderr during the test run.
	if logger := New(Config{}); logger == nil {
		test.Fatal("New(Config{}) returned nil")
	}
}

func TestSetupConfiguresDefaultFromEnv(test *testing.T) {
	// Setup mutates slog's process-global default; restore it afterward.
	// (t.Setenv already forbids t.Parallel here.)
	prev := slog.Default()
	test.Cleanup(func() { slog.SetDefault(prev) })

	test.Setenv("LOG_LEVEL", "error")
	test.Setenv("LOG_FORMAT", "json")

	logger := Setup()
	if logger == nil {
		test.Fatal("Setup() returned nil")
	}

	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelInfo) {
		test.Error("info should be disabled when LOG_LEVEL=error")
	}
	if !logger.Enabled(ctx, slog.LevelError) {
		test.Error("error should be enabled when LOG_LEVEL=error")
	}
	if !slog.Default().Enabled(ctx, slog.LevelError) {
		test.Error("Setup did not install the configured logger as the slog default")
	}
}
