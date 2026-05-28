package pages

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"time"

	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/planning"
)

// RenderPlans renders the list of plans with table and pagination.
func RenderPlans(planStore planning.PlanStore, page int) (string, error) {
	content := ui.ContainerStart()
	if planStore == nil {
		content += components.WarningAlert("Planning is not enabled.")
		content += ui.ContainerEnd()
		return ui.Header("Agentize Debug - Plans") + ui.NavbarAndBody("/agentize/debug/plans", content) + ui.Footer(), nil
	}

	ctx := context.Background()
	allPlans, err := planStore.List(ctx, "")
	if err != nil {
		return "", fmt.Errorf("list plans: %w", err)
	}

	totalItems := len(allPlans)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	if startIdx > len(allPlans) {
		startIdx = len(allPlans)
	}
	if endIdx > len(allPlans) {
		endIdx = len(allPlans)
	}
	paginatedPlans := allPlans[startIdx:endIdx]

	content += ui.CardStartWithCount("All Plans", "journal-text", totalItems)
	if totalItems == 0 {
		content += components.InfoAlert("No plans found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Plan ID", NoWrap: true},
			{Header: "User ID", NoWrap: true},
			{Header: "Session ID", NoWrap: true},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Steps", Center: true, NoWrap: true},
			{Header: "Created", NoWrap: true},
			{Header: "Updated", NoWrap: true},
			{Header: "", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped: true, Hover: true, Small: true, Responsive: true, AlignMiddle: true,
		})
		for _, p := range paginatedPlans {
			statusVariant := planStatusBadgeVariant(p.Status)
			statusBadge := components.Badge(string(p.Status), statusVariant)
			stepsCount := 0
			if p.Steps != nil {
				stepsCount = len(p.Steps)
			}
			detailURL := "/agentize/debug/plans/" + url.PathEscape(p.ID)
			content += components.TableRow([]string{
				template.HTMLEscapeString(p.ID),
				template.HTMLEscapeString(p.UserID),
				template.HTMLEscapeString(p.SessionID),
				statusBadge,
				fmt.Sprintf("%d", stepsCount),
				p.CreatedAt.Format("2006-01-02 15:04"),
				p.UpdatedAt.Format("2006-01-02 15:04"),
				fmt.Sprintf(`<a href="%s" class="btn btn-sm btn-outline-primary">View</a>`, detailURL),
			})
		}
		content += components.TableEnd(true)
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/plans")
	}
	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Plans") + ui.NavbarAndBody("/agentize/debug/plans", content) + ui.Footer(), nil
}

