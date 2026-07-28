package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

const schedulesNavPath = "/agentize/debug/schedules"

// RenderTaskSchedules renders the dedicated scheduler management page.
func RenderTaskSchedules(
	schedules []*model.TaskSchedule,
	workerRunning bool,
	notice string,
	pageError string,
) string {
	content := ui.ContainerStart()
	if notice != "" {
		content += components.SuccessAlert(notice)
	}
	if pageError != "" {
		content += components.DangerAlert(pageError)
	}

	workerState := `<span class="badge text-bg-danger">stopped</span>`
	if workerRunning {
		workerState = `<span class="badge text-bg-success">running</span>`
	}
	content += ui.CardStart("Task Scheduler", "clock-history")
	content += fmt.Sprintf(
		`<div class="d-flex flex-wrap justify-content-between gap-2 align-items-center">
			<p class="mb-0 text-muted">Persistent tasks with an isolated memory session. Prompt schedules stay on one fixed agent; workflow schedules run an exact DAG.</p>
			<div>Worker: %s &nbsp; Schedules: <strong>%d</strong></div>
		</div>`,
		workerState, len(schedules),
	)
	content += ui.CardEnd()

	content += renderScheduleCreateForm()
	content += renderScheduleTable(schedules)
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Scheduler") +
		ui.NavbarAndBody(schedulesNavPath, content) + ui.Footer()
}

func renderScheduleCreateForm() string {
	var b strings.Builder
	b.WriteString(ui.CardStart("Create Schedule", "calendar-plus"))
	b.WriteString(`<form method="post" action="/agentize/debug/schedules">
		<div class="row g-3">
			<div class="col-md-4">
				<label class="form-label">Name</label>
				<input class="form-control" name="name" required maxlength="120" placeholder="Monitor inventory">
			</div>
			<div class="col-md-4">
				<label class="form-label">User ID</label>
				<input class="form-control" name="user_id" required placeholder="user-123">
			</div>
			<div class="col-md-4">
				<label class="form-label">Source worker session ID</label>
				<input class="form-control" name="session_id" required placeholder="user-123-researcher-s0001">
				<div class="form-text">A new dedicated session of the same agent type is created automatically.</div>
			</div>
			<div class="col-md-3">
				<label class="form-label">Interval (seconds)</label>
				<input class="form-control" type="number" name="interval_seconds" min="1" max="31536000" value="300" required>
			</div>
			<div class="col-md-3">
				<label class="form-label">Max runs <span class="text-muted">(0 = unlimited)</span></label>
				<input class="form-control" type="number" name="max_runs" min="0" max="1000000" value="0">
			</div>
			<div class="col-md-4">
				<label class="form-label">Conclusion model <span class="text-muted">(optional)</span></label>
				<input class="form-control" name="conclusion_model" placeholder="openai/gpt-5-nano">
			</div>
			<div class="col-md-5">
				<label class="form-label">Conclusion instruction <span class="text-muted">(optional)</span></label>
				<input class="form-control" name="conclusion_prompt" placeholder="Report only meaningful changes and alerts">
			</div>
			<div class="col-12">
				<label class="form-label">Task prompt</label>
				<textarea class="form-control" name="prompt" rows="3" required placeholder="Check the data source and report changes..."></textarea>
			</div>
			<div class="col-12">
				<button class="btn btn-primary" type="submit"><i class="bi bi-plus-lg me-1"></i>Create schedule</button>
			</div>
		</div>
	</form>`)
	b.WriteString(ui.CardEnd())
	return b.String()
}

