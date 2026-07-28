package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

const workflowsNavPath = "/agentize/debug/workflows"

// RenderWorkflows renders durable deterministic Core workflow runs.
func RenderWorkflows(handler *debuger.DebugHandler, page int) (string, error) {
	workflows, err := handler.GetStore().ListWorkflowRuns("", 200)
	if err != nil {
		return "", fmt.Errorf("failed to get workflows: %w", err)
	}
	total := len(workflows)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, total, components.DefaultItemsPerPage)
	pageItems := workflows[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("Workflow DAGs", "diagram-3-fill", total)
	content += `<p class="text-muted small mb-3">Durable, deterministic Core tool workflows. Immediate workflows request approval per task; scheduled workflows execute the exact approved DAG without a planner LLM or per-run approvals.</p>`
	if total == 0 {
		content += components.InfoAlert("No workflow runs have been recorded yet.")
	} else {
		content += `<div class="table-responsive"><table class="table table-sm table-hover align-middle">
			<thead><tr><th>Workflow</th><th>Owner</th><th>Source</th><th>Tasks</th><th>Status</th><th>Created</th><th></th></tr></thead><tbody>`
		for _, workflow := range pageItems {
			source := `<span class="text-muted">immediate</span>`
			if workflow.ScheduleID != "" {
				source = fmt.Sprintf(
					`<a href="/agentize/debug/schedules/%s">schedule</a>`,
					template.URLQueryEscaper(workflow.ScheduleID),
				)
			}
			content += fmt.Sprintf(`<tr>
				<td><div class="fw-semibold">%s</div><code class="small">%s</code></td>
				<td><a href="/agentize/debug/users/%s">%s</a><div class="small text-muted">%s</div></td>
				<td>%s</td><td>%d</td><td>%s</td>
				<td class="text-nowrap" title="%s">%s</td>
				<td><a class="btn btn-sm btn-outline-primary" href="%s/%s">View DAG</a></td>
			</tr>`,
				template.HTMLEscapeString(workflow.Name),
				template.HTMLEscapeString(workflow.WorkflowID),
				template.URLQueryEscaper(workflow.UserID),
				template.HTMLEscapeString(workflow.UserID),
				template.HTMLEscapeString(workflow.SessionID),
				source, len(workflow.Tasks), workflowStatusBadge(workflow.Status),
				template.HTMLEscapeString(debuger.FormatTime(workflow.CreatedAt)),
				template.HTMLEscapeString(debuger.FormatTimeAgo(workflow.CreatedAt)),
				workflowsNavPath, template.URLQueryEscaper(workflow.WorkflowID),
			)
		}
		content += `</tbody></table></div>`
		content += components.PaginationSimple(page, total, components.DefaultItemsPerPage, workflowsNavPath)
	}
	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Workflows") +
		ui.NavbarAndBody(workflowsNavPath, content) + ui.Footer(), nil
}

// RenderWorkflowDetail renders one workflow's metadata, dependency edges,
// arguments, outputs, and errors.
func RenderWorkflowDetail(handler *debuger.DebugHandler, workflowID string) (string, error) {
	workflow, err := handler.GetStore().GetWorkflowRun(workflowID)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow: %w", err)
	}
	if workflow == nil {
		return "", fmt.Errorf("workflow not found: %s", workflowID)
	}

	content := ui.ContainerStart()
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Workflows", URL: workflowsNavPath},
		{Label: workflow.Name, Active: true},
	})
	content += ui.CardStart("Workflow", "diagram-3-fill")
	content += `<div class="row"><div class="col-md-6"><table class="table table-sm"><tbody>`
	content += workflowMetaRow("Workflow ID", components.InlineCode(workflow.WorkflowID))
	content += workflowMetaRow("Name", template.HTMLEscapeString(workflow.Name))
	content += workflowMetaRow("User", components.TruncatedLink(workflow.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(workflow.UserID), 30))
	content += workflowMetaRow("Session", components.TruncatedLink(workflow.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(workflow.SessionID), 30))
	content += `</tbody></table></div><div class="col-md-6"><table class="table table-sm"><tbody>`
	content += workflowMetaRow("Status", workflowStatusBadge(workflow.Status))
	content += workflowMetaRow("Tasks", fmt.Sprintf("%d", len(workflow.Tasks)))
	content += workflowMetaRow("Schedule", workflowScheduleLink(workflow.ScheduleID))
	content += workflowMetaRow("Created", template.HTMLEscapeString(debuger.FormatTime(workflow.CreatedAt)))
	content += workflowMetaRow("Duration", template.HTMLEscapeString(workflowDuration(workflow)))
	content += `</tbody></table></div></div>`
	if workflow.Error != "" {
		content += `<div class="alert alert-danger mb-0"><strong>Error:</strong> ` +
			template.HTMLEscapeString(workflow.Error) + `</div>`
	}
	content += ui.CardEnd()

	content += ui.CardStartWithCount("Task DAG", "share", len(workflow.Tasks))
	content += `<p class="text-muted small">Dependencies are explicit task IDs. Tasks are displayed in the persisted definition order; runtime state is updated before and after every invocation.</p>`
	content += `<div class="table-responsive"><table class="table table-sm align-middle">
		<thead><tr><th>Task</th><th>Dependencies</th><th>Tool</th><th>Status</th><th>Arguments / Result</th></tr></thead><tbody>`
	for _, task := range workflow.Tasks {
		content += workflowTaskRow(task)
	}
	content += `</tbody></table></div>`
	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Workflow: "+workflow.Name) +
		ui.NavbarAndBody(workflowsNavPath, content) + ui.Footer(), nil
}

