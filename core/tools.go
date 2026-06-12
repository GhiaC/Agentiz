package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// getCoreToolsForLLM returns the tools in OpenAI format using dynamic agent tools.
func (ch *CoreHandler) getCoreToolsForLLM() []openai.Tool {
	// Dynamic call_agent_{name} tools from AgentManager
	tools := ch.agents.BuildCallTools()

	// Session management tools with dynamic agent names
	tools = append(tools, ch.agents.BuildSessionManagementTools()...)

	// Static tools
	tools = append(tools,
		openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_sessions",
				Description: "Get a list of all sessions for the current user. Use to find sessions for change_session.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "ban_user",
				Description: "Ban the current user for a specified duration. Use this when a user repeatedly sends nonsense messages or violates rules.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"duration_hours": map[string]interface{}{
							"type":        "number",
							"description": "Ban duration in hours (0 for permanent ban)",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Optional custom ban message to show to the user",
						},
					},
					"required": []string{"duration_hours"},
				},
			},
		},
		openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "update_status",
				Description: "Send a real-time status/progress update to the user. " +
					"Use before long operations to inform the user what you are doing.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Status message to show the user (in Persian)",
						},
					},
					"required": []string{"message"},
				},
			},
		},
	)

	tools = append(tools, openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "sleep",
			Description: "Pause execution for a specified number of seconds. Use when a delay is needed between operations (e.g. waiting for an external process).",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"seconds": map[string]interface{}{
						"type":        "number",
						"description": "Duration to sleep in seconds (max 300)",
					},
				},
				"required": []string{"seconds"},
			},
		},
	})

	if !ch.config.WebSearchDisabled {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search",
				Description: "Search the web for up-to-date information.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query",
						},
					},
					"required": []string{"query"},
				},
			},
		})
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search_deepresearch",
				Description: "Web search using Tongyi DeepResearch model for deep-research style results.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query",
						},
					},
					"required": []string{"query"},
				},
			},
		})
	}

	if ch.orchestrator != nil {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "execute_plan",
				Description: "Run the user's request through the planning layer. Use for multi-step or structured tasks where order and dependencies matter (e.g. 'fetch data then compare then summarize'). Input: message (the user request). Returns the final output of the plan execution.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The user request or goal to execute as a plan (multi-step task).",
						},
					},
					"required": []string{"message"},
				},
			},
		})
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "get_plan_status",
				Description: "Get the current status of a plan by ID (running, completed, failed, cancelled). Use to check progress of an execute_plan run.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"plan_id": map[string]interface{}{
							"type":        "string",
							"description": "The plan ID returned or known from execute_plan.",
						},
					},
					"required": []string{"plan_id"},
				},
			},
		})
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "cancel_plan",
				Description: "Cancel a running plan by ID. No effect if the plan is already completed or failed.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"plan_id": map[string]interface{}{
							"type":        "string",
							"description": "The plan ID to cancel.",
						},
					},
					"required": []string{"plan_id"},
				},
			},
		})
	}

	return tools
}

// requireStringArg extracts a required string argument from parsed tool-call
// arguments, returning a descriptive error when missing, empty or mistyped.
// Use it for every required string field so validation reads the same at all
// tool sites.
func requireStringArg(args map[string]interface{}, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required and must be a non-empty string", key)
	}
	return v, nil
}

