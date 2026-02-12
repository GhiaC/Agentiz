package ui

import (
	"fmt"
	"sync"
)

// NavItem represents a navigation item
type NavItem struct {
	URL  string
	Icon string
	Text string
}

var (
	extraNavItems []NavItem
	navMu         sync.RWMutex
)

// RegisterNavItem adds a navigation item that will appear on all debugger pages.
// Call this during initialization (e.g. from AddDebugPage).
func RegisterNavItem(item NavItem) {
	navMu.Lock()
	defer navMu.Unlock()
	// avoid duplicates
	for _, existing := range extraNavItems {
		if existing.URL == item.URL {
			return
		}
	}
	extraNavItems = append(extraNavItems, item)
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

// ExtraNavItems returns only the registered extra (non-default) navigation items.
func ExtraNavItems() []NavItem {
	navMu.RLock()
	defer navMu.RUnlock()
	out := make([]NavItem, len(extraNavItems))
	copy(out, extraNavItems)
	return out
}

// AllNavItems returns default items plus any registered extra items.
func AllNavItems() []NavItem {
	navMu.RLock()
	defer navMu.RUnlock()
	items := DefaultNavItems()
	items = append(items, extraNavItems...)
	return items
}

// Navbar generates the top navigation bar with default items only.
// Extra items (from RegisterNavItem) are shown in the left Sidebar.
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
