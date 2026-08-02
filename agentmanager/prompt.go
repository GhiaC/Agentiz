package agentmanager

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ---------------------------------------------------------------------------
// Session-related function signatures passed from the caller (core package)
// to avoid circular imports.
// ---------------------------------------------------------------------------

// SessionGetter retrieves a session by ID.
type SessionGetter func(sessionID string) (*model.Session, error)

// ActiveSessionIDGetter returns the active session ID for a user and agent type.
type ActiveSessionIDGetter func(userID string, agentType model.AgentType) string

// ---------------------------------------------------------------------------
// Agent Description Prompt
// ---------------------------------------------------------------------------

// BuildAgentsDescriptionPrompt generates a system prompt section that describes
// all registered agents, their capabilities, cost tiers, and knowledge domains.
// This replaces the hardcoded UserAgent table in core_controller.md.
func (am *AgentManager) BuildAgentsDescriptionPrompt() string {
	agents := am.GetAll()
	if len(agents) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Agents\n\n")
	sb.WriteString("| Agent | Description | Cost | Capabilities |\n")
	sb.WriteString("|-------|-------------|------|--------------|\n")

	for _, a := range agents {
		caps := "-"
		if len(a.Config.Capabilities) > 0 {
			caps = strings.Join(a.Config.Capabilities, ", ")
		}
		displayName := a.Config.Name
		if a.Config.DisplayName != "" {
			displayName = fmt.Sprintf("%s (%s)", a.Config.Name, a.Config.DisplayName)
		}
		sb.WriteString(fmt.Sprintf("| **%s** | %s | %s | %s |\n",
			displayName, a.Config.Description, a.Config.CostTier, caps))
	}

	// Knowledge domains section (only if any agent has knowledge sets)
	hasKnowledge := false
	for _, a := range agents {
		if len(a.Config.Knowledge) > 0 {
			hasKnowledge = true
			break
		}
	}
	if hasKnowledge {
		sb.WriteString("\n### Knowledge Domains\n")
		for _, a := range agents {
			if len(a.Config.Knowledge) == 0 {
				continue
			}
			var parts []string
			for _, k := range a.Config.Knowledge {
				entry := k.Name
				if k.Model != "" {
					entry += fmt.Sprintf(" (model: %s)", k.Model)
				}
				parts = append(parts, entry)
			}
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", a.Config.Name, strings.Join(parts, ", ")))
		}
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// Agent Tools Prompt
// ---------------------------------------------------------------------------

// BuildAgentToolsPrompt generates a system prompt section listing all tools
// registered across all agent engines. This helps the Core LLM understand what
// capabilities agents have so it can route requests accurately.
func (am *AgentManager) BuildAgentToolsPrompt() string {
	agents := am.GetAll()
	toolSet := make(map[string]bool)

	for _, a := range agents {
		if a.Engine != nil && a.Engine.Functions != nil {
			for _, name := range a.Engine.Functions.GetAllRegistered() {
				toolSet[name] = true
			}
		}
	}

	if len(toolSet) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Registered Agent Tools\n\n")
	sb.WriteString("The following tools are available to agents.\n")
	sb.WriteString("When a user's request requires any of these tools, delegate to the appropriate agent.\n\n")
	names := make([]string, 0, len(toolSet))
	for name := range toolSet {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sb.WriteString(fmt.Sprintf("- `%s`", name))
		if description := agentToolPromptDescription(name); description != "" {
			sb.WriteString(": ")
			sb.WriteString(description)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// agentToolPromptDescription supplies operational guidance for agent tools whose
// capability cannot be inferred from the name alone. The Core sees this prompt
// while deciding which agent should handle a request; it does not invoke the
// agent's tools itself.
func agentToolPromptDescription(name string) string {
	switch name {
	case "browser_use":
		return "Autonomous browser work: navigate, search, extract content, interact with pages and forms, " +
			"upload session files, download files, and capture screenshots. Delegate web research, website testing, " +
			"login/form workflows, or browser-based data extraction to an agent with this tool. The agent starts work " +
			"with `action=run` and a precise `task`; it can then use the returned job ID with `status`, `screenshot`, " +
			"`downloads`, `download`, or `cancel`. The browser session and open tabs persist between runs; use " +
			"`tabs` for a current tab snapshot and `close_tab` with a returned `tab_id` to close one. Use `file_ids` " +
			"on `run` when user files must be uploaded to a site."
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Dynamic call_agent_* Tools
// ---------------------------------------------------------------------------

// BuildCallTools dynamically generates one `call_agent_{name}` tool per
// registered agent. These replace the hardcoded call_user_agent_high/low
// tool definitions.
func (am *AgentManager) BuildCallTools() []openai.Tool {
	agents := am.GetAll()
	tools := make([]openai.Tool, 0, len(agents))

	for _, a := range agents {
		desc := fmt.Sprintf("Send a message to the %s agent", a.Config.Name)
		if a.Config.DisplayName != "" {
			desc = fmt.Sprintf("Send a message to %s (%s)", a.Config.Name, a.Config.DisplayName)
		}
		if a.Config.Description != "" {
			desc += ": " + a.Config.Description
		}
		desc += fmt.Sprintf(". Cost tier: %s. Session is managed automatically.", a.Config.CostTier)

		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "call_agent_" + a.Config.Name,
				Description: desc,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The message to send to the agent",
						},
					},
					"required": []string{"message"},
				},
			},
		})
	}

	return tools
}

// ---------------------------------------------------------------------------
// Session Management Tools
// ---------------------------------------------------------------------------

// BuildSessionManagementTools generates create_session and change_session tools
// with dynamic enum values populated from registered agent names.
func (am *AgentManager) BuildSessionManagementTools() []openai.Tool {
	names := am.Names()
	if len(names) == 0 {
		return nil
	}

	enumValues := make([]string, len(names))
	copy(enumValues, names)

	tools := []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "create_session",
				Description: "Create a new session for an agent and make it the active session. Use when starting a new topic or conversation.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_name": map[string]interface{}{
							"type":        "string",
							"enum":        enumValues,
							"description": "The name of the agent to create the session for",
						},
						"title": map[string]interface{}{
							"type":        "string",
							"description": "Optional title for the session",
						},
					},
					"required": []string{"agent_name"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "change_session",
				Description: "Switch to a different existing session. Use when user wants to continue a previous conversation.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_name": map[string]interface{}{
							"type":        "string",
							"enum":        enumValues,
							"description": "The name of the agent",
						},
						"session_id": map[string]interface{}{
							"type":        "string",
							"description": "The session ID to switch to (from list_sessions)",
						},
					},
					"required": []string{"agent_name", "session_id"},
				},
			},
		},
	}

	return tools
}

