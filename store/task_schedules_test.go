package store

import (
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

func TestSQLiteTaskSchedulePersistence(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	testTaskSchedules(t, st)
}

func testTaskSchedules(t *testing.T, st Store) {
	now := time.Now().Truncate(time.Second)
	schedule := &model.TaskSchedule{
		ScheduleID: "sch-1", UserID: "user-1", SessionID: "session-1",
		Name: "Monitor", Prompt: "check", IntervalSeconds: 60,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.PutTaskSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	run := &model.TaskScheduleRun{
		RunID: "run-1", ScheduleID: schedule.ScheduleID,
		UserID: schedule.UserID, SessionID: schedule.SessionID,
		Status: model.TaskRunSucceeded, Output: "raw", Conclusion: "done",
		StartedAt: now, CompletedAt: now.Add(time.Second),
	}
	if err := st.PutTaskScheduleRun(run); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetTaskSchedule(schedule.ScheduleID)
	if err != nil || got == nil {
		t.Fatalf("get schedule: got=%#v err=%v", got, err)
	}
	if got.Name != schedule.Name || !got.NextRunAt.Equal(schedule.NextRunAt) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	list, err := st.ListTaskSchedules("user-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: len=%d err=%v", len(list), err)
	}
	if other, err := st.ListTaskSchedules("other"); err != nil || len(other) != 0 {
		t.Fatalf("owner filter: len=%d err=%v", len(other), err)
	}
	runs, err := st.ListTaskScheduleRuns(schedule.ScheduleID, 10)
	if err != nil || len(runs) != 1 || runs[0].Conclusion != "done" {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}

	if err := st.DeleteTaskSchedule(schedule.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetTaskSchedule(schedule.ScheduleID); err != nil || got != nil {
		t.Fatalf("schedule remained after delete: %#v err=%v", got, err)
	}
	runs, err = st.ListTaskScheduleRuns(schedule.ScheduleID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs remained after delete: %#v err=%v", runs, err)
	}
}
