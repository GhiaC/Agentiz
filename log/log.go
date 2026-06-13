package log

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Logger provides a simple logging interface with formatted output methods
type Logger struct {
	logger *slog.Logger
}

// Log is the global logger instance. It is configured from the environment at
// process start:
//
//	AGENTIZE_LOG_LEVEL  = debug | info | warn | error   (default: info)
//	AGENTIZE_LOG_FORMAT = text | json                   (default: text)
//
// The emoji-prefixed console output (text) stays the dev default; set
// AGENTIZE_LOG_FORMAT=json for structured production logging.
var Log = New()

// New builds a Logger from the AGENTIZE_LOG_LEVEL / AGENTIZE_LOG_FORMAT
// environment variables. Unrecognized values fall back to info / text so a
// typo never silences logging.
func New() *Logger {
	opts := &slog.HandlerOptions{Level: levelFromEnv()}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AGENTIZE_LOG_FORMAT")), "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return &Logger{logger: slog.New(h)}
}

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

// Infof logs an info level message with formatting
func (l *Logger) Infof(format string, args ...any) {
	l.logger.Info(sprintf(format, args...))
}

// Warnf logs a warning level message with formatting
func (l *Logger) Warnf(format string, args ...any) {
	l.logger.Warn(sprintf(format, args...))
}

// Errorf logs an error level message with formatting
func (l *Logger) Errorf(format string, args ...any) {
	l.logger.Error(sprintf(format, args...))
}

// Debugf logs a debug level message with formatting
func (l *Logger) Debugf(format string, args ...any) {
	l.logger.Debug(sprintf(format, args...))
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