func renderScheduleTable(schedules []*model.TaskSchedule) string {
	content := ui.CardStartWithCount("Schedules", "calendar3", len(schedules))
	if len(schedules) == 0 {
		content += components.InfoAlert("No task schedules exist yet. Create one above or ask the LLM to use manage_schedules.")
		content += ui.CardEnd()
		return content
	}

	columns := []components.ColumnConfig{
		{Header: "Schedule"},
		{Header: "Owner"},
		{Header: "Kind", Center: true, NoWrap: true},
		{Header: "Status", Center: true, NoWrap: true},
		{Header: "Interval", NoWrap: true},
		{Header: "Next run", NoWrap: true},
		{Header: "Last run", NoWrap: true},
		{Header: "Conclusion model"},
		{Header: "Actions", Center: true, NoWrap: true},
	}
	content += components.TableStartWithConfig(columns, components.TableConfig{
		Hover: true, Small: true, Responsive: true, AlignMiddle: true,
	})
	for _, schedule := range schedules {
		content += renderScheduleRow(schedule)
	}
	content += components.TableEnd(true)
	content += ui.CardEnd()
	return content
}

func renderScheduleRow(schedule *model.TaskSchedule) string {
	esc := template.HTMLEscapeString
	statusColor := "success"
	if schedule.Status == model.TaskSchedulePaused {
		statusColor = "secondary"
	} else if schedule.Status == model.TaskScheduleCompleted {
		statusColor = "info"
	}
	kind := "prompt"
	if len(schedule.WorkflowTasks) > 0 {
		kind = "workflow"
	}
	runStatus := `<span class="text-muted">never</span>`
	if schedule.LastRunStatus != "" {
		color := "success"
		if schedule.LastRunStatus == model.TaskRunFailed {
			color = "danger"
		} else if schedule.LastRunStatus == model.TaskRunCancelled {
			color = "warning"
		} else if schedule.LastRunStatus == model.TaskRunRunning {
			color = "primary"
		}
		runStatus = fmt.Sprintf(`<span class="badge text-bg-%s">%s</span> <span class="text-muted">#%d</span>`,
			color, esc(string(schedule.LastRunStatus)), schedule.RunCount)
	}
	modelName := `<span class="text-muted">disabled</span>`
	if schedule.ConclusionModel != "" {
		modelName = components.InlineCode(schedule.ConclusionModel)
	}
	nextRunTitle := formatScheduleTime(schedule.NextRunAt)
	nextRunText := formatScheduleRelativeTime(schedule.NextRunAt)
	if schedule.Status == model.TaskScheduleCompleted {
		nextRunTitle = "completed"
		nextRunText = "—"
	}

	var actions strings.Builder
	actions.WriteString(`<div class="d-flex gap-1 justify-content-center">`)
	if schedule.Status == model.TaskScheduleActive {
		actions.WriteString(scheduleActionForm(schedule.ScheduleID, "run-now", "Run", "outline-primary", false))
		actions.WriteString(scheduleActionForm(schedule.ScheduleID, "stop", "Stop", "outline-warning", false))
	} else if schedule.Status == model.TaskSchedulePaused {
		actions.WriteString(scheduleActionForm(schedule.ScheduleID, "resume", "Resume", "outline-success", false))
	}
	actions.WriteString(fmt.Sprintf(
		`<a class="btn btn-sm btn-outline-secondary" href="%s/%s">Details</a>`,
		schedulesNavPath, template.URLQueryEscaper(schedule.ScheduleID),
	))
	actions.WriteString(scheduleActionForm(schedule.ScheduleID, "delete", "Delete", "outline-danger", true))
	actions.WriteString(`</div>`)

	return fmt.Sprintf(`<tr>
		<td><div class="fw-semibold">%s</div><code class="small">%s</code></td>
		<td><div>%s</div><div class="small text-muted">%s</div></td>
		<td class="text-center"><span class="badge text-bg-dark">%s</span></td>
		<td class="text-center"><span class="badge text-bg-%s">%s</span></td>
		<td>%s</td>
		<td title="%s">%s</td>
		<td>%s</td>
		<td>%s</td>
		<td>%s</td>
	</tr>`,
		esc(schedule.Name), esc(schedule.ScheduleID),
		esc(schedule.UserID), esc(schedule.SessionID),
		esc(kind),
		statusColor, esc(string(schedule.Status)),
		esc(schedule.Interval().String()),
		esc(nextRunTitle), esc(nextRunText),
		runStatus, modelName, actions.String(),
	)
}

