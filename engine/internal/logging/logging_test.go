package logging

import (
	"bytes"
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
		if got := parseLevel(tt.in); got != tt.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"json", "json"},
		{"JSON", "json"},
		{"text", "text"},
		{"", "text"},     // default
		{"yaml", "text"}, // default on unrecognized
	}
	for _, tt := range tests {
		if got := parseFormat(tt.in); got != tt.want {
			t.Errorf("parseFormat(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNewTextHandlerWritesStructured(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: slog.LevelInfo, Format: "text", Writer: &buf})

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
	logger := New(Config{Level: slog.LevelInfo, Format: "json", Writer: &buf})

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

func TestNewDefaultsToStderrWriter(t *testing.T) {
	// A zero Config must not panic and must produce a usable logger.
	logger := New(Config{})
	if logger == nil {
		t.Fatal("New(Config{}) returned nil")
	}
	logger.Info("zero-config logger works")
}
