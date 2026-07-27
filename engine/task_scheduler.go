package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

const (
	defaultTaskSchedulerPollInterval = time.Second
	maxTaskScheduleTextLength        = 64 * 1024
	maxTaskScheduleInterval          = 365 * 24 * time.Hour
)

// ScheduledTaskExecutor runs one schedule through its owning agent/session.
type ScheduledTaskExecutor func(ctx context.Context, schedule *model.TaskSchedule) (string, error)

// ScheduledConclusion is the cheap-model result and its token usage.
type ScheduledConclusion struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
}

// ScheduledConclusionFunc sends a task's output to an optional conclusion model.
type ScheduledConclusionFunc func(ctx context.Context, schedule *model.TaskSchedule, output string) (ScheduledConclusion, error)

// CreateTaskScheduleInput is shared by the LLM tool, admin page, and public API.
type CreateTaskScheduleInput struct {
	UserID           string
	SessionID        string
	Name             string
	Prompt           string
	Interval         time.Duration
	ConclusionModel  string
	ConclusionPrompt string
}

// TaskScheduler is a persistent loop runner for user-owned tasks.
type TaskScheduler struct {
	store     store.Store
	executor  ScheduledTaskExecutor
	concluder ScheduledConclusionFunc
	pollEvery time.Duration

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	wake     chan struct{}
	inFlight map[string]context.CancelFunc
	// Empty means this worker accepts every agent type (standalone Engine).
	allowedAgentTypes map[model.AgentType]bool
	wg                sync.WaitGroup
	// Serializes short persisted lifecycle transitions. Task execution itself
	// never holds this lock, so unrelated schedules can still run concurrently.
	lifecycleMu sync.Mutex
}

// NewTaskScheduler creates a scheduler. Call Start after the LLM/executor is ready.
func NewTaskScheduler(
	taskStore store.Store,
	executor ScheduledTaskExecutor,
	concluder ScheduledConclusionFunc,
) *TaskScheduler {
	return &TaskScheduler{
		store: taskStore, executor: executor, concluder: concluder,
		pollEvery: defaultTaskSchedulerPollInterval,
		wake:      make(chan struct{}, 1), inFlight: make(map[string]context.CancelFunc),
		allowedAgentTypes: make(map[model.AgentType]bool),
	}
}

// SetAllowedAgentTypes limits which persisted schedules this worker executes.
// CRUD/tool operations remain available for every schedule owned by the caller.
// With no types configured the worker accepts all schedules.
func (s *TaskScheduler) SetAllowedAgentTypes(agentTypes ...model.AgentType) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.allowedAgentTypes = make(map[model.AgentType]bool, len(agentTypes))
	for _, agentType := range agentTypes {
		if agentType != "" {
			s.allowedAgentTypes[agentType] = true
		}
	}
	s.mu.Unlock()
	s.notify()
}

func (s *TaskScheduler) accepts(schedule *model.TaskSchedule) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.allowedAgentTypes) == 0 {
		return true
	}
	return s.allowedAgentTypes[schedule.AgentType]
}

// InitializeTaskScheduler creates the Engine-owned scheduler and registers its
// model-facing tool. It is safe to call before LLM configuration.
func (e *Engine) InitializeTaskScheduler() {
	if e == nil || e.Sessions == nil {
		return
	}
	e.taskSchedulerMu.Lock()
	if e.taskScheduler == nil {
		e.taskScheduler = NewTaskScheduler(
			e.Sessions,
			func(ctx context.Context, schedule *model.TaskSchedule) (string, error) {
				output, _, err := e.ProcessScheduledMessage(ctx, schedule.SessionID, schedule.Prompt)
				return output, err
			},
			e.concludeScheduledTask,
		)
	}
	e.taskSchedulerMu.Unlock()
	e.RegisterTaskSchedulerTool()
}

