package components

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ghiac/agentize/model"
)

// assertCleanUTF8 fails if s is not valid UTF-8 or contains the U+FFFD
// replacement glyph ("�"). A byte-index truncation of multibyte text
// (Persian/Arabic/emoji) cuts mid-rune, leaving invalid UTF-8 that renders
// as "�" at the cut point — these checks catch exactly that regression.
func assertCleanUTF8(t *testing.T, label, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Errorf("%s: output is not valid UTF-8 (truncation split a multibyte rune): %q", label, s)
	}
	if strings.ContainsRune(s, '�') {
		t.Errorf("%s: output contains the U+FFFD replacement glyph: %q", label, s)
	}
}

func TestTruncatedTextRuneSafe(t *testing.T) {
	// 60 Persian runes (2 bytes each) — byte-slicing at 40 would land mid-rune.
	persian := strings.Repeat("ش", 60)
	out := TruncatedText(persian, 40)
	assertCleanUTF8(t, "TruncatedText", out)
	if !strings.HasSuffix(out, "...") {
		t.Errorf("TruncatedText: expected an ellipsis on truncated text, got %q", out)
	}
}

func TestTruncatedLinkRuneSafe(t *testing.T) {
	persian := strings.Repeat("ع", 60)
	out := TruncatedLink(persian, "/agentize/debug/users/x", 40)
	assertCleanUTF8(t, "TruncatedLink", out)
}

func TestExpandableWithPreviewRuneSafe(t *testing.T) {
	// 4-byte emoji runes — the worst case for byte-index slicing.
	emoji := strings.Repeat("😀", 60)
	out := string(ExpandableWithPreview(emoji, 40))
	assertCleanUTF8(t, "ExpandableWithPreview", out)
}

// TestSessionTableRowRuneSafe exercises the originally-reported sites: the
// collapsed-row title truncation (~40) and the expanded-row summary truncation
// (~200), both with Persian content long enough to force truncation.
func TestSessionTableRowRuneSafe(t *testing.T) {
	session := &model.Session{
		AgentType: model.AgentTypeCore,
		Title:     strings.Repeat("ش", 60),     // > 40 runes → title truncates
		Summary:   strings.Repeat("سلام ", 80), // 400 runes → summary truncates
	}
	out := SessionTableRow(session, DefaultSessionRowConfig(), 0)
	assertCleanUTF8(t, "SessionTableRow", out)
}
