package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

type countingWorkflowApprovalManager struct {
	mu     sync.Mutex
	count  int
	reject bool
	reqs   map[string]*model.ReviewRequest
}

func (m *countingWorkflowApprovalManager) Request(
	_ context.Context,
	request *model.ReviewRequest,
) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reqs == nil {
		m.reqs = make(map[string]*model.ReviewRequest)
	}
	m.count++
	request.ID = "workflow-review-" + request.RefID
	m.reqs[request.ID] = request
	return request.ID, nil
}

func (m *countingWorkflowApprovalManager) Await(
	_ context.Context,
	requestID string,
) (*model.ReviewRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	request := m.reqs[requestID]
	if m.reject {
		request.Status = model.ReviewRejected
		request.Decision = "reject"
	} else {
		request.Status = model.ReviewApproved
		request.Decision = "approve"
	}
	return request, nil
}

func (m *countingWorkflowApprovalManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

func TestImmediateWorkflowApprovesEveryTaskAndResolvesOutputs(t *testing.T) {
	ch, st := newTestCoreHandler(t, nil)
	coreSession := model.NewSessionWithID("user-1", "user-1-core-s0001", model.AgentTypeCore)
	if err := st.Put(coreSession); err != nil {
		t.Fatal(err)
	}
	approvals := &countingWorkflowApprovalManager{}
	ch.SetToolApprovalManager(approvals)

	result, err := ch.executeWorkflowTool(
		context.Background(), "user-1", coreSession.SessionID, coreSession, "message-1",
		map[string]interface{}{
			"name": "two exact actions",
			"tasks": []interface{}{
				map[string]interface{}{
					"id": "first", "name": "First", "tool": "update_status",
					"arguments": map[string]interface{}{"message": "starting"},
				},
				map[string]interface{}{
					"id": "second", "name": "Second", "tool": "update_status",
					"arguments":  map[string]interface{}{"message": "{{tasks.first.output}}"},
					"depends_on": []interface{}{"first"},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if approvals.Count() != 2 {
		t.Fatalf("approval requests = %d, want 2", approvals.Count())
	}
	var payload struct {
		WorkflowID string               `json:"workflow_id"`
		Status     model.WorkflowStatus `json:"status"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != model.WorkflowSucceeded || payload.WorkflowID == "" {
		t.Fatalf("workflow result = %s", result)
	}
	persisted, err := st.GetWorkflowRun(payload.WorkflowID)
	if err != nil || persisted == nil {
		t.Fatalf("persisted workflow=%#v err=%v", persisted, err)
	}
	if got := persisted.Tasks[1].Arguments["message"]; got != "{{tasks.first.output}}" {
		t.Fatalf("persisted template was mutated: %#v", got)
	}
	toolCalls, err := st.GetAllToolCalls()
	if err != nil {
		t.Fatal(err)
	}
	if len(toolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(toolCalls))
	}
	var secondCall *model.ToolCall
	for _, call := range toolCalls {
		if call.ToolCallID == payload.WorkflowID+":second" {
			secondCall = call
			break
		}
	}
	if secondCall == nil {
		t.Fatalf("second task tool call not found: %#v", toolCalls)
	}
	var secondArgs map[string]interface{}
	if err := json.Unmarshal([]byte(secondCall.Arguments), &secondArgs); err != nil {
		t.Fatal(err)
	}
	if secondArgs["message"] != "status updated" {
		t.Fatalf("resolved second arguments = %#v", secondArgs)
	}
}

func TestScheduledWorkflowUsesDedicatedSessionAndBypassesPerTaskApproval(t *testing.T) {
	ch, st := newTestCoreHandler(t, nil)
	source := model.NewSessionWithID("user-1", "user-1-core-s0001", model.AgentTypeCore)
	if err := st.Put(source); err != nil {
		t.Fatal(err)
	}
	approvals := &countingWorkflowApprovalManager{reject: true}
	ch.SetToolApprovalManager(approvals)
	ch.taskScheduler.Start(context.Background())
	t.Cleanup(ch.taskScheduler.Stop)

	result, err := ch.createWorkflowScheduleTool(
		"user-1", source.SessionID,
		map[string]interface{}{
			"name": "fixed state machine", "interval_seconds": float64(3600), "max_runs": float64(1),
			"tasks": []interface{}{
				map[string]interface{}{
					"id": "notify", "name": "Notify", "tool": "update_status",
					"arguments": map[string]interface{}{"message": "scheduled"},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ScheduleID string `json:"schedule_id"`
		SessionID  string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(result), &created); err != nil {
		t.Fatal(err)
	}
	if created.ScheduleID == "" || created.SessionID == "" || created.SessionID == source.SessionID {
		t.Fatalf("create result = %s", result)
	}
	scheduleSession, err := st.Get(created.SessionID)
	if err != nil || scheduleSession == nil {
		t.Fatalf("schedule session=%#v err=%v", scheduleSession, err)
	}
	if scheduleSession.AgentType != model.AgentTypeWorkflow ||
		scheduleSession.Title != "Schedule: fixed state machine" {
		t.Fatalf("schedule session metadata = %#v", scheduleSession)
	}
	if _, err := ch.taskScheduler.RunNow(created.ScheduleID, "user-1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		schedule, getErr := ch.taskScheduler.Get(created.ScheduleID, "user-1")
		if getErr != nil {
			t.Fatal(getErr)
		}
		if schedule.Status == model.TaskScheduleCompleted {
			if schedule.LastRunStatus != model.TaskRunSucceeded || schedule.LastWorkflowID == "" {
				t.Fatalf("completed schedule = %#v", schedule)
			}
			workflow, workflowErr := st.GetWorkflowRun(schedule.LastWorkflowID)
			if workflowErr != nil || workflow == nil || workflow.Status != model.WorkflowSucceeded {
				t.Fatalf("workflow=%#v err=%v", workflow, workflowErr)
			}
			if workflow.SessionID != created.SessionID || workflow.ScheduleID != created.ScheduleID {
				t.Fatalf("workflow links = %#v", workflow)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if approvals.Count() != 0 {
		t.Fatalf("scheduled task requested %d approvals, want 0", approvals.Count())
	}
}

func TestScheduledWorkflowCannotRouteToAgent(t *testing.T) {
	ch, st := newTestCoreHandler(t, []string{"researcher"})
	session := model.NewSessionWithID("user-1", "user-1-workflow-s0001", model.AgentTypeWorkflow)
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	workflow, err := ch.runWorkflow(
		context.Background(), "user-1", session.SessionID, session, "", "schedule-1", "invalid",
		[]*model.WorkflowTask{
			{
				ID: "route", Name: "Route", Tool: "call_agent_researcher",
				Arguments: map[string]any{"message": "do work"},
			},
		},
		false,
	)
	if err == nil || workflow != nil {
		t.Fatalf("scheduled agent routing should fail validation: workflow=%#v err=%v", workflow, err)
	}
}

func TestCorePromptScheduleBindsDedicatedSessionToNamedAgent(t *testing.T) {
	ch, st := newTestCoreHandler(t, []string{"researcher"})
	source := model.NewSessionWithID("user-1", "user-1-core-s0001", model.AgentTypeCore)
	if err := st.Put(source); err != nil {
		t.Fatal(err)
	}
	result, err := ch.runCoreToolImpl(
		context.Background(), "user-1", source.SessionID, source, "message-1",
		openai.ToolCall{Function: openai.FunctionCall{
			Name:      "manage_schedules",
			Arguments: `{"action":"create","agent_name":"researcher","name":"memory","prompt":"continue","interval_seconds":60}`,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Schedule *model.TaskSchedule `json:"schedule"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Schedule == nil ||
		payload.Schedule.AgentType != model.AgentType("researcher") ||
		payload.Schedule.SessionID == source.SessionID {
		t.Fatalf("schedule = %#v", payload.Schedule)
	}
	dedicated, err := st.Get(payload.Schedule.SessionID)
	if err != nil || dedicated == nil || dedicated.Title != "Schedule: memory" {
		t.Fatalf("dedicated session=%#v err=%v", dedicated, err)
	}
}
