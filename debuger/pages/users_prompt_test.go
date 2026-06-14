package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

// TestRenderCoreSystemPromptCard_PreviewAndEscaping verifies the card renders as
// collapsed <details>, flags a preview, escapes section content, and badges
// dropped/empty/noted sections.
func TestRenderCoreSystemPromptCard_PreviewAndEscaping(t *testing.T) {
	sections := []model.PromptSection{
		{Key: "core_controller", Title: "Core Controller", Content: "rules <b>x</b>", Required: true, Dynamic: false, Included: true, Bytes: 14},
		{Key: "user_files", Title: "User Files", Content: "", Required: false, Dynamic: true, Included: false, Bytes: 0, Note: "Available with a live Core (registered agents)"},
		{Key: "core_session_context", Title: "Core Session Context", Content: strings.Repeat("y", 100), Required: false, Dynamic: true, Included: false, Bytes: 100},
	}

	html := renderCoreSystemPromptCard(sections, true, nil)

	if !strings.Contains(html, "<details") || !strings.Contains(html, "<summary") {
		t.Error("expected collapsible <details>/<summary> markup")
	}
	if strings.Contains(html, "<details class=\"card collapsible-card mb-4\" open>") {
		t.Error("the card must be collapsed by default (no open attribute)")
	}
	if !strings.Contains(html, "PREVIEW") {
		t.Error("preview mode should render a PREVIEW badge")
	}
	if strings.Contains(html, "rules <b>x</b>") {
		t.Error("section content must be HTML-escaped, never injected raw")
	}
	if !strings.Contains(html, "rules &lt;b&gt;x&lt;/b&gt;") {
		t.Error("expected HTML-escaped section content")
	}
	for _, title := range []string{"Core Controller", "User Files", "Core Session Context"} {
		if !strings.Contains(html, title) {
			t.Errorf("expected section title %q to render", title)
		}
	}
	if !strings.Contains(html, "Dropped") {
		t.Error("an over-budget (present but not included) section should be flagged Dropped")
	}
	if !strings.Contains(html, "Available with a live Core") {
		t.Error("an empty section's Note should render")
	}
}

// TestRenderCoreSystemPromptCard_BuildError surfaces a builder error in the card.
func TestRenderCoreSystemPromptCard_BuildError(t *testing.T) {
	html := renderCoreSystemPromptCard(nil, false, fmt.Errorf("boom"))
	if !strings.Contains(html, "Failed to build the system prompt") || !strings.Contains(html, "boom") {
		t.Error("a build error should surface in the card")
	}
}
