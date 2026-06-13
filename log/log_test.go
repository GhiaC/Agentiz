package log

import (
	"log/slog"
	"testing"
)

func TestLevelFromEnv(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		" info ":  slog.LevelInfo,
		"":        slog.LevelInfo,
		"bogus":   slog.LevelInfo, // unrecognized falls back to info
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		t.Setenv("AGENTIZE_LOG_LEVEL", in)
		if got := levelFromEnv(); got != want {
			t.Errorf("AGENTIZE_LOG_LEVEL=%q: got %v, want %v", in, got, want)
		}
	}
}

func TestNewBuildsLoggerForBothFormats(t *testing.T) {
	for _, format := range []string{"json", "text", "", "JSON"} {
		t.Setenv("AGENTIZE_LOG_FORMAT", format)
		if got := New(); got == nil || got.logger == nil {
			t.Fatalf("New() returned an unusable logger for format %q", format)
		}
	}
}
