package ui

import (
	"fmt"
)

// NavItem represents a navigation item
type NavItem struct {
	URL  string
	Icon string
	Text string
}

// DefaultNavItems returns the default navigation items
func DefaultNavItems() []NavItem {
	return []NavItem{
		{"/agentize/debug", "📊", "Dashboard"},
		{"/agentize/debug/users", "👤", "Users"},
		{"/agentize/debug/sessions", "📋", "Sessions"},
		{"/agentize/debug/messages", "💬", "Messages"},
		{"/agentize/debug/files", "📁", "Files"},
		{"/agentize/debug/tool-calls", "🔧", "Tool Calls"},
		{"/agentize/debug/summarized", "📝", "Summarized"},
	}
}

// Navbar generates the Bootstrap navigation bar
func Navbar(currentPage string) string {
	return NavbarWithItems(currentPage, DefaultNavItems())
}

// NavbarWithItems generates the navigation bar with custom items
func NavbarWithItems(currentPage string, items []NavItem) string {
	html := `<nav class="navbar navbar-expand-lg navbar-dark" style="background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); box-shadow: 0 2px 10px rgba(0,0,0,0.1);">
    <div class="container-fluid">
        <a class="navbar-brand fw-bold" href="/agentize/debug">
            <i class="bi bi-bug-fill me-2"></i>Agentize Debug
        </a>
        <button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#navbarNav" aria-controls="navbarNav" aria-expanded="false" aria-label="Toggle navigation">
            <span class="navbar-toggler-icon"></span>
        </button>
        <div class="collapse navbar-collapse" id="navbarNav">
            <ul class="navbar-nav ms-auto">`

	for _, item := range items {
		active := ""
		if item.URL == currentPage {
			active = "active fw-bold"
		}
		html += fmt.Sprintf(`
                <li class="nav-item">
                    <a class="nav-link %s" href="%s">%s %s</a>
                </li>`, active, item.URL, item.Icon, item.Text)
	}

	html += `
            </ul>
        </div>
    </div>
</nav>`

	return html
}