func scheduleActionForm(scheduleID, action, label, color string, confirm bool) string {
	confirmAttr := ""
	if confirm {
		confirmAttr = ` onsubmit="return confirm('Delete this schedule and all of its run history?')"`
	}
	return fmt.Sprintf(
		`<form method="post" action="%s/%s/%s"%s><button class="btn btn-sm btn-%s" type="submit">%s</button></form>`,
		schedulesNavPath, template.URLQueryEscaper(scheduleID), action,
		confirmAttr, color, template.HTMLEscapeString(label),
	)
}

// RenderTaskScheduleDetail renders configuration, latest output, and run history.
func RenderTaskScheduleDetail(schedule *model.TaskSchedule, runs []*model.TaskScheduleRun) string {
	esc := template.HTMLEscapeString
	content := ui.ContainerStart()
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Scheduler", URL: schedulesNavPath},
		{Label: schedule.Name, Active: true},
	})
	content += ui.CardStart("Schedule Configuration", "calendar3")
	content += `<div class="row"><div class="col-md-6"><table class="table table-sm"><tbody>`
	content += scheduleMetaRow("Schedule ID", components.InlineCode(schedule.ScheduleID))
	content += scheduleMetaRow("Name", esc(schedule.Name))
	content += scheduleMetaRow("User", esc(schedule.UserID))
	content += scheduleMetaRow("Dedicated session", components.TruncatedLink(schedule.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(schedule.SessionID), 30))
	content += scheduleMetaRow("Source session", esc(schedule.SourceSessionID))
	content += scheduleMetaRow("Agent type", esc(string(schedule.AgentType)))
	content += scheduleMetaRow("Status", esc(string(schedule.Status)))
	content += `</tbody></table></div><div class="col-md-6"><table class="table table-sm"><tbody>`
	kind := "prompt"
	if len(schedule.WorkflowTasks) > 0 {
		kind = "workflow"
	}
	content += scheduleMetaRow("Kind", esc(kind))
	content += scheduleMetaRow("Interval", esc(schedule.Interval().String()))
	runCount := fmt.Sprintf("%d", schedule.RunCount)
	if schedule.MaxRuns > 0 {
		runCount = fmt.Sprintf("%d / %d", schedule.RunCount, schedule.MaxRuns)
	}
	content += scheduleMetaRow("Run count", runCount)
	content += scheduleMetaRow("Next run", esc(formatScheduleTime(schedule.NextRunAt)))
	content += scheduleMetaRow("Latest workflow", scheduleWorkflowLink(schedule.LastWorkflowID))
	content += scheduleMetaRow("Conclusion model", esc(schedule.ConclusionModel))
	content += scheduleMetaRow("Updated", esc(formatScheduleTime(schedule.UpdatedAt)))
	content += `</tbody></table></div></div>`
	if len(schedule.WorkflowTasks) > 0 {
		workflowJSON, _ := json.MarshalIndent(schedule.WorkflowTasks, "", "  ")
		content += `<h6>Fixed workflow DAG</h6><pre class="bg-body-tertiary border rounded p-3 text-wrap">` + esc(string(workflowJSON)) + `</pre>`
	} else {
		content += `<h6>Task prompt</h6><pre class="bg-body-tertiary border rounded p-3 text-wrap">` + esc(schedule.Prompt) + `</pre>`
	}
	if schedule.ConclusionPrompt != "" {
		content += `<h6>Conclusion instruction</h6><pre class="bg-body-tertiary border rounded p-3 text-wrap">` + esc(schedule.ConclusionPrompt) + `</pre>`
	}
	content += ui.CardEnd()

	if schedule.LastConclusion != "" || schedule.LastOutput != "" || schedule.LastError != "" {
		content += ui.CardStart("Latest Result", "activity")
		if schedule.LastError != "" {
			content += `<div class="alert alert-danger">` + esc(schedule.LastError) + `</div>`
		}
		if schedule.LastConclusion != "" {
			content += `<h6>Cheap-model conclusion</h6><pre class="bg-body-tertiary border rounded p-3 text-wrap">` + esc(schedule.LastConclusion) + `</pre>`
		}
		if schedule.LastOutput != "" {
			content += `<details><summary class="mb-2">Raw agent output</summary><pre class="bg-body-tertiary border rounded p-3 text-wrap">` + esc(schedule.LastOutput) + `</pre></details>`
		}
		content += ui.CardEnd()
	}

	content += ui.CardStartWithCount("Run History", "list-ol", len(runs))
	if len(runs) == 0 {
		content += components.InfoAlert("This schedule has not run yet.")
	} else {
		content += `<div class="table-responsive"><table class="table table-sm align-middle"><thead><tr>
			<th>Started</th><th>Status</th><th>Duration</th><th>Workflow</th><th>Tokens</th><th>Result</th>
		</tr></thead><tbody>`
		for _, run := range runs {
			content += renderScheduleRunRow(run)
		}
		content += `</tbody></table></div>`
	}
	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Schedule: "+schedule.Name) +
		ui.NavbarAndBody(schedulesNavPath, content) + ui.Footer()
}

