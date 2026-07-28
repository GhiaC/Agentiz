package pages

// truncateRunes shortens s to at most n runes, appending "…" when truncated.
// It is rune-safe so multibyte text (including Persian) is never split.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
