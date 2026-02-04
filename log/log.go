package log

import (
	"fmt"
	"log/slog"
	"os"
)

// Logger provides a simple logging interface with formatted output methods
type Logger struct {
	logger *slog.Logger
}

// Log is the global logger instance
var Log = &Logger{
	logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})),
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