// GetTaskScheduler returns the persistent general-purpose scheduler.
func (e *Engine) GetTaskScheduler() *TaskScheduler {
	if e == nil {
		return nil
	}
	e.taskSchedulerMu.RLock()
	defer e.taskSchedulerMu.RUnlock()
	return e.taskScheduler
}

// SetTaskSchedulerAgentTypes scopes this Engine's worker to schedules created
// from the given agent types. AgentManager calls this during registration.
func (e *Engine) SetTaskSchedulerAgentTypes(agentTypes ...model.AgentType) {
	e.InitializeTaskScheduler()
	if scheduler := e.GetTaskScheduler(); scheduler != nil {
		scheduler.SetAllowedAgentTypes(agentTypes...)
	}
}

// StopTaskScheduler gracefully stops the general-purpose scheduler.
func (e *Engine) StopTaskScheduler() {
	if scheduler := e.GetTaskScheduler(); scheduler != nil {
		scheduler.Stop()
	}
}

func (e *Engine) concludeScheduledTask(
	ctx context.Context,
	schedule *model.TaskSchedule,
	output string,
) (ScheduledConclusion, error) {
	if e.llmClient == nil {
		return ScheduledConclusion{}, fmt.Errorf("LLM client is not configured")
	}
	ctx = model.WithUserID(ctx, schedule.UserID)
	instruction := strings.TrimSpace(schedule.ConclusionPrompt)
	if instruction == "" {
		instruction = "Summarize the result compactly, preserving decisions, changes, alerts, and actionable facts."
	}
	messages := []openai.ChatCompletionMessage{
		{
			Role: openai.ChatMessageRoleSystem,
			Content: "You are a low-cost conclusion worker for a recurring task. " +
				"Follow the requested conclusion instruction and use only the supplied task output. Return only the conclusion.",
		},
		{
			Role: openai.ChatMessageRoleUser,
			Content: "CONCLUSION INSTRUCTION:\n" + instruction +
				"\n\nTASK OUTPUT:\n" + truncateTaskScheduleText(output, maxTaskScheduleTextLength),
		},
	}
	if e.Callback != nil {
		if err := e.Callback.BeforeAction(ctx, &UsageEvent{
			UserID: schedule.UserID, SessionID: schedule.SessionID,
			EventType: EventLLMCall, Name: EventNameLLMCall, Model: schedule.ConclusionModel,
			Metadata: map[string]interface{}{"source": "task_scheduler_conclusion"},
		}); err != nil {
			return ScheduledConclusion{}, err
		}
	}

	started := time.Now()
	resp, err := e.llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
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
	if e.Callback != nil {
		e.Callback.AfterAction(ctx, &UsageEvent{
			UserID: schedule.UserID, SessionID: schedule.SessionID,
			EventType: EventLLMCall, Name: EventNameLLMCall, Model: schedule.ConclusionModel,
			Tokens: resp.Usage.TotalTokens, InputTokens: resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens, CachedInputTokens: cached,
			Duration: duration, Error: err,
			Metadata: map[string]interface{}{"source": "task_scheduler_conclusion"},
		})
	}
	if err != nil {
		return ScheduledConclusion{}, err
	}
	if len(resp.Choices) == 0 {
		return ScheduledConclusion{}, fmt.Errorf("conclusion model returned no choices")
	}
	return ScheduledConclusion{
		Text:             strings.TrimSpace(resp.Choices[0].Message.Content),
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}, nil
}

// Start begins polling persisted schedules. It is idempotent.
func (s *TaskScheduler) Start(ctx context.Context) {
	if s == nil || s.store == nil || s.executor == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()

	s.wg.Add(1)
	go s.loop(runCtx)
}