// executeCoreTool executes a Core tool and returns the result string.
func (ch *CoreHandler) executeCoreTool(
	ctx context.Context,
	userID, sessionID string,
	coreSession *model.Session,
	messageID string,
	toolCall openai.ToolCall,
) string {
	persister := ch.getToolCallPersister()
	var toolID string
	if coreSession != nil {
		displayLabel := ch.coreTools.GetDisplayName(toolCall.Function.Name)
		toolID = persister.SaveWithAgentType(coreSession, messageID, toolCall, model.AgentTypeCore, displayLabel)
	}

	toolDetail := ch.coreTools.GetDisplayName(toolCall.Function.Name)
	if toolDetail == "" {
		toolDetail = toolCall.Function.Name
	}
	engine.NotifyStatus(ctx, userID, sessionID, engine.StatusToolExecuting, toolDetail)

	if ch.Callback != nil {
		if cbErr := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
			UserID:    userID,
			SessionID: sessionID,
			EventType: engine.EventToolCall,
			Name:      toolCall.Function.Name,
		}); cbErr != nil {
			result := engine.FormatBlockedActionResult(cbErr)
			persister.Update(toolID, result, cbErr)
			// Surface the block on the routing DAG (e.g. a quota callback denied it).
			routeRecorderFrom(ctx).Tool(model.RouteNodeToolCall, toolCall.Function.Name, toolDetail, "blocked: "+cbErr.Error(), model.RouteStatusBlocked, 0)
			return result
		}
	}

	toolStart := time.Now()
	result, err := ch.runCoreToolImpl(ctx, userID, sessionID, messageID, toolCall)
	toolDuration := time.Since(toolStart)
	metrics.ToolCall("core", toolCall.Function.Name, metrics.Status(err), toolDuration)
	if err != nil {
		result = fmt.Sprintf("Error executing tool: %v", err)
	}

	if ch.Callback != nil {
		ch.Callback.AfterAction(ctx, &engine.UsageEvent{
			UserID:    userID,
			SessionID: sessionID,
			EventType: engine.EventToolCall,
			Name:      toolCall.Function.Name,
			Duration:  toolDuration,
			Error:     err,
		})
	}

	engine.NotifyStatus(ctx, userID, sessionID, engine.StatusToolDone, toolDetail)
	persister.Update(toolID, result, err)

	// Record into the routing DAG. Agent dispatch/escalation nodes are recorded
	// in runCoreToolImpl (with per-agent timing), so skip call_agent_* here.
	if rec := routeRecorderFrom(ctx); rec != nil && !strings.HasPrefix(toolCall.Function.Name, "call_agent_") {
		nodeType := model.RouteNodeToolCall
		if toolCall.Function.Name == "execute_plan" {
			nodeType = model.RouteNodePlan
		}
		rec.Tool(nodeType, toolCall.Function.Name, toolDetail, toolCall.Function.Arguments, routeStatus(err), toolDuration.Milliseconds())
	}

	return result
}

// skipRedundantAgentCall records — but does NOT execute — a call_agent_* tool
// call the Core emitted after it had already dispatched earlier in the same
// turn. Running it would spin up another full worker agent whose answer the Core
// would immediately discard (it returns the first agent's answer verbatim), so
// the Core skips it. The skip is logged and marked on the routing DAG; the
// returned string is the synthetic tool result that keeps the message history
// well-formed. See processWithTools for the dispatch-only rationale.
func (ch *CoreHandler) skipRedundantAgentCall(ctx context.Context, toolCall openai.ToolCall) string {
	agentName := strings.TrimPrefix(toolCall.Function.Name, "call_agent_")
	label := agentName
	if agent, ok := ch.agents.Get(agentName); ok {
		agentName = agent.Config.Name
		label = agentDispatchLabel(agent)
	}

	var message string
	var args map[string]interface{}
	if json.Unmarshal([]byte(toolCall.Function.Arguments), &args) == nil {
		message, _ = args["message"].(string)
	}

	routeRecorderFrom(ctx).SkipDispatch(agentName, label, message)
	log.Log.Infof("[CoreHandler] ⏭️  Skipping redundant agent call (already dispatched this turn) | Agent: %s", agentName)

	return fmt.Sprintf("Skipped: the Core already routed to another agent in this turn, so %s was not called.", agentName)
}

