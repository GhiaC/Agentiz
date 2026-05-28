package debuger

import (
	"fmt"
	"strings"
	"time"
)

// FormatDurationMs formats duration in milliseconds for display (e.g. "123 ms", "1.5 s").
func FormatDurationMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.2f s", float64(ms)/1000)
}

// FormatChars formats a character count for display (e.g. "1,234" or "0").
func FormatChars(n int) string {
	if n <= 0 {
		return "0"
	}
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// FormatTimeAgo formats time as "X ago" (e.g. "5m ago", "2h ago").
func FormatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	duration := time.Since(t)
	if duration < time.Minute {
		return fmt.Sprintf("%ds ago", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm ago", int(duration.Minutes()))
	}
	if duration < 24*time.Hour {
		return fmt.Sprintf("%.1fh ago", duration.Hours())
	}
	return fmt.Sprintf("%dd ago", int(duration.Hours()/24))
}

// FormatDateTime formats time for display (YYYY-MM-DD HH:MM:SS).
func FormatDateTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// FormatTime is an alias for FormatDateTime for display of absolute time.
func FormatTime(t time.Time) string {
	return FormatDateTime(t)
}

// FormatDuration formats a time as relative duration (e.g. "5m ago"). Alias for FormatTimeAgo.
func FormatDuration(t time.Time) string {
	return FormatTimeAgo(t)
}

// GetModelDisplay returns a short display name for an LLM model (e.g. "gpt-4o" from "openai/gpt-4o").
func GetModelDisplay(model string) string {
	if model == "" {
		return "-"
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx+1 < len(model) {
		return model[idx+1:]
	}
	return model
}

// TruncateString truncates s to max runes and appends "..." if truncated.
func TruncateString(s string, max int) string {
	if max <= 0 || len(s) == 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// FormatDurationValue formats a time.Duration for display (e.g. "5m0s", "1h30m").
func FormatDurationValue(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	return d.String()
}

// Min returns the smaller of a and b.
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
