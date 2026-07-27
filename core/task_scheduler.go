package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

const coreScheduleOutputLimit = 64 * 1024

// initializeTaskScheduler enables the built-in scheduler when the Core's
// session store is Agentize's full persistent Store. Narrow test/fake stores
// continue to work; they simply do not expose manage_schedules.
func (ch *CoreHandler) initializeTaskScheduler() {
	if ch == nil || ch.sessionHandler == nil {
		return
	}
	taskStore, ok := ch.sessionHandler.GetStore().(store.Store)
	if !ok {
		return
	}
	ch.taskScheduler = engine.NewTaskScheduler(
		taskStore,
		func(ctx context.Context, schedule *model.TaskSchedule) (string, error) {
			return ch.ProcessScheduledMessage(ctx, schedule.UserID, schedule.Prompt)
		},
		ch.concludeScheduledTask,
	)
	ch.taskScheduler.SetAllowedAgentTypes(model.AgentTypeCore)
	_ = ch.coreTools.RegisterOrReplace(
		"manage_schedules",
		"مدیریت زمان‌بندی‌ها",
		func(map[string]interface{}) (string, error) { return "", nil },
	)
}

// StopTaskScheduler stops the Core recurring-task worker. Hosts should call it
// during graceful shutdown when they run Core directly.
func (ch *CoreHandler) StopTaskScheduler() {
	if ch != nil && ch.taskScheduler != nil {
		ch.taskScheduler.Stop()
	}
}

func (ch *CoreHandler) concludeScheduledTask(
	ctx context.Context,
	schedule *model.TaskSchedule,
	output string,
) (engine.ScheduledConclusion, error) {
	if ch.llmClient == nil {
		return engine.ScheduledConclusion{}, fmt.Errorf("Core LLM client is not configured")
	}
	ctx = model.WithUserID(ctx, schedule.UserID)
	instruction := strings.TrimSpace(schedule.ConclusionPrompt)
	if instruction == "" {
		instruction = "Summarize the result compactly, preserving decisions, changes, alerts, and actionable facts."
	}
	if len(output) > coreScheduleOutputLimit {
		output = output[:coreScheduleOutputLimit] + "\n[truncated]"
	}
	messages := []openai.ChatCompletionMessage{
		{
			Role: openai.ChatMessageRoleSystem,
			Content: "You are a low-cost conclusion worker for a recurring task. " +
				"Follow the requested instruction using only the supplied output. Return only the conclusion.",
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "CONCLUSION INSTRUCTION:\n" + instruction + "\n\nTASK OUTPUT:\n" + output,
		},
	}
	if ch.Callback != nil {
		if err := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
			UserID: schedule.UserID, SessionID: schedule.SessionID,
			EventType: engine.EventLLMCall, Name: engine.EventNameLLMCall,
			Model:    schedule.ConclusionModel,
			Metadata: map[string]interface{}{"source": "task_scheduler_conclusion"},
		}); err != nil {
			return engine.ScheduledConclusion{}, err
		}
	}
	started := time.Now()
	resp, err := ch.llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: schedule.ConclusionModel, Messages: messages, MaxTokens: 500,
	})
	duration := time.Since(started)
	cached := 0
	if resp.Usage.PromptTokensDetails != nil {
		cached = resp.Usage.PromptTokensDetails.CachedTokens
	}
	metrics.LLMCall(
		"schedule_conclusion", schedule.ConclusionModel, metrics.Status(err), duration,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cached,
	)
	if ch.Callback != nil {
		ch.Callback.AfterAction(ctx, &engine.UsageEvent{
			UserID: schedule.UserID, SessionID: schedule.SessionID,
			EventType: engine.EventLLMCall, Name: engine.EventNameLLMCall,
			Model: schedule.ConclusionModel, Tokens: resp.Usage.TotalTokens,
			InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
			CachedInputTokens: cached, Duration: duration, Error: err,
			Metadata: map[string]interface{}{"source": "task_scheduler_conclusion"},
		})
	}
	if err != nil {
		return engine.ScheduledConclusion{}, err
	}
	if len(resp.Choices) == 0 {
		return engine.ScheduledConclusion{}, fmt.Errorf("conclusion model returned no choices")
	}
	return engine.ScheduledConclusion{
		Text:             strings.TrimSpace(resp.Choices[0].Message.Content),
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}, nil
}
