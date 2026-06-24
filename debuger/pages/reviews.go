package pages

import (
	"fmt"
	"html/template"
	"net/url"

	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

// RenderReviews renders the pending human-in-the-loop reviews with inline
// approve/reject forms. Both buttons POST to the SAME Resolve endpoint with a
// different decision value — the one entry point every frontend uses.
func RenderReviews(reviews []*model.ReviewRequest) (string, error) {
	content := ui.ContainerStart()
	content += ui.CardStartWithCount("Pending Reviews", "person-check", len(reviews))
	if len(reviews) == 0 {
		content += components.InfoAlert("No pending reviews.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "ID", NoWrap: true},
			{Header: "Kind", NoWrap: true},
			{Header: "Subject", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Title"},
			{Header: "Created", NoWrap: true},
			{Header: "Decision", Center: true, NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped: true, Hover: true, Small: true, Responsive: true, AlignMiddle: true,
		})
		for _, r := range reviews {
			content += components.TableRow([]string{
				template.HTMLEscapeString(r.ID),
				template.HTMLEscapeString(r.Kind),
				template.HTMLEscapeString(r.RefID),
				template.HTMLEscapeString(r.UserID),
				template.HTMLEscapeString(truncateRunes(r.Title, 60)),
				r.CreatedAt.Format("2006-01-02 15:04"),
				reviewDecisionForms(r),
			})
		}
		content += components.TableEnd(true)
	}
	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Reviews") + ui.NavbarAndBody("/agentize/debug/reviews", content) + ui.Footer(), nil
}

// reviewDecisionForms renders the approve/reject forms for one review. Both POST
// to /agentize/debug/reviews/:id/resolve, differing only by the decision value,
// so the dashboard drives approval through the same Resolve path as any UI. The
// ?confirm=<id> typed-confirmation guards against cross-site POST forgery.
func reviewDecisionForms(r *model.ReviewRequest) string {
	action := "/agentize/debug/reviews/" + url.PathEscape(r.ID) + "/resolve?confirm=" + url.QueryEscape(r.ID)
	form := func(decision, label, btnClass string) string {
		return fmt.Sprintf(
			`<form method="POST" action="%s" style="display:inline-block;margin-right:4px">`+
				`<input type="hidden" name="decision" value="%s">`+
				`<button type="submit" class="btn btn-sm %s">%s</button></form>`,
			action, decision, btnClass, template.HTMLEscapeString(label))
	}
	return form("approve", "Approve", "btn-success") + form("reject", "Reject", "btn-outline-danger")
}
