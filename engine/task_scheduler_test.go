package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

func newTaskSchedulerTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := model.NewSessionWithID("user-1", "user-1-low-s0001", model.AgentTypeLow)
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTaskSchedulerRunConclusionAndLifecycle(t *testing.T) {
	st := newTaskSchedulerTestStore(t)
	var executions atomic.Int64
	scheduler := NewTaskScheduler(
		st,
		func(ctx context.Context, schedule *model.TaskSchedule) (string, error) {
			executions.Add(1)
			return "raw output", nil
		},
		func(ctx context.Context, schedule *model.TaskSchedule, output string) (ScheduledConclusion, error) {
			if output != "raw output" {
				t.Errorf("concluder output = %q", output)
			}
			return ScheduledConclusion{Text: "cheap conclusion", PromptTokens: 7, CompletionTokens: 3}, nil
		},
	)
	scheduler.Start(context.Background())
	t.Cleanup(scheduler.Stop)

	schedule, err := scheduler.Create(CreateTaskScheduleInput{
		UserID: "user-1", SessionID: "user-1-low-s0001",
		Name: "check", Prompt: "perform check", Interval: time.Hour,
		ConclusionModel: "cheap-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.RunNow(schedule.ScheduleID, "user-1"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err := scheduler.Get(schedule.ScheduleID, "user-1")
		if err != nil {
			t.Fatal(err)
		}
		if current.RunCount == 1 {
			if current.LastRunStatus != model.TaskRunSucceeded {
				t.Fatalf("last status = %q", current.LastRunStatus)
			}
			if current.LastConclusion != "cheap conclusion" {
				t.Fatalf("last conclusion = %q", current.LastConclusion)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions = %d, want 1", executions.Load())
	}
	runs, err := scheduler.Runs(schedule.ScheduleID, "user-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].PromptTokens != 7 || runs[0].CompletionTokens != 3 {
		t.Fatalf("runs = %#v", runs)
	}

	paused, err := scheduler.Pause(schedule.ScheduleID, "user-1")
	if err != nil || paused.Status != model.TaskSchedulePaused {
		t.Fatalf("pause: schedule=%#v err=%v", paused, err)
	}
	if _, err := scheduler.RunNow(schedule.ScheduleID, "user-1"); err == nil {
		t.Fatal("run-now on paused schedule should fail")
	}
	resumed, err := scheduler.Resume(schedule.ScheduleID, "user-1")
	if err != nil || resumed.Status != model.TaskScheduleActive {
		t.Fatalf("resume: schedule=%#v err=%v", resumed, err)
	}
	if err := scheduler.Delete(schedule.ScheduleID, "user-1"); err != nil {
		t.Fatal(err)
	}
	got, err := scheduler.Get(schedule.ScheduleID, "user-1")
	if err != nil || got != nil {
		t.Fatalf("after delete: schedule=%#v err=%v", got, err)
	}
}

func TestTaskSchedulerToolOwnershipAndSchema(t *testing.T) {
	st := newTaskSchedulerTestStore(t)
	scheduler := NewTaskScheduler(st, func(context.Context, *model.TaskSchedule) (string, error) {
		return "ok", nil
	}, nil)

	def := TaskSchedulerToolDefinition()
	if def.Function == nil || def.Function.Name != "manage_schedules" {
		t.Fatalf("unexpected tool definition: %#v", def)
	}
	result, err := scheduler.ExecuteTool(map[string]interface{}{
		"__user_id__": "user-1", "__session_id__": "user-1-low-s0001",
		"action": "create", "name": "loop", "prompt": "work",
		"interval_seconds": float64(60),
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		OK       bool                `json:"ok"`
		Schedule *model.TaskSchedule `json:"schedule"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Schedule == nil {
		t.Fatalf("payload = %#v", payload)
	}
	if _, err := scheduler.Get(payload.Schedule.ScheduleID, "other-user"); err == nil {
		t.Fatal("cross-user schedule access should be denied")
	}
	_, err = scheduler.ExecuteTool(map[string]interface{}{
		"__user_id__": "other-user", "__session_id__": "other-session",
		"action": "delete", "schedule_id": payload.Schedule.ScheduleID,
	})
	if err == nil {
		t.Fatal("cross-user delete should be denied")
	}
}

func TestTaskSchedulerDeleteCancelsWithoutRecreatingRows(t *testing.T) {
	st := newTaskSchedulerTestStore(t)
	started := make(chan struct{})
	scheduler := NewTaskScheduler(st, func(ctx context.Context, _ *model.TaskSchedule) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	scheduler.Start(context.Background())
	t.Cleanup(scheduler.Stop)

	schedule, err := scheduler.Create(CreateTaskScheduleInput{
		UserID: "user-1", SessionID: "user-1-low-s0001",
		Name: "blocking", Prompt: "wait", Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.RunNow(schedule.ScheduleID, "user-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled executor did not start")
	}
	if err := scheduler.Delete(schedule.ScheduleID, "user-1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTaskSchedule(schedule.ScheduleID)
	if err != nil || got != nil {
		t.Fatalf("schedule recreated after delete: got=%#v err=%v", got, err)
	}
	runs, err := st.ListTaskScheduleRuns(schedule.ScheduleID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("run rows recreated after delete: runs=%#v err=%v", runs, err)
	}
}

func TestTaskSchedulerCreatesDedicatedMemorySessionAndCompletesAtMaxRuns(t *testing.T) {
	st := newTaskSchedulerTestStore(t)
	seenSessionIDs := make(chan string, 2)
	scheduler := NewTaskScheduler(st, func(_ context.Context, schedule *model.TaskSchedule) (string, error) {
		seenSessionIDs <- schedule.SessionID
		session, err := st.Get(schedule.SessionID)
		if err != nil {
			return "", err
		}
		session.Msgs = append(session.Msgs, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: "remembered",
		})
		if err := st.Put(session); err != nil {
			return "", err
		}
		return "ok", nil
	}, nil)
	scheduler.Start(context.Background())
	t.Cleanup(scheduler.Stop)

	schedule, err := scheduler.Create(CreateTaskScheduleInput{
		UserID: "user-1", SessionID: "user-1-low-s0001",
		Name: "one shot", Prompt: "work", Interval: time.Hour, MaxRuns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if schedule.SessionID == schedule.SourceSessionID {
		t.Fatalf("schedule reused foreground session %q", schedule.SessionID)
	}
	dedicated, err := st.Get(schedule.SessionID)
	if err != nil || dedicated == nil {
		t.Fatalf("dedicated session: %#v err=%v", dedicated, err)
	}
	if dedicated.Title != "Schedule: one shot" ||
		len(dedicated.Tags) != 2 ||
		dedicated.Tags[1] != "schedule:"+schedule.ScheduleID {
		t.Fatalf("dedicated session metadata = %#v", dedicated)
	}

	if _, err := scheduler.RunNow(schedule.ScheduleID, "user-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seenSessionIDs:
		if got != schedule.SessionID {
			t.Fatalf("executor session = %q, want %q", got, schedule.SessionID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("schedule did not run")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := scheduler.Get(schedule.ScheduleID, "user-1")
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status == model.TaskScheduleCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := scheduler.Get(schedule.ScheduleID, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskScheduleCompleted || current.RunCount != 1 {
		t.Fatalf("schedule status=%q runs=%d", current.Status, current.RunCount)
	}
	if _, err := scheduler.Resume(schedule.ScheduleID, "user-1"); err == nil {
		t.Fatal("completed schedule should not resume past max_runs")
	}
	dedicated, err = st.Get(schedule.SessionID)
	if err != nil || len(dedicated.Msgs) != 1 || dedicated.Msgs[0].Content != "remembered" {
		t.Fatalf("dedicated session memory was not persisted: %#v err=%v", dedicated, err)
	}
}