func renderScheduleRunRow(run *model.TaskScheduleRun) string {
	esc := template.HTMLEscapeString
	color := "success"
	if run.Status == model.TaskRunFailed {
		color = "danger"
	} else if run.Status == model.TaskRunCancelled {
		color = "warning"
	} else if run.Status == model.TaskRunRunning {
		color = "primary"
	}
	duration := "—"
	if !run.CompletedAt.IsZero() {
		duration = run.CompletedAt.Sub(run.StartedAt).Round(time.Millisecond).String()
	}
	result := run.Conclusion
	if result == "" {
		result = run.Output
	}
	if run.Error != "" {
		result = run.Error
	}
	fullResult := truncateRunes(result, 8000)
	return fmt.Sprintf(`<tr>
		<td class="text-nowrap">%s</td>
		<td><span class="badge text-bg-%s">%s</span></td>
		<td class="text-nowrap">%s</td>
		<td>%s</td>
		<td class="text-nowrap">%d / %d</td>
		<td><details><summary>%s</summary><pre class="mt-2 bg-body-tertiary border rounded p-2 text-wrap">%s</pre></details></td>
	</tr>`,
		esc(formatScheduleTime(run.StartedAt)), color, esc(string(run.Status)),
		esc(duration), scheduleWorkflowLink(run.WorkflowID), run.PromptTokens, run.CompletionTokens,
		esc(truncateRunes(result, 90)), esc(fullResult),
	)
}

func scheduleWorkflowLink(workflowID string) string {
	if workflowID == "" {
		return `<span class="text-muted">—</span>`
	}
	return fmt.Sprintf(`<a href="/agentize/debug/workflows/%s"><code>%s</code></a>`,
		template.URLQueryEscaper(workflowID), template.HTMLEscapeString(workflowID))
}

func scheduleMetaRow(label, value string) string {
	if strings.TrimSpace(value) == "" {
		value = `<span class="text-muted">—</span>`
	}
	return fmt.Sprintf(`<tr><th style="width:35%%">%s</th><td>%s</td></tr>`,
		template.HTMLEscapeString(label), value)
}

func formatScheduleTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return debuger.FormatTime(value)
}

func formatScheduleRelativeTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	delta := time.Until(value)
	if delta <= 0 {
		return "due now"
	}
	return "in " + delta.Round(time.Second).String()
}
