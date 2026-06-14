package core

import (
	"fmt"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/planning"
)

// Stable identifiers for the Core's system-prompt sections. They double as the
// labels of the agentize_system_prompt_sections_dropped_total metric, so keep
// them in sync with the values passed to metrics.SystemPromptSectionDropped.
const (
	SectionCoreController       = "core_controller"
	SectionAppPolicy            = "app_policy"
	SectionAgents               = "agents"
	SectionAgentTools           = "agent_tools"
	SectionCoreSessionContext   = "core_session_context"
	SectionAgentSessionContexts = "agent_session_contexts"
	SectionUserFiles            = "user_files"
	SectionActiveSessions       = "active_sessions"
	SectionSessionsList         = "sessions_list"
	SectionPlanning             = "planning"
)

// sectionSpec is the un-budgeted description of one prompt section: its identity
// and freshly-built content. assembleSections turns a slice of these into
// budgeted model.PromptSection values.
type sectionSpec struct {
	key      string
	title    string
	required bool // required sections are never dropped
	dynamic  bool // dynamic sections vary per user/turn; static ones are deployment-stable
	content  string
}

// assembleSections builds every system-prompt section for a user with full
// metadata, applying the same size budget the live array uses: required sections
// are always kept; optional sections that would push the running total past
// MaxSystemPromptSize are marked Included=false (and counted/logged exactly as on
// the hot path). Empty sections are returned too (Included=false) so the debug UI
// can show that a section exists but is currently empty.
//
// This is the single source of truth shared by buildSystemPrompts (the live
// []string the Core sends to its routing LLM) and SystemPromptSectionsFor /
// PreviewSystemPromptSections (the structured debug view), so the two never
// drift. The section order here is the order they are sent to the LLM and the
// order they appear in the UI; the static prefix (controller, agents, agent
// tools) comes first so provider-side prompt caching can reuse it.
func (ch *CoreHandler) assembleSections(userID string, coreSession *model.Session) ([]model.PromptSection, error) {
	coreCtx := ""
	if coreSession != nil {
		coreCtx = ch.buildCoreSessionContext(coreSession)
	}

	specs := []sectionSpec{
		{SectionCoreController, "Core Controller", true, false, coreControllerPrompt},
	}

	// Deployment Policy: optional app-supplied rules (output language, length
	// limits, app-owned capabilities to delegate, billing/quota handling). It sits
	// in the static prefix right after the controller so it caches provider-side
	// and so its rules refine/override the generic controller for this deployment.
	if ap := ch.config.AppPolicy; ap != "" {
		specs = append(specs, sectionSpec{SectionAppPolicy, ch.config.appPolicyTitle(), true, false, ap})
	}

	specs = append(specs,
		sectionSpec{SectionAgents, "Available Agents", true, false, ch.agents.BuildAgentsDescriptionPrompt()},
		sectionSpec{SectionAgentTools, "Registered Agent Tools", true, false, ch.agents.BuildAgentToolsPrompt()},
		sectionSpec{SectionCoreSessionContext, "Core Session Context", false, true, coreCtx},
		sectionSpec{SectionAgentSessionContexts, "Agent Session Contexts", false, true,
			ch.agents.BuildAllSessionContextsPrompt(ch.getSessionFunc(), ch.getActiveSessionIDFunc(), userID)},
		sectionSpec{SectionUserFiles, "User Files", false, true, ch.buildUserFilesPrompt(userID)},
		sectionSpec{SectionActiveSessions, "Active Sessions", false, true,
			ch.agents.BuildActiveSessionsPrompt(ch.getSessionFunc(), ch.getActiveSessionIDFunc(), userID)},
	)

	// Sessions list (for change_session) can fail when the store is unreachable;
	// surface that rather than silently shipping a prompt without it.
	sessionsPrompt, err := ch.sessionHandler.GetSessionsPrompt(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions prompt: %w", err)
	}
	specs = append(specs, sectionSpec{SectionSessionsList, "Sessions List", false, true, sessionsPrompt})

	// Planning: only when an orchestrator is wired, so Core can choose execute_plan.
	if ch.orchestrator != nil {
		specs = append(specs, sectionSpec{SectionPlanning, "Planning", false, false, planning.CorePrompt()})
	}

	limit := ch.config.maxSystemPromptSize()
	used := 0
	out := make([]model.PromptSection, 0, len(specs))
	for _, s := range specs {
		sec := model.PromptSection{
			Key:      s.key,
			Title:    s.title,
			Content:  s.content,
			Required: s.required,
			Dynamic:  s.dynamic,
			Bytes:    len(s.content),
		}
		switch {
		case s.content == "":
			// Empty section: never emitted to the LLM; shown in the UI for context.
			sec.Included = false
		case s.required:
			sec.Included = true
			used += len(s.content)
		case used+len(s.content) > limit:
			// Over budget — drop the optional section so one user's huge history
			// cannot inflate every message's token cost. Counted + logged exactly
			// as the hot path did before this was a structured assembly.
			metrics.SystemPromptSectionDropped(s.key)
			log.Log.Warnf("[CoreHandler] ⚠️  System prompt over budget — dropping section | UserID: %s | Section: %s | SectionLen: %d | Used: %d | Limit: %d",
				userID, s.key, len(s.content), used, limit)
			sec.Included = false
		default:
			sec.Included = true
			used += len(s.content)
		}
		out = append(out, sec)
	}
	return out, nil
}

