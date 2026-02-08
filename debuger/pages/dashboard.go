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

	html := ui.Header("Agentize Debug - Dashboard")
	html += ui.Navbar("/agentize/debug")
	html += ui.ContainerStart()

	// Stats cards row
	html += `<div class="row g-4 mb-4">`

	// Users card
	html += `<div class="col-md-6 col-lg-4 col-xl-2">`
	html += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalUsers),
		"Users", "👤", "primary",
		"/agentize/debug/users", "View Details",
	)
	html += `</div>`

	// Sessions card
	html += `<div class="col-md-6 col-lg-4 col-xl-2">`
	html += components.StatCard(
		fmt.Sprintf("%d", stats.TotalSessions),
		"Sessions", "📊", "success",
	)
	html += `</div>`

	// Messages card
	html += `<div class="col-md-6 col-lg-4 col-xl-2">`
	html += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalMessages),
		"Messages", "💬", "info",
		"/agentize/debug/messages", "View Details",
	)
	html += `</div>`

	// Files card
	html += `<div class="col-md-6 col-lg-4 col-xl-2">`
	html += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalFiles),
		"Files", "📁", "warning",
		"/agentize/debug/files", "View Details",
	)
	html += `</div>`

	// Tool Calls card
	html += `<div class="col-md-6 col-lg-4 col-xl-2">`
	html += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalToolCalls),
		"Tool Calls", "🔧", "danger",
		"/agentize/debug/tool-calls", "View Details",
	)
	html += `</div>`

	html += `</div>`

	// Quick links card
	html += `<div class="row">
    <div class="col-12">
        <div class="card">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-link-45deg me-2"></i>Quick Links</h5>
            </div>
            <div class="card-body">
                <div class="row g-3">`

	// Users link
	html += `<div class="col-md-6 col-lg-3">`
	html += components.LinkCard(
		"View All Users",
		"Browse all users and their sessions with detailed information",
		"👤", "/agentize/debug/users",
	)
	html += `</div>`

	// Messages link
	html += `<div class="col-md-6 col-lg-3">`
	html += components.LinkCard(
		"View All Messages",
		"See all messages across all sessions with full context",
		"💬", "/agentize/debug/messages",
	)
	html += `</div>`

	// Files link
	html += `<div class="col-md-6 col-lg-3">`
	html += components.LinkCard(
		"View All Opened Files",
		"Browse all files that were opened during sessions",
		"📁", "/agentize/debug/files",
	)
	html += `</div>`

	// Tool Calls link
	html += `<div class="col-md-6 col-lg-3">`
	html += components.LinkCard(
		"View All Tool Calls",
		"See all tool calls and their results in detail",
		"🔧", "/agentize/debug/tool-calls",
	)
	html += `</div>`

	html += `</div>
            </div>
        </div>
    </div>
</div>`

	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
