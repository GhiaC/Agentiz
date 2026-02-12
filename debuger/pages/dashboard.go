package pages

import (
	"fmt"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

// RenderDashboard generates the dashboard HTML page
func RenderDashboard(handler *debuger.DebugHandler) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	stats, err := dp.GetDashboardStats()
	if err != nil {
		return "", fmt.Errorf("failed to get dashboard stats: %w", err)
	}

	content := ui.ContainerStart()

	// Stats cards row
	content += `<div class="row g-4 mb-4">`

	// Users card
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalUsers),
		"Users", "👤", "primary",
		"/agentize/debug/users", "View Details",
	)
	content += `</div>`

	// Sessions card
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCard(
		fmt.Sprintf("%d", stats.TotalSessions),
		"Sessions", "📊", "success",
	)
	content += `</div>`

	// Messages card
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalMessages),
		"Messages", "💬", "info",
		"/agentize/debug/messages", "View Details",
	)
	content += `</div>`

	// Files card
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalFiles),
		"Files", "📁", "warning",
		"/agentize/debug/files", "View Details",
	)
	content += `</div>`

	// Tool Calls card
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalToolCalls),
		"Tool Calls", "🔧", "danger",
		"/agentize/debug/tool-calls", "View Details",
	)
	content += `</div>`

	content += `</div>`

	// Quick links card
	content += `<div class="row">
    <div class="col-12">
        <div class="card">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-link-45deg me-2"></i>Quick Links</h5>
            </div>
            <div class="card-body">
                <div class="row g-3">`

	// Users link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"View All Users",
		"Browse all users and their sessions with detailed information",
		"👤", "/agentize/debug/users",
	)
	content += `</div>`

	// Messages link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"View All Messages",
		"See all messages across all sessions with full context",
		"💬", "/agentize/debug/messages",
	)
	content += `</div>`

	// Files link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"View All Opened Files",
		"Browse all files that were opened during sessions",
		"📁", "/agentize/debug/files",
	)
	content += `</div>`

	// Tool Calls link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"View All Tool Calls",
		"See all tool calls and their results in detail",
		"🔧", "/agentize/debug/tool-calls",
	)
	content += `</div>`

	content += `</div>
            </div>
        </div>
    </div>
</div>`

	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Dashboard") + ui.NavbarAndBody("/agentize/debug", content) + ui.Footer(), nil
}