// runCoreToolImpl runs the Core tool logic (switch on tool name).
// messageID is the assistant message that contains this tool call; used e.g. to update plan status on that message.
func (ch *CoreHandler) runCoreToolImpl(
	ctx context.Context,
	userID, sessionID, messageID string,
	toolCall openai.ToolCall,
) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("failed to parse tool arguments: %w", err)
	}

	toolName := toolCall.Function.Name

	// Dynamic agent dispatch: call_agent_{name}
	if strings.HasPrefix(toolName, "call_agent_") {
		agentName := strings.TrimPrefix(toolName, "call_agent_")
		agent, ok := ch.agents.Get(agentName)
		if !ok {
			return "", fmt.Errorf("unknown agent: %s", agentName)
		}

		engine.NotifyStatus(ctx, userID, "", engine.StatusAgentCalling, agentName)
		if ch.Callback != nil {
			if cbErr := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
				UserID: userID, EventType: engine.EventAgentRouting, Name: agentName,
			}); cbErr != nil {
				return engine.FormatBlockedActionResult(cbErr), nil
			}
		}

		dispatchStart := time.Now()
		result, err := ch.callAgent(ctx, userID, args, agent)
		dispatchMsg, _ := args["message"].(string)
		// Record the forward (routing decision) on the DAG, with per-agent timing.
		routeRecorderFrom(ctx).Dispatch(agent.Config.Name, agentDispatchLabel(agent), dispatchMsg, routeStatus(err), time.Since(dispatchStart).Milliseconds())

		// Escalation: if this is not the highest-tier agent and result starts with "ESCALATE:"
		if err == nil && strings.HasPrefix(strings.TrimSpace(result), "ESCALATE:") {
			if ch.Callback != nil {
				ch.Callback.AfterAction(ctx, &engine.UsageEvent{
					UserID: userID, SessionID: sessionID, EventType: engine.EventAgentRouting, Name: agentName,
				})
			}
			// Find a higher-tier agent
			higherAgent := ch.findHigherTierAgent(agent.Config.CostTier)
			if higherAgent != nil {
				metrics.AgentEscalation(agentName)
				engine.NotifyStatus(ctx, userID, "", engine.StatusAgentCalling, higherAgent.Config.Name+" (escalated)")
				escStart := time.Now()
				result, err = ch.callAgent(ctx, userID, args, higherAgent)
				// Record the escalation (forward to a higher tier) on the DAG.
				routeRecorderFrom(ctx).Escalate(higherAgent.Config.Name, agentDispatchLabel(higherAgent), dispatchMsg, routeStatus(err), time.Since(escStart).Milliseconds())
				metrics.AgentRouting(higherAgent.Config.Name, metrics.Status(err))
				if ch.Callback != nil {
					ch.Callback.AfterAction(ctx, &engine.UsageEvent{
						UserID: userID, SessionID: sessionID, EventType: engine.EventAgentRouting, Name: higherAgent.Config.Name, Error: err,
					})
				}
				engine.NotifyStatus(ctx, userID, "", engine.StatusAgentDone, higherAgent.Config.Name)
				return result, err
			}
		}

		if ch.Callback != nil {
			ch.Callback.AfterAction(ctx, &engine.UsageEvent{
				UserID: userID, SessionID: sessionID, EventType: engine.EventAgentRouting, Name: agentName, Error: err,
			})
		}
		metrics.AgentRouting(agentName, metrics.Status(err))
		engine.NotifyStatus(ctx, userID, "", engine.StatusAgentDone, agentName)
		return result, err
	}

	switch toolName {
	case "update_status":
		message, _ := args["message"].(string)
		if message != "" {
			engine.NotifyStatus(ctx, userID, "", engine.StatusCustom, message)
		}
		return "status updated", nil

	case "create_session":
		return ch.createSessionTool(ctx, userID, args)

	case "change_session":
		return ch.changeSessionTool(ctx, userID, args)

	case "list_sessions":
		return ch.listSessionsTool(userID)

	case "ban_user":
		return ch.banUserTool(ctx, userID, args)

	case "web_search":
		return ch.webSearchWithModelTool(ctx, userID, args, "")
	case "web_search_deepresearch":
		return ch.webSearchWithModelTool(ctx, userID, args, engine.SearchModelTongyiDeepResearch)

	case "sleep":
		return ch.sleepTool(ctx, args)

	case "execute_plan":
		return ch.executePlanTool(ctx, userID, sessionID, messageID, args)
	case "get_plan_status":
		return ch.getPlanStatusTool(ctx, args)
	case "cancel_plan":
		return ch.cancelPlanTool(ctx, args)

	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

// callAgent sends a message to an agent's Engine.
func (ch *CoreHandler) callAgent(
	ctx context.Context,
	userID string,
	args map[string]interface{},
	agent *agentmanager.RegisteredAgent,
) (string, error) {
	message, err := requireStringArg(args, "message")
	if err != nil {
		return "", err
	}

	sessionID, err := ch.getOrCreateActiveSession(userID, agent.Config.AgentType)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Failed to get/create active session | UserID: %s | Agent: %s | Error: %v",
			userID, agent.Config.Name, err)
		return "", fmt.Errorf("failed to get active session: %w", err)
	}

	log.Log.Infof("[CoreHandler] 🎯 Calling agent | Agent: %s | SessionID: %s | UserID: %s | Message length: %d chars",
		agent.Config.Name, sessionID, userID, len(message))

	response, _, err := agent.Engine.ProcessMessage(ctx, sessionID, message)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Agent processing failed | Agent: %s | SessionID: %s | Error: %v", agent.Config.Name, sessionID, err)
		return "", fmt.Errorf("agent %s error: %w", agent.Config.Name, err)
	}

	log.Log.Infof("[CoreHandler] ✅ Agent response received | Agent: %s | SessionID: %s | Response length: %d chars",
		agent.Config.Name, sessionID, len(response))

	return response, nil
}

