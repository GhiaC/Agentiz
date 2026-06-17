package log

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Backend is the pluggable destination for the package logger. Anything that
// provides these printf-style methods — for example a logrus *Logger or any
// host-application logger — can be injected via SetLogger so Agentize's logs
// flow through the host's logging pipeline (its levels, formatting and sinks)
// instead of Agentize's own stdout slog output.
type Backend interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
	Debugf(format string, args ...any)
}

// Logger is the package logger. It delegates to a swappable Backend so the
// destination can be replaced at runtime via SetLogger/SetBackend without
// touching the hundreds of log.Log.* call sites across the codebase.
type Logger struct {
	mu      sync.RWMutex
	backend Backend
}

// Log is the global logger instance. By default it writes to stdout via slog,
// configured from the environment at process start:
//
//	AGENTIZE_LOG_LEVEL  = debug | info | warn | error   (default: info)
//	AGENTIZE_LOG_FORMAT = text | json                   (default: text)
//
// A host application embedding Agentize can redirect all of its logging into
// the host's own logger by calling SetLogger once at startup, e.g.:
//
//	log.SetLogger(myLogrusLogger) // a *logrus.Logger satisfies Backend
var Log = New()

// New builds a Logger backed by the default slog sink (see newSlogBackend and
// the AGENTIZE_LOG_LEVEL / AGENTIZE_LOG_FORMAT environment variables).
func New() *Logger {
	return &Logger{backend: newSlogBackend()}
}

// SetLogger redirects the global Log to b. Passing nil resets Log to the
// default slog backend. Safe for concurrent use; call it once early at startup,
// before logging traffic begins, for the cleanest behaviour.
func SetLogger(b Backend) {
	Log.SetBackend(b)
}

// SetBackend swaps this logger's backend. A nil backend resets to the default
// slog sink so logging is never silently disabled. Safe for concurrent use.
func (l *Logger) SetBackend(b Backend) {
	if b == nil {
		b = newSlogBackend()
	}
	l.mu.Lock()
	l.backend = b
	l.mu.Unlock()
}

// backendOrDefault returns the current backend, falling back to a fresh slog
// sink if (defensively) none is set.
func (l *Logger) backendOrDefault() Backend {
	l.mu.RLock()
	b := l.backend
	l.mu.RUnlock()
	if b == nil {
		return newSlogBackend()
	}
	return b
}

// Infof logs an info level message with formatting.
func (l *Logger) Infof(format string, args ...any) { l.backendOrDefault().Infof(format, args...) }

// Warnf logs a warning level message with formatting.
func (l *Logger) Warnf(format string, args ...any) { l.backendOrDefault().Warnf(format, args...) }

// Errorf logs an error level message with formatting.
func (l *Logger) Errorf(format string, args ...any) { l.backendOrDefault().Errorf(format, args...) }

// Debugf logs a debug level message with formatting.
func (l *Logger) Debugf(format string, args ...any) { l.backendOrDefault().Debugf(format, args...) }

// slogBackend is the default Backend: a stdlib slog logger writing to stdout,
// configured from the environment. It preserves the behaviour Agentize had
// before the Backend seam was introduced.
type slogBackend struct {
	logger *slog.Logger
}

// newSlogBackend builds a slogBackend from the AGENTIZE_LOG_LEVEL /
// AGENTIZE_LOG_FORMAT environment variables. Unrecognized values fall back to
// info / text so a typo never silences logging.
func newSlogBackend() *slogBackend {
	opts := &slog.HandlerOptions{Level: levelFromEnv()}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AGENTIZE_LOG_FORMAT")), "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return &slogBackend{logger: slog.New(h)}
}

func (s *slogBackend) Infof(format string, args ...any)  { s.logger.Info(sprintf(format, args...)) }
func (s *slogBackend) Warnf(format string, args ...any)  { s.logger.Warn(sprintf(format, args...)) }
func (s *slogBackend) Errorf(format string, args ...any) { s.logger.Error(sprintf(format, args...)) }
func (s *slogBackend) Debugf(format string, args ...any) { s.logger.Debug(sprintf(format, args...)) }

// levelFromEnv maps AGENTIZE_LOG_LEVEL to an slog.Level (default LevelInfo).
func levelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTIZE_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default: // "info" and any unrecognized value
		return slog.LevelInfo
	}
}

// sprintf is a helper function to format strings
func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return formatString(format, args...)
}

// formatString formats a string with the given arguments
func formatString(format string, args ...any) string {
	// Use fmt.Sprintf for formatting
	return fmt.Sprintf(format, args...)
}