// currentCoreSession returns the user's cached Core session (may be nil) without
// loading or creating one — the read used by both the live array and the debug
// view so neither has a side effect on the Core's session state.
func (ch *CoreHandler) currentCoreSession(userID string) *model.Session {
	ch.coreSessionsMu.RLock()
	defer ch.coreSessionsMu.RUnlock()
	return ch.coreSessions[userID]
}

// SystemPromptSectionsFor returns the live, fully-assembled system-prompt array
// for a user as labeled sections (including ones dropped by the budget or left
// empty), for display in the debug UI. It is the exact assembly the Core would
// send on this user's next message — wire it to the debug page via
// Agentize.SetCoreSystemPromptProvider:
//
//	ag.SetCoreSystemPromptProvider(coreHandler.SystemPromptSectionsFor)
func (ch *CoreHandler) SystemPromptSectionsFor(userID string) ([]model.PromptSection, error) {
	return ch.assembleSections(userID, ch.currentCoreSession(userID))
}

// PreviewSystemPromptSections reconstructs the Core's system-prompt array for a
// user directly from a store, without a running CoreHandler. It is the default
// the debug page uses when no live Core is wired (Agentize installs it in
// createDebugHandler): the controller rules and the user's own memory, files and
// sessions list are real, while agent-dependent sections come back empty (there
// are no registered agents) and are annotated via Note.
//
// It builds a display-only handler exactly as the engine wires a real one
// (model.NewSessionHandler over the store + an empty agentmanager), loads the
// user's Core session read-only (never creating one), and runs the same
// assembleSections, so the preview cannot drift from the live builder.
func PreviewSystemPromptSections(store model.SessionStore, userID string, cfg CoreHandlerConfig) ([]model.PromptSection, error) {
	sh := model.NewSessionHandler(store, model.SessionHandlerConfig{DisableLogs: true})
	am := agentmanager.New(sh)
	ch := NewCoreHandler(sh, am, cfg)

	var coreSession *model.Session
	if getter, ok := store.(interface {
		GetCoreSession(string) (*model.Session, error)
	}); ok {
		if cs, err := getter.GetCoreSession(userID); err == nil {
			coreSession = cs
		}
	}

	sections, err := ch.assembleSections(userID, coreSession)
	if err != nil {
		return nil, err
	}
	for i := range sections {
		if sections[i].Content == "" && isAgentDependentSection(sections[i].Key) {
			sections[i].Note = "Available with a live Core (registered agents)"
		}
	}
	return sections, nil
}

// isAgentDependentSection reports whether a section's content comes from the
// registered AgentManager, and so is empty in a store-only preview.
func isAgentDependentSection(key string) bool {
	switch key {
	case SectionAgents, SectionAgentTools, SectionAgentSessionContexts, SectionActiveSessions:
		return true
	default:
		return false
	}
}