// Stop cancels the worker and every task currently executing.
func (s *TaskScheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.running && len(s.inFlight) == 0 {
		s.mu.Unlock()
		return
	}
	s.running = false
	if s.cancel != nil {
		s.cancel()
	}
	for _, cancel := range s.inFlight {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// IsRunning reports whether the background loop is active.
func (s *TaskScheduler) IsRunning() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *TaskScheduler) loop(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.wg.Done()
	}()
	ticker := time.NewTicker(s.pollEvery)
	defer ticker.Stop()

	s.dispatchDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchDue(ctx)
		case <-s.wake:
			s.dispatchDue(ctx)
		}
	}
}

func (s *TaskScheduler) dispatchDue(ctx context.Context) {
	schedules, err := s.store.ListTaskSchedules("")
	if err != nil {
		log.Log.Errorf("[TaskScheduler] failed to list schedules: %v", err)
		return
	}
	now := time.Now()
	for _, schedule := range schedules {
		if ctx.Err() != nil {
			return
		}
		if schedule.Status != model.TaskScheduleActive || schedule.NextRunAt.After(now) {
			continue
		}
		if !s.accepts(schedule) {
			continue
		}
		s.startRun(ctx, schedule.ScheduleID)
	}
}

func (s *TaskScheduler) startRun(parent context.Context, scheduleID string) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	if _, exists := s.inFlight[scheduleID]; exists {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	s.inFlight[scheduleID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inFlight, scheduleID)
			s.mu.Unlock()
		}()
		s.execute(runCtx, scheduleID)
	}()
}

func (s *TaskScheduler) execute(ctx context.Context, scheduleID string) {
	scheduleLock := &s.lifecycleMu
	scheduleLock.Lock()
	schedule, err := s.store.GetTaskSchedule(scheduleID)
	if err != nil || schedule == nil || schedule.Status != model.TaskScheduleActive {
		scheduleLock.Unlock()
		return
	}

	now := time.Now()
	run := &model.TaskScheduleRun{
		RunID: newTaskID("run"), ScheduleID: schedule.ScheduleID,
		UserID: schedule.UserID, SessionID: schedule.SessionID,
		Status: model.TaskRunRunning, StartedAt: now,
		ConclusionModel: schedule.ConclusionModel,
	}
	schedule.LastRunAt = now
	schedule.LastRunStatus = model.TaskRunRunning
	schedule.LastError = ""
	// Move the due timestamp before work begins. The in-flight guard prevents
	// overlap, while this persisted value prevents a rapid duplicate after restart.
	schedule.NextRunAt = now.Add(schedule.Interval())
	schedule.UpdatedAt = now
	if err := s.store.PutTaskSchedule(schedule); err != nil {
		scheduleLock.Unlock()
		log.Log.Errorf("[TaskScheduler] failed to mark schedule %s running: %v", scheduleID, err)
		return
	}
	scheduleLock.Unlock()
	if err := s.store.PutTaskScheduleRun(run); err != nil {
		log.Log.Errorf("[TaskScheduler] failed to create run for %s: %v", scheduleID, err)
	}

	output, execErr := s.executor(ctx, schedule)
	output = truncateTaskScheduleText(output, maxTaskScheduleTextLength)
	var conclusion ScheduledConclusion
	var conclusionErr error
	if execErr == nil && schedule.ConclusionModel != "" && ctx.Err() == nil {
		if s.concluder == nil {
			conclusionErr = fmt.Errorf("conclusion model is not configured")
		} else {
			conclusion, conclusionErr = s.concluder(ctx, schedule, output)
			conclusion.Text = truncateTaskScheduleText(conclusion.Text, maxTaskScheduleTextLength)
		}
	}

	// Delete is authoritative: do not recreate a run or parent row after it was
	// removed while execution was being cancelled.
	scheduleLock.Lock()
	current, getErr := s.store.GetTaskSchedule(scheduleID)
	if getErr != nil || current == nil {
		scheduleLock.Unlock()
		return
	}

	completedAt := time.Now()
	run.CompletedAt = completedAt
	run.Output = output
	run.Conclusion = conclusion.Text
	run.PromptTokens = conclusion.PromptTokens
	run.CompletionTokens = conclusion.CompletionTokens
	current.RunCount++
	current.LastOutput = output
	current.LastConclusion = conclusion.Text
	current.UpdatedAt = completedAt

	switch {
	case ctx.Err() != nil:
		run.Status = model.TaskRunCancelled
		run.Error = ctx.Err().Error()
	case execErr != nil:
		run.Status = model.TaskRunFailed
		run.Error = execErr.Error()
	case conclusionErr != nil:
		run.Status = model.TaskRunFailed
		run.Error = "conclusion model: " + conclusionErr.Error()
	default:
		run.Status = model.TaskRunSucceeded
	}
	current.LastRunStatus = run.Status
	current.LastError = run.Error
	if current.Status == model.TaskScheduleActive {
		if run.Status == model.TaskRunCancelled {
			// A resume may have raced with cancellation of the previous run.
			// Preserve resume's "due now" semantics instead of delaying a full interval.
			current.NextRunAt = completedAt
		} else {
			current.NextRunAt = completedAt.Add(current.Interval())
		}
	}

	if err := s.store.PutTaskScheduleRun(run); err != nil {
		log.Log.Errorf("[TaskScheduler] failed to finish run %s: %v", run.RunID, err)
	}
	if err := s.store.PutTaskSchedule(current); err != nil {
		log.Log.Errorf("[TaskScheduler] failed to update schedule %s: %v", scheduleID, err)
	}
	scheduleLock.Unlock()
}

