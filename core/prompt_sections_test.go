package core

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

// TestSystemPromptSectionsFor_KeysAndClassification checks that the structured
// view exposes every section with correct identity/classification and that the
// required prefix is byte-exact and included.
func TestSystemPromptSectionsFor_KeysAndClassification(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	ch.coreSessions["user1"] = &model.Session{
		SessionID: "s1",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   "the user likes go",
		Tags:      []string{"golang"},
	}

	sections, err := ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}

	byKey := map[string]model.PromptSection{}
	for _, s := range sections {
		byKey[s.Key] = s
	}

	for _, k := range []string{SectionCoreController, SectionAgents, SectionAgentTools} {
		s, ok := byKey[k]
		if !ok {
			t.Fatalf("missing required section %q", k)
		}
		if !s.Required || s.Dynamic {
			t.Errorf("section %q should be Required+Static", k)
		}
		if !s.Included {
			t.Errorf("required section %q should be Included", k)
		}
	}

	if got := byKey[SectionCoreController]; got.Content != coreControllerPrompt {
		t.Error("core_controller content must equal the embedded controller prompt")
	} else if got.Bytes != len(coreControllerPrompt) {
		t.Errorf("Bytes (%d) must equal len(Content) (%d)", got.Bytes, len(coreControllerPrompt))
	}

	cs, ok := byKey[SectionCoreSessionContext]
	if !ok {
		t.Fatal("missing core_session_context section")
	}
	if cs.Required || !cs.Dynamic {
		t.Error("core_session_context should be Optional+Dynamic")
	}
	if !cs.Included || !strings.Contains(cs.Content, "the user likes go") {
		t.Error("core_session_context should be included and carry the summary")
	}

	if !strings.Contains(byKey[SectionAgents].Content, "researcher") {
		t.Error("agents section should mention the registered agent")
	}
}

// TestAppPolicySection_InjectedWhenConfigured verifies the deployment-policy
// injection hook: when CoreHandlerConfig.AppPolicy is set, it appears as a
// required, static section titled by AppPolicyTitle, positioned in the static
// prefix right after the Core Controller; when empty, no such section exists.
func TestAppPolicySection_InjectedWhenConfigured(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})

	// Empty by default: no app_policy section.
	sections, err := ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}
	for _, s := range sections {
		if s.Key == SectionAppPolicy {
			t.Fatal("app_policy section must be absent when AppPolicy is empty")
		}
	}

	// Configured: section is present, required, static, included, byte-exact, and
	// sits immediately after the Core Controller (so it caches in the static prefix).
	const policy = "DEPLOY-POLICY-XYZ: reply only in Klingon."
	ch.config.AppPolicy = policy
	ch.config.AppPolicyTitle = "Deployment Policy"

	sections, err = ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}

	var idx = -1
	for i, s := range sections {
		if s.Key == SectionAppPolicy {
			idx = i
			if s.Content != policy || s.Bytes != len(policy) {
				t.Errorf("app_policy content/bytes mismatch: %q (%d)", s.Content, s.Bytes)
			}
			if !s.Required || s.Dynamic || !s.Included {
				t.Error("app_policy must be Required+Static+Included")
			}
			if s.Title != "Deployment Policy" {
				t.Errorf("app_policy title = %q, want %q", s.Title, "Deployment Policy")
			}
		}
	}
	if idx == -1 {
		t.Fatal("app_policy section must be present when AppPolicy is set")
	}
	if sections[0].Key != SectionCoreController || idx != 1 {
		t.Errorf("app_policy must sit right after core_controller; got index %d (section[0]=%s)", idx, sections[0].Key)
	}

	// The live array projects the same assembly, so the policy text must be in it.
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts: %v", err)
	}
	if !strings.Contains(strings.Join(prompts, "\n"), policy) {
		t.Error("live system-prompt array must contain the injected AppPolicy text")
	}
}

// TestSystemPromptSectionsFor_DropsOverBudget mirrors the live-array budget test:
// an over-budget optional section is marked not-included but still exposes its
// content so the UI can show what was cut.
func TestSystemPromptSectionsFor_DropsOverBudget(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	huge := strings.Repeat("x", 5000)
	ch.coreSessions["user1"] = &model.Session{
		SessionID: "s1",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   huge,
	}
	required := len(coreControllerPrompt) +
		len(ch.agents.BuildAgentsDescriptionPrompt()) +
		len(ch.agents.BuildAgentToolsPrompt())
	ch.config.MaxSystemPromptSize = required + 1000 // room for small sections, not the 5000-char summary

	sections, err := ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}

	var ctx *model.PromptSection
	for i := range sections {
		if sections[i].Key == SectionCoreSessionContext {
			ctx = &sections[i]
		}
	}
	if ctx == nil {
		t.Fatal("core_session_context section missing")
	}
	if ctx.Included {
		t.Error("over-budget core_session_context must be marked not included")
	}
	if ctx.Content == "" {
		t.Error("a dropped section should still expose its content for display")
	}

	// And the live array (which projects the same assembly) must not contain it.
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts: %v", err)
	}
	if strings.Contains(strings.Join(prompts, " "), huge[:100]) {
		t.Error("the live array must drop the over-budget section too")
	}
}

// TestPreviewSystemPromptSections_FromStore checks the store-only preview: the
// real controller and the user's stored Core session surface, while
// agent-dependent sections are empty and annotated (no registered agents).
func TestPreviewSystemPromptSections_FromStore(t *testing.T) {
	_, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	if err := sqliteStore.Put(&model.Session{
		SessionID: "core-s",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   "stored summary",
		Tags:      []string{"topic"},
	}); err != nil {
		t.Fatalf("seed core session: %v", err)
	}

	sections, err := PreviewSystemPromptSections(sqliteStore, "user1", CoreHandlerConfig{})
	if err != nil {
		t.Fatalf("PreviewSystemPromptSections: %v", err)
	}

	byKey := map[string]model.PromptSection{}
	for _, s := range sections {
		byKey[s.Key] = s
	}

	if byKey[SectionCoreController].Content != coreControllerPrompt {
		t.Error("preview must include the real embedded controller prompt")
	}
	if !strings.Contains(byKey[SectionCoreSessionContext].Content, "stored summary") {
		t.Error("preview should surface the stored Core session summary")
	}

	agentCtx := byKey[SectionAgentSessionContexts]
	if agentCtx.Content != "" {
		t.Error("agent session contexts should be empty in a store-only preview")
	}
	if agentCtx.Note == "" {
		t.Error("an empty agent-dependent section should carry a Note in preview mode")
	}
}
