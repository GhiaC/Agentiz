package pages

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

// RenderDashboard generates the dashboard HTML page.
// sysInfo, when non-nil, renders a "System Info" panel describing the storage
// backends (database, file store) and runtime configuration.
func RenderDashboard(handler *debuger.DebugHandler, sysInfo *debuger.SystemInfo) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	stats, err := dp.GetDashboardStats()
	if err != nil {
		return "", fmt.Errorf("failed to get dashboard stats: %w", err)
	}

	// Documents count (recorded user files). Prefer the value from sysInfo so the
	// dashboard and the System Info panel agree; fall back to a direct query.
	documentCount := 0
	if sysInfo != nil {
		documentCount = sysInfo.TotalDocuments
	} else if files, err := dp.GetAllUserFiles(); err == nil {
		documentCount = len(files)
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

	// Documents card (user-sent + generated files)
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCardWithLink(
		fmt.Sprintf("%d", documentCount),
		"Documents", "📄", "secondary",
		"/agentize/debug/documents", "View Details",
	)
	content += `</div>`

	// Files card (knowledge-tree files opened during sessions)
	content += `<div class="col-md-6 col-lg-4 col-xl-2">`
	content += components.StatCardWithLink(
		fmt.Sprintf("%d", stats.TotalFiles),
		"Opened Files", "📁", "warning",
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

	// System Info panel (database, file store, runtime)
	if sysInfo != nil {
		content += systemInfoCard(sysInfo)
	}

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

	// Documents link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"View All Documents",
		"Browse every file users sent or the agent generated, with preview and download",
		"📄", "/agentize/debug/documents",
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

	// Scheduler link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"Task Scheduler",
		"Create and control persistent recurring agent tasks",
		"⏱️", "/agentize/debug/schedules",
	)
	content += `</div>`

	// Workflows link
	content += `<div class="col-md-6 col-lg-3">`
	content += components.LinkCard(
		"Workflow DAGs",
		"Inspect durable deterministic workflows, dependencies, and task results",
		"🔀", "/agentize/debug/workflows",
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

// systemInfoCard renders the System Info panel: storage backends and runtime
// configuration, with secondary details tucked under a "More info" expander.
func systemInfoCard(info *debuger.SystemInfo) string {
	esc := template.HTMLEscapeString

	row := func(label, valueHTML string) string {
		return fmt.Sprintf(`<tr><td class="fw-bold" style="width: 30%%;">%s</td><td>%s</td></tr>`,
			esc(label), valueHTML)
	}
	location := func(loc string) string {
		if strings.TrimSpace(loc) == "" {
			return `<span class="text-muted">—</span>`
		}
		return components.InlineCode(loc)
	}

	var b strings.Builder
	b.WriteString(`<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-hdd-stack-fill me-2"></i>System Info</h5>
    </div>
    <div class="card-body">
        <table class="table table-sm mb-0"><tbody>`)

	b.WriteString(row("Version", components.InlineCode(info.Version)))
	b.WriteString(row("Database",
		components.BadgeWithIcon(info.Database.Type, "🗄️", backendColor(info.Database.Type))+" "+location(info.Database.Location)))
	b.WriteString(row("File store",
		components.BadgeWithIcon(info.FileStore.Type, "📦", "secondary")+" "+location(info.FileStore.Location)))
	b.WriteString(row("Documents", fmt.Sprintf("%d", info.TotalDocuments)))
	b.WriteString(row("Registered tools", fmt.Sprintf("%d", info.RegisteredTools)))
	b.WriteString(row("Tool approvals", enabledBadge(info.ToolApprovals)))
	b.WriteString(`</tbody></table>`)

	if len(info.More) > 0 {
		b.WriteString(`<details class="mt-3"><summary class="text-muted" style="cursor: pointer;">More info</summary>
        <table class="table table-sm mt-2 mb-0"><tbody>`)
		for _, kv := range info.More {
			b.WriteString(row(kv.Key, esc(kv.Value)))
		}
		b.WriteString(`</tbody></table></details>`)
	}

	b.WriteString(`</div></div>`)
	return b.String()
}

// backendColor maps a backend type to a Bootstrap color for its badge.
func backendColor(backendType string) string {
	switch backendType {
	case "MongoDB":
		return "success"
	case "SQLite":
		return "info"
	case "Local Disk":
		return "primary"
	default:
		return "secondary"
	}
}

func enabledBadge(enabled bool) string {
	if enabled {
		return components.Badge("enabled", "success")
	}
	return components.Badge("disabled", "secondary")
}