// Create validates and persists a new active schedule.
func (s *TaskScheduler) Create(input CreateTaskScheduleInput) (*model.TaskSchedule, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("task scheduler is not configured")
	}
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Name = strings.TrimSpace(input.Name)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.ConclusionModel = strings.TrimSpace(input.ConclusionModel)
	input.ConclusionPrompt = strings.TrimSpace(input.ConclusionPrompt)
	if input.Interval < time.Second || input.Interval > maxTaskScheduleInterval {
		return nil, fmt.Errorf("interval must be between 1 second and 365 days")
	}
	if len(input.Name) > 120 {
		return nil, fmt.Errorf("name must not exceed 120 characters")
	}
	if len(input.Prompt) > 128*1024 {
		return nil, fmt.Errorf("prompt must not exceed 131072 characters")
	}
	if len(input.ConclusionModel) > 256 {
		return nil, fmt.Errorf("conclusion_model must not exceed 256 characters")
	}
	if len(input.ConclusionPrompt) > 8*1024 {
		return nil, fmt.Errorf("conclusion_prompt must not exceed 8192 characters")
	}
	session, err := s.store.Get(input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	if session.UserID != input.UserID {
		return nil, fmt.Errorf("session does not belong to user")
	}
	now := time.Now()
	schedule := &model.TaskSchedule{
		ScheduleID: newTaskID("sch"), UserID: input.UserID, SessionID: input.SessionID,
		AgentType: session.AgentType,
		Name:      input.Name, Prompt: input.Prompt, IntervalSeconds: int64(input.Interval / time.Second),
		ConclusionModel: input.ConclusionModel, ConclusionPrompt: input.ConclusionPrompt,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(input.Interval),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := schedule.Validate(); err != nil {
		return nil, err
	}
	if err := s.store.PutTaskSchedule(schedule); err != nil {
		return nil, err
	}
	s.notify()
	return schedule, nil
}

// List returns schedules scoped to one user; empty userID is the admin view.
func (s *TaskScheduler) List(userID string) ([]*model.TaskSchedule, error) {
	return s.store.ListTaskSchedules(strings.TrimSpace(userID))
}

// Get returns one schedule after optional owner enforcement.
func (s *TaskScheduler) Get(scheduleID, ownerUserID string) (*model.TaskSchedule, error) {
	schedule, err := s.store.GetTaskSchedule(strings.TrimSpace(scheduleID))
	if err != nil || schedule == nil {
		return schedule, err
	}
	if ownerUserID != "" && schedule.UserID != ownerUserID {
		return nil, fmt.Errorf("schedule not found")
	}
	return schedule, nil
}

// Runs returns run history after optional owner enforcement.
func (s *TaskScheduler) Runs(scheduleID, ownerUserID string, limit int) ([]*model.TaskScheduleRun, error) {
	schedule, err := s.Get(scheduleID, ownerUserID)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, fmt.Errorf("schedule not found")
	}
	return s.store.ListTaskScheduleRuns(scheduleID, limit)
}

