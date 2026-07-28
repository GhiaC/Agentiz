package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowTopologicalOrderStable(t *testing.T) {
	tasks := []*WorkflowTask{
		{ID: "publish", Name: "Publish", Tool: "publish", DependsOn: []string{"draft", "review"}},
		{ID: "draft", Name: "Draft", Tool: "draft"},
		{ID: "review", Name: "Review", Tool: "review", DependsOn: []string{"draft"}},
		{ID: "independent", Name: "Independent", Tool: "notify"},
	}
	got, err := WorkflowTopologicalOrder(tasks)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestWorkflowTopologicalOrderRejectsInvalidDAG(t *testing.T) {
	tests := []struct {
		name   string
		tasks  []*WorkflowTask
		substr string
	}{
		{
			name: "unknown dependency",
			tasks: []*WorkflowTask{
				{ID: "one", Name: "One", Tool: "tool", DependsOn: []string{"missing"}},
			},
			substr: "unknown task",
		},
		{
			name: "cycle",
			tasks: []*WorkflowTask{
				{ID: "one", Name: "One", Tool: "tool", DependsOn: []string{"two"}},
				{ID: "two", Name: "Two", Tool: "tool", DependsOn: []string{"one"}},
			},
			substr: "cycle",
		},
		{
			name: "duplicate id",
			tasks: []*WorkflowTask{
				{ID: "one", Name: "One", Tool: "tool"},
				{ID: "one", Name: "Other", Tool: "tool"},
			},
			substr: "duplicate",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WorkflowTopologicalOrder(tc.tasks)
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Fatalf("error = %v, want substring %q", err, tc.substr)
			}
		})
	}
}
