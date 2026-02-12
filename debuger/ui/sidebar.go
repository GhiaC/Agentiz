package ui

import "fmt"

// Sidebar renders a left sidebar with the given nav items.
// Returns empty string if items is empty, so it can be used unconditionally.
func Sidebar(currentPage string, items []NavItem) string {
	if len(items) == 0 {
		return ""
	}
	html := `<aside class="debug-sidebar">
    <div class="debug-sidebar-title">Extra</div>
    <nav class="debug-sidebar-nav">`
	for _, item := range items {
		active := ""
		if item.URL == currentPage {
			active = " active"
		}
		html += fmt.Sprintf(`
        <a class="debug-sidebar-link%s" href="%s">%s <span>%s</span></a>`,
			active, item.URL, item.Icon, item.Text)
	}
	html += `
    </nav>
</aside>`
	return html
}

// SidebarExtra renders the sidebar with registered extra nav items only.
// Use this in layout when you want the standard "extra items on the left" behavior.
func SidebarExtra(currentPage string) string {
	return Sidebar(currentPage, ExtraNavItems())
}