// Pause stops future executions and cancels a currently-running one.
func (s *TaskScheduler) Pause(scheduleID, ownerUserID string) (*model.TaskSchedule, error) {
	return s.changeStatus(scheduleID, ownerUserID, model.TaskSchedulePaused)
}

// Resume makes a paused schedule active and due immediately.
func (s *TaskScheduler) Resume(scheduleID, ownerUserID string) (*model.TaskSchedule, error) {
	return s.changeStatus(scheduleID, ownerUserID, model.TaskScheduleActive)
}

func (s *TaskScheduler) changeStatus(
	scheduleID, ownerUserID string,
	status model.TaskScheduleStatus,
) (*model.TaskSchedule, error) {
	scheduleLock := &s.lifecycleMu
	scheduleLock.Lock()
	schedule, err := s.Get(scheduleID, ownerUserID)
	if err != nil {
		scheduleLock.Unlock()
		return nil, err
	}
	if schedule == nil {
		scheduleLock.Unlock()
		return nil, fmt.Errorf("schedule not found")
	}
	schedule.Status = status
	schedule.UpdatedAt = time.Now()
	if status == model.TaskScheduleActive {
		schedule.NextRunAt = time.Now()
	}
	if err := s.store.PutTaskSchedule(schedule); err != nil {
		scheduleLock.Unlock()
		return nil, err
	}
	scheduleLock.Unlock()
	if status == model.TaskSchedulePaused {
		s.cancelRun(schedule.ScheduleID)
	}
	s.notify()
	return schedule, nil
}

// RunNow makes an active schedule immediately due.
func (s *TaskScheduler) RunNow(scheduleID, ownerUserID string) (*model.TaskSchedule, error) {
	scheduleLock := &s.lifecycleMu
	scheduleLock.Lock()
	defer scheduleLock.Unlock()
	schedule, err := s.Get(scheduleID, ownerUserID)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, fmt.Errorf("schedule not found")
	}
	if schedule.Status != model.TaskScheduleActive {
		return nil, fmt.Errorf("schedule is paused; resume it first")
	}
	schedule.NextRunAt = time.Now()
	schedule.UpdatedAt = time.Now()
	if err := s.store.PutTaskSchedule(schedule); err != nil {
		return nil, err
	}
	s.notify()
	return schedule, nil
}

// Delete cancels in-flight work and removes the schedule plus history.
func (s *TaskScheduler) Delete(scheduleID, ownerUserID string) error {
	schedule, err := s.Get(scheduleID, ownerUserID)
	if err != nil {
		return err
	}
	if schedule == nil {
		return fmt.Errorf("schedule not found")
	}
	s.mu.Lock()
	cancel := s.inFlight[schedule.ScheduleID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	scheduleLock := &s.lifecycleMu
	scheduleLock.Lock()
	defer scheduleLock.Unlock()
	current, err := s.Get(schedule.ScheduleID, ownerUserID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("schedule not found")
	}
	return s.store.DeleteTaskSchedule(schedule.ScheduleID)
}

func (s *TaskScheduler) cancelRun(scheduleID string) {
	s.mu.Lock()
	cancel := s.inFlight[scheduleID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *TaskScheduler) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func newTaskID(prefix string) string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err == nil {
		return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(random[:]))
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func truncateTaskScheduleText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n[truncated]"
}