// RenderPlanDetail renders the detail view for a single plan.
func RenderPlanDetail(planStore planning.PlanStore, planID string) (string, error) {
	content := ui.ContainerStart()
	if planStore == nil {
		content += components.WarningAlert("Planning is not enabled.")
		content += ui.ContainerEnd()
		return ui.Header("Agentize Debug - Plan") + ui.NavbarAndBody("/agentize/debug/plans", content) + ui.Footer(), nil
	}

	ctx := context.Background()
	plan, err := planStore.Get(ctx, planID)
	if err != nil {
		if err == planning.ErrPlanNotFound {
			content += components.WarningAlert("Plan not found.")
			content += ui.ContainerEnd()
			return ui.Header("Agentize Debug - Plan") + ui.NavbarAndBody("/agentize/debug/plans", content) + ui.Footer(), nil
		}
		return "", err
	}

	content += components.SimpleBreadcrumb("Dashboard", "/agentize/debug", "Plans", "/agentize/debug/plans", planID)

	// Plan info card
	content += ui.CardStart("Plan: "+plan.ID, "journal-text")
	statusVariant := planStatusBadgeVariant(plan.Status)
	content += `<dl class="row mb-0">`
	content += fmt.Sprintf(`<dt class="col-sm-2">ID</dt><dd class="col-sm-10">%s</dd>`, template.HTMLEscapeString(plan.ID))
	content += fmt.Sprintf(`<dt class="col-sm-2">User ID</dt><dd class="col-sm-10"><a href="/agentize/debug/users/%s">%s</a></dd>`, url.PathEscape(plan.UserID), template.HTMLEscapeString(plan.UserID))
	content += fmt.Sprintf(`<dt class="col-sm-2">Session ID</dt><dd class="col-sm-10"><a href="/agentize/debug/sessions">%s</a></dd>`, template.HTMLEscapeString(plan.SessionID))
	content += fmt.Sprintf(`<dt class="col-sm-2">Status</dt><dd class="col-sm-10">%s</dd>`, components.Badge(string(plan.Status), statusVariant))
	content += fmt.Sprintf(`<dt class="col-sm-2">Input</dt><dd class="col-sm-10">%s</dd>`, template.HTMLEscapeString(plan.Input))
	content += fmt.Sprintf(`<dt class="col-sm-2">Created</dt><dd class="col-sm-10">%s</dd>`, plan.CreatedAt.Format(time.RFC3339))
	content += fmt.Sprintf(`<dt class="col-sm-2">Updated</dt><dd class="col-sm-10">%s</dd>`, plan.UpdatedAt.Format(time.RFC3339))
	if plan.CompletedAt != nil {
		content += fmt.Sprintf(`<dt class="col-sm-2">Completed</dt><dd class="col-sm-10">%s</dd>`, plan.CompletedAt.Format(time.RFC3339))
	}
	if plan.Output != "" {
		content += fmt.Sprintf(`<dt class="col-sm-2">Output</dt><dd class="col-sm-10">%s</dd>`, template.HTMLEscapeString(plan.Output))
	}
	if plan.Error != "" {
		content += fmt.Sprintf(`<dt class="col-sm-2">Error</dt><dd class="col-sm-10 text-danger">%s</dd>`, template.HTMLEscapeString(plan.Error))
	}
	content += `</dl>`
	content += ui.CardEnd()

	// System prompts card
	if len(plan.SystemPrompts) > 0 {
		content += ui.CardStartWithCount("System Prompts", "chat-left-text", len(plan.SystemPrompts))
		for i, sp := range plan.SystemPrompts {
			preview := sp
			if len(preview) > 500 {
				preview = preview[:500] + "…"
			}
			content += fmt.Sprintf(`<div class="mb-3"><h6 class="text-muted">Prompt %d</h6><pre class="bg-light p-2 rounded" style="white-space:pre-wrap;max-height:300px;overflow-y:auto;font-size:0.82rem;">%s</pre></div>`,
				i+1, template.HTMLEscapeString(preview))
		}
		content += ui.CardEnd()
	}

	// Steps table
	content += ui.CardStartWithCount("Steps", "list-check", len(plan.Steps))
	if len(plan.Steps) > 0 {
		columns := []components.ColumnConfig{
			{Header: "Step ID", NoWrap: true},
			{Header: "Type", NoWrap: true},
			{Header: "Name", NoWrap: true},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Started", NoWrap: true},
			{Header: "Duration", NoWrap: true},
			{Header: "Result preview", NoWrap: false},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped: true, Hover: true, Small: true, Responsive: true, AlignMiddle: true,
		})
		for _, s := range plan.Steps {
			stepStatusVariant := stepStatusBadgeVariant(s.Status)
			started := "-"
			if s.StartedAt != nil {
				started = s.StartedAt.Format("15:04:05")
			}
			dur := "-"
			if s.Result != nil && s.Result.Duration > 0 {
				dur = s.Result.Duration.String()
			}
			preview := ""
			if s.Result != nil && s.Result.Output != "" {
				if len(s.Result.Output) > 100 {
					preview = template.HTMLEscapeString(s.Result.Output[:100]) + "..."
				} else {
					preview = template.HTMLEscapeString(s.Result.Output)
				}
			}
			content += components.TableRow([]string{
				template.HTMLEscapeString(s.ID),
				template.HTMLEscapeString(string(s.Type)),
				template.HTMLEscapeString(s.Name),
				components.Badge(string(s.Status), stepStatusVariant),
				started,
				dur,
				preview,
			})
		}
		content += components.TableEnd(true)
	} else {
		content += components.InfoAlert("No steps.")
	}
	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Plan "+planID) + ui.NavbarAndBody("/agentize/debug/plans", content) + ui.Footer(), nil
}

func planStatusBadgeVariant(s planning.PlanStatus) string {
	switch s {
	case planning.PlanPending, planning.PlanRunning:
		return "warning"
	case planning.PlanCompleted:
		return "success"
	case planning.PlanFailed:
		return "danger"
	case planning.PlanCancelled:
		return "secondary"
	default:
		return "secondary"
	}
}

func stepStatusBadgeVariant(s planning.StepStatus) string {
	switch s {
	case planning.StepPending, planning.StepRunning:
		return "warning"
	case planning.StepCompleted:
		return "success"
	case planning.StepFailed:
		return "danger"
	case planning.StepSkipped:
		return "secondary"
	default:
		return "secondary"
	}
}
