package core

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

// TestBuildSystemPrompts_DropsOptionalSectionsOverBudget verifies that when the
// assembled prompt would exceed MaxSystemPromptSize, optional sections (here the
// Core session context, which carries the summary) are dropped while the required
// sections (controller, agent descriptions, agent tools) always survive.
func TestBuildSystemPrompts_DropsOptionalSectionsOverBudget(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})

	// Give the user a Core session with a huge summary so the optional
	// core_session_context section alone busts the budget.
	hugeSummary := strings.Repeat("x", 5000)
	ch.coreSessions["user1"] = &model.Session{
		SessionID: "s1",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   hugeSummary,
	}

	// First build with a generous budget: the summary must be present.
	ch.config.MaxSystemPromptSize = 1 << 20
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts: %v", err)
	}
	if !strings.Contains(strings.Join(prompts, " "), hugeSummary[:100]) {
		t.Fatal("with a large budget, the session summary section must be included")
	}
	generousCount := len(prompts)

	// Now shrink the budget below what the required sections + summary need.
	required := len(coreControllerPrompt) +
		len(ch.agents.BuildAgentsDescriptionPrompt()) +
		len(ch.agents.BuildAgentToolsPrompt())
	ch.config.MaxSystemPromptSize = required + 1000 // room for small sections, not the 5000-char summary

	prompts, err = ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts (tight budget): %v", err)
	}
	all := strings.Join(prompts, " ")
	if strings.Contains(all, hugeSummary[:100]) {
		t.Error("over-budget optional section (session summary) must be dropped")
	}
	if prompts[0] != coreControllerPrompt {
		t.Error("controller prompt must always be the first required section")
	}
	if !strings.Contains(all, "researcher") {
		t.Error("agent descriptions are required and must survive the budget")
	}
	if len(prompts) >= generousCount {
		t.Errorf("tight budget should produce fewer sections: got %d, generous was %d", len(prompts), generousCount)
	}
}

// TestCoreHandlerConfig_LimitDefaults verifies that zero-valued limits fall back
// to their documented defaults.
func TestCoreHandlerConfig_LimitDefaults(t *testing.T) {
	var zero CoreHandlerConfig
	if got := zero.maxSystemPromptSize(); got != defaultMaxSystemPromptSize {
		t.Errorf("maxSystemPromptSize() = %d, want %d", got, defaultMaxSystemPromptSize)
	}
	if got := zero.maxUserFilesInPrompt(); got != defaultMaxUserFilesInPrompt {
		t.Errorf("maxUserFilesInPrompt() = %d, want %d", got, defaultMaxUserFilesInPrompt)
	}
	if got := zero.maxLLMIterations(); got != defaultMaxLLMIterations {
		t.Errorf("maxLLMIterations() = %d, want %d", got, defaultMaxLLMIterations)
	}

	custom := CoreHandlerConfig{MaxSystemPromptSize: 7, MaxUserFilesInPrompt: 8, MaxLLMIterations: 9}
	if custom.maxSystemPromptSize() != 7 || custom.maxUserFilesInPrompt() != 8 || custom.maxLLMIterations() != 9 {
		t.Error("explicit limits must override the defaults")
	}
}