// findHigherTierAgent returns an agent with a higher cost tier, or nil if none found.
func (ch *CoreHandler) findHigherTierAgent(currentTier agentmanager.CostTier) *agentmanager.RegisteredAgent {
	var targetTier agentmanager.CostTier
	switch currentTier {
	case agentmanager.CostTierLow:
		targetTier = agentmanager.CostTierMedium
	case agentmanager.CostTierMedium:
		targetTier = agentmanager.CostTierHigh
	default:
		return nil
	}

	agents := ch.agents.GetByTier(targetTier)
	if len(agents) > 0 {
		return agents[0]
	}

	// If medium not found, try high
	if targetTier == agentmanager.CostTierMedium {
		agents = ch.agents.GetByTier(agentmanager.CostTierHigh)
		if len(agents) > 0 {
			return agents[0]
		}
	}
	return nil
}

// createSessionTool creates a new session for a dynamic agent.
func (ch *CoreHandler) createSessionTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	agentName, err := requireStringArg(args, "agent_name")
	if err != nil {
		return "", err
	}

	agent, ok := ch.agents.Get(agentName)
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agentName)
	}

	log.Log.Infof("[CoreHandler] 🛠️  createSessionTool | UserID: %s | Agent: %s", userID, agentName)

	session, err := ch.createSessionForUser(userID, agent.Config.AgentType)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	if title, ok := args["title"].(string); ok && title != "" {
		session.Title = title
		ch.sessionHandler.UpdateSessionMetadata(session.SessionID, title, nil, "")
	}

	if err := ch.setActiveSessionID(userID, agent.Config.AgentType, session.SessionID); err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to set active session | UserID: %s | Agent: %s | Error: %v", userID, agentName, err)
	}

	// The active-session and sessions-list sections of the system prompt changed.
	ch.invalidateSystemPrompt(userID)

	return fmt.Sprintf("Created new session and set as active (agent: %s)", agentName), nil
}

// changeSessionTool switches to an existing session.
func (ch *CoreHandler) changeSessionTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	agentName, err := requireStringArg(args, "agent_name")
	if err != nil {
		return "", err
	}

	sessionID, err := requireStringArg(args, "session_id")
	if err != nil {
		return "", err
	}

	agent, ok := ch.agents.Get(agentName)
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agentName)
	}

	session, err := ch.sessionHandler.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	if session.AgentType != agent.Config.AgentType {
		return "", fmt.Errorf("session %s is not a %s session (it's a %s session)", sessionID, agentName, session.AgentType)
	}

	if err := ch.setActiveSessionID(userID, agent.Config.AgentType, sessionID); err != nil {
		return "", fmt.Errorf("failed to set active session: %w", err)
	}

	// The active-session and sessions-list sections of the system prompt changed.
	ch.invalidateSystemPrompt(userID)

	title := session.Title
	if title == "" {
		title = "Untitled"
	}

	return fmt.Sprintf("Switched to session: %s (agent: %s)", title, agentName), nil
}

func (ch *CoreHandler) listSessionsTool(userID string) (string, error) {
	_, err := ch.sessionHandler.ListUserSessions(userID)
	if err != nil {
		return "", err
	}
	return ch.sessionHandler.GetSessionsPrompt(userID)
}