func workflowTaskRow(task *model.WorkflowTask) string {
	dependencies := `<span class="text-muted">root</span>`
	if len(task.DependsOn) > 0 {
		var badges []string
		for _, dependency := range task.DependsOn {
			badges = append(badges, components.InlineCode(dependency))
		}
		dependencies = strings.Join(badges, ` <span class="text-muted">+</span> `)
	}
	arguments, _ := json.MarshalIndent(task.Arguments, "", "  ")
	details := `<details><summary>arguments</summary><pre class="mt-2 bg-body-tertiary border rounded p-2 text-wrap">` +
		template.HTMLEscapeString(string(arguments)) + `</pre></details>`
	if task.Output != "" {
		details += `<details class="mt-1"><summary>output</summary><pre class="mt-2 bg-body-tertiary border rounded p-2 text-wrap">` +
			template.HTMLEscapeString(task.Output) + `</pre></details>`
	}
	if task.Error != "" {
		details += `<div class="text-danger small mt-1">` + template.HTMLEscapeString(task.Error) + `</div>`
	}
	return fmt.Sprintf(`<tr>
		<td><div class="fw-semibold">%s</div><code>%s</code></td>
		<td>%s</td><td><code>%s</code></td><td>%s</td><td>%s</td>
	</tr>`,
		template.HTMLEscapeString(task.Name), template.HTMLEscapeString(task.ID),
		dependencies, template.HTMLEscapeString(task.Tool),
		workflowTaskStatusBadge(task.Status), details,
	)
}

func workflowStatusBadge(status model.WorkflowStatus) string {
	color := "secondary"
	switch status {
	case model.WorkflowRunning:
		color = "primary"
	case model.WorkflowSucceeded:
		color = "success"
	case model.WorkflowFailed:
		color = "danger"
	case model.WorkflowCancelled:
		color = "warning"
	}
	return components.Badge(string(status), color)
}

func workflowTaskStatusBadge(status model.WorkflowTaskStatus) string {
	color := "secondary"
	switch status {
	case model.WorkflowTaskRunning:
		color = "primary"
	case model.WorkflowTaskSucceeded:
		color = "success"
	case model.WorkflowTaskFailed:
		color = "danger"
	case model.WorkflowTaskSkipped, model.WorkflowTaskCancelled:
		color = "warning"
	}
	return components.Badge(string(status), color)
}

func workflowMetaRow(label, value string) string {
	if strings.TrimSpace(value) == "" {
		value = `<span class="text-muted">—</span>`
	}
	return fmt.Sprintf(`<tr><th style="width:35%%">%s</th><td>%s</td></tr>`,
		template.HTMLEscapeString(label), value)
}

func workflowScheduleLink(scheduleID string) string {
	if scheduleID == "" {
		return `<span class="text-muted">immediate</span>`
	}
	return fmt.Sprintf(`<a href="/agentize/debug/schedules/%s">%s</a>`,
		template.URLQueryEscaper(scheduleID), template.HTMLEscapeString(scheduleID))
}

func workflowDuration(workflow *model.WorkflowRun) string {
	if workflow.StartedAt.IsZero() {
		return "—"
	}
	end := workflow.CompletedAt
	if end.IsZero() {
		return "running"
	}
	return end.Sub(workflow.StartedAt).String()
}
