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
		if got := New(); got == nil || got.backend == nil {
			t.Fatalf("New() returned an unusable logger for format %q", format)
		}
	}
}

// recordingBackend captures the level of the last formatted call so tests can
// assert that an injected Backend actually receives the log traffic.
type recordingBackend struct {
	lastLevel  string
	lastFormat string
	calls      int
}

func (r *recordingBackend) Infof(format string, args ...any)  { r.record("info", format) }
func (r *recordingBackend) Warnf(format string, args ...any)  { r.record("warn", format) }
func (r *recordingBackend) Errorf(format string, args ...any) { r.record("error", format) }
func (r *recordingBackend) Debugf(format string, args ...any) { r.record("debug", format) }

func (r *recordingBackend) record(level, format string) {
	r.lastLevel = level
	r.lastFormat = format
	r.calls++
}

func TestSetLoggerRoutesThroughInjectedBackend(t *testing.T) {
	l := New()
	rec := &recordingBackend{}
	l.SetBackend(rec)

	l.Infof("hello %s", "world")
	if rec.calls != 1 || rec.lastLevel != "info" || rec.lastFormat != "hello %s" {
		t.Fatalf("Infof not routed to backend: %+v", rec)
	}
	l.Warnf("warn")
	l.Errorf("err")
	l.Debugf("dbg")
	if rec.calls != 4 || rec.lastLevel != "debug" {
		t.Fatalf("levels not routed to backend: %+v", rec)
	}
}

func TestSetBackendNilResetsToDefault(t *testing.T) {
	l := New()
	l.SetBackend(&recordingBackend{})
	l.SetBackend(nil) // must not panic and must not leave a nil backend
	if l.backend == nil {
		t.Fatal("SetBackend(nil) left a nil backend; logging would be lost")
	}
	l.Infof("still works") // must not panic
}

func TestGlobalSetLoggerSwapsLogBackend(t *testing.T) {
	original := Log.backendOrDefault()
	t.Cleanup(func() { Log.SetBackend(original) })

	rec := &recordingBackend{}
	SetLogger(rec)
	Log.Infof("through global")
	if rec.calls != 1 || rec.lastLevel != "info" {
		t.Fatalf("SetLogger did not redirect global Log: %+v", rec)
	}
}