func (ch *CoreHandler) banUserTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user_id is required but not available in context")
	}

	durationHours, ok := args["duration_hours"].(float64)
	if !ok {
		return "", fmt.Errorf("duration_hours is required and must be a number")
	}

	message, _ := args["message"].(string)
	if message == "" {
		if durationHours == 0 {
			message = "You have been permanently restricted."
		} else {
			message = fmt.Sprintf("You have been restricted for %.0f hours.", durationHours)
		}
	}

	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get user: %w", err)
	}

	var banDuration time.Duration
	if durationHours > 0 {
		banDuration = time.Duration(durationHours) * time.Hour
	}

	user.Ban(banDuration, message)
	metrics.Ban("manual")
	if err := ch.saveUser(user); err != nil {
		return "", fmt.Errorf("failed to save user ban: %w", err)
	}

	log.Log.Infof("[CoreHandler] 🚫 User banned | UserID: %s | Duration: %v", userID, banDuration)
	return fmt.Sprintf("User %s has been banned. Duration: %v", userID, banDuration), nil
}

func (ch *CoreHandler) sleepTool(ctx context.Context, args map[string]interface{}) (string, error) {
	seconds, _ := args["seconds"].(float64)
	if seconds <= 0 {
		return "", fmt.Errorf("seconds must be a positive number")
	}
	const maxSleep = 300
	if seconds > maxSleep {
		seconds = maxSleep
	}
	d := time.Duration(seconds * float64(time.Second))
	select {
	case <-time.After(d):
		return fmt.Sprintf("slept for %.1f seconds", seconds), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (ch *CoreHandler) webSearchWithModelTool(ctx context.Context, userID string, args map[string]interface{}, searchModel string) (string, error) {
	query, err := requireStringArg(args, "query")
	if err != nil {
		return "", err
	}
	result, err := engine.PerformWebSearchWithModel(ctx, ch.llmClient, ch.llmConfig, query, userID, searchModel)
	if err != nil {
		return "", fmt.Errorf("web search failed: %w", err)
	}
	if result != "" {
		initialMessage := engine.FormatWebSearchInitialMessage(result, 0)
		engine.NotifyStatus(ctx, userID, "", engine.StatusCustom, initialMessage, engine.OptSendAsNewMessage())
	}
	return result, nil
}

// registerCoreTools registers display names for status/UI.
func (ch *CoreHandler) registerCoreTools() {
	coreToolNoOp := func(args map[string]interface{}) (string, error) { return "", nil }

	// Dynamic agent tools
	for _, agent := range ch.agents.GetAll() {
		toolName := "call_agent_" + agent.Config.Name
		ch.coreTools.MustRegister(toolName, agent.Config.DisplayName, coreToolNoOp)
	}

	ch.coreTools.MustRegister("update_status", "به‌روزرسانی وضعیت", coreToolNoOp)
	ch.coreTools.MustRegister("create_session", "ایجاد نشست", coreToolNoOp)
	ch.coreTools.MustRegister("change_session", "تغییر نشست", coreToolNoOp)
	ch.coreTools.MustRegister("list_sessions", "لیست نشست‌ها", coreToolNoOp)
	ch.coreTools.MustRegister("ban_user", "مسدود کاربر", coreToolNoOp)
	ch.coreTools.MustRegister("sleep", "توقف موقت", coreToolNoOp)
	ch.coreTools.MustRegister("web_search", "جستجوی وب", coreToolNoOp)
	ch.coreTools.MustRegister("web_search_deepresearch", "جستجوی وب (عمیق)", coreToolNoOp)
}

// saveCoreMessage saves a message from CoreHandler to the database.
func (ch *CoreHandler) saveCoreMessage(
	userID string,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) string {
	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to get core session for message save | UserID: %s | Error: %v", userID, err)
		return ""
	}

	content := choice.Message.Content
	if content == "" && len(choice.Message.ToolCalls) > 0 {
		content = engine.FormatToolCallsContent(choice.Message.ToolCalls)
	}

	messageID, seqID := coreSession.GenerateMessageIDWithSeq()
	msg := model.NewMessage(
		messageID,
		seqID,
		userID,
		coreSession.SessionID,
		openai.ChatMessageRoleAssistant,
		content,
		model.AgentTypeCore,
		model.ContentTypeText,
		request,
		response,
		choice,
	)

	ch.saveMessage(msg)
	return msg.MessageID
}

func (ch *CoreHandler) saveMessage(msg *model.Message) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutMessage(*model.Message) error
	}); ok {
		if err := sqliteStore.PutMessage(msg); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to save message | MessageID: %s | Error: %v", msg.MessageID, err)
		}
	}
}

func (ch *CoreHandler) getToolCallPersister() *engine.ToolCallPersister {
	return engine.NewToolCallPersister(ch.sessionHandler.GetStore(), "CoreHandler")
}
