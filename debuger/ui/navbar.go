package ui

import (
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
		{"/agentize/debug/plans", "📑", "Plans"},
		{"/agentize/debug/messages", "💬", "Messages"},
		{"/agentize/debug/files", "📁", "Files"},
		{"/agentize/debug/documents", "📄", "Documents"},
		{"/agentize/debug/tool-calls", "🔧", "Tool Calls"},
		{"/agentize/debug/routes", "🧭", "Routes"},
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
// The layout (NavbarAndBody) renders these in the left sidebar.
func AllNavItems() []NavItem {
	navMu.RLock()
	defer navMu.RUnlock()
	items := DefaultNavItems()
	items = append(items, extraNavItems...)
	return items
}