// ---------------------------------------------------------------------------
// Active Sessions Prompt
// ---------------------------------------------------------------------------

// BuildActiveSessionsPrompt generates a system prompt section showing the
// current active session for each registered agent. This replaces the
// hardcoded High/Low loop in the old CoreHandler.buildActiveSessionsPrompt.
func (am *AgentManager) BuildActiveSessionsPrompt(
	getSession SessionGetter,
	getActiveSessionID ActiveSessionIDGetter,
	userID string,
) string {
	agents := am.GetAll()
	if len(agents) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Current Active Sessions\n\n")
	sb.WriteString("These are the currently active sessions. Messages are automatically sent to these sessions.\n\n")

	hasActive := false
	for _, a := range agents {
		sessionID := getActiveSessionID(userID, a.Config.AgentType)
		displayName := a.Config.Name
		if a.Config.DisplayName != "" {
			displayName = a.Config.DisplayName
		}

		if sessionID == "" {
			sb.WriteString(fmt.Sprintf("- **%s**: No active session (will be created automatically on first message)\n", displayName))
			continue
		}

		session, err := getSession(sessionID)
		if err != nil || session == nil {
			sb.WriteString(fmt.Sprintf("- **%s**: No active session (previous session was deleted)\n", displayName))
			continue
		}

		hasActive = true
		title := session.Title
		if title == "" {
			title = "Untitled"
		}
		sb.WriteString(fmt.Sprintf("- **%s**: [%s] \"%s\" (%d messages)\n",
			displayName, sessionID, title, len(session.Msgs)))
	}

	if !hasActive {
		return ""
	}

	sb.WriteString("\nUse `create_session` to start a new topic, or `change_session` to switch to a different session.\n")
	return sb.String()
}

// ---------------------------------------------------------------------------
// Session Context Prompts (Summary + Tags)
// ---------------------------------------------------------------------------

// BuildSessionContextPrompt generates the session context (Summary + Tags) for
// a specific agent's active session. This is injected as a system prompt section
// to give the Core LLM context about the ongoing conversation with that agent.
func (am *AgentManager) BuildSessionContextPrompt(
	agentName string,
	getSession SessionGetter,
	getActiveSessionID ActiveSessionIDGetter,
	userID string,
) string {
	agent, ok := am.Get(agentName)
	if !ok {
		return ""
	}

	sessionID := getActiveSessionID(userID, agent.Config.AgentType)
	if sessionID == "" {
		return ""
	}

	session, err := getSession(sessionID)
	if err != nil || session == nil {
		return ""
	}

	if session.Summary == "" && len(session.Tags) == 0 {
		return ""
	}

	displayName := agent.Config.Name
	if agent.Config.DisplayName != "" {
		displayName = agent.Config.DisplayName
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Session Context for %s\n\n", displayName))
	if session.Summary != "" {
		sb.WriteString("### Summary\n")
		sb.WriteString(session.Summary)
		sb.WriteString("\n\n")
	}
	if len(session.Tags) > 0 {
		sb.WriteString("### Topics\n")
		sb.WriteString(strings.Join(session.Tags, ", "))
		sb.WriteString("\n")
	}
	return sb.String()
}

// BuildAllSessionContextsPrompt generates session context for ALL registered
// agents' active sessions. This gives the Core LLM a bird's-eye view of what
// has been discussed across all agents, helping with better routing decisions.
func (am *AgentManager) BuildAllSessionContextsPrompt(
	getSession SessionGetter,
	getActiveSessionID ActiveSessionIDGetter,
	userID string,
) string {
	agents := am.GetAll()
	var parts []string

	for _, a := range agents {
		ctx := am.BuildSessionContextPrompt(a.Config.Name, getSession, getActiveSessionID, userID)
		if ctx != "" {
			parts = append(parts, ctx)
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return "# Agent Session Contexts\n\n" + strings.Join(parts, "\n")
}
