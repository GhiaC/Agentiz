package ui

import (
	"fmt"
	"html/template"
)

// RenderPage renders a complete HTML page with the given content.
func RenderPage(title, navbar, content string) string {
	return Header(title) + navbar + content + Footer()
}

// NavbarAndBody renders the full app shell: a left sidebar (brand + grouped
// nav) and a main column with a slim topbar and the scrollable content. Every
// debug page is rendered through this, so the layout stays consistent.
func NavbarAndBody(currentPage, content string) string {
	return `<div class="app" id="app">` +
		sidebar(currentPage) +
		`<div class="sidebar-backdrop" id="sidebar-backdrop"></div>` +
		`<main class="main">` +
		topbar(currentPage) +
		`<div class="content-scroll">` + content + `</div>` +
		`</main></div>`
}

// sidebar builds the left navigation: brand, the default nav items under a
// "Debug" label, any registered extra items under an "Extra" label, and footer
// links back to the docs/graph views.
func sidebar(currentPage string) string {
	html := `<aside class="sidebar" id="sidebar">
    <a class="sidebar-brand" href="/agentize/debug">
        <span class="brand-mark">🐞</span>
        <span>
            <span class="brand-title">Agentize</span>
            <div class="brand-sub">Debug Console</div>
        </span>
    </a>
    <nav class="sidebar-nav">`

	html += `<div class="nav-section-label">Debug</div>`
	html += navLinks(currentPage, DefaultNavItems())

	if extra := ExtraNavItems(); len(extra) > 0 {
		html += `<div class="nav-section-label">Extra</div>`
		html += navLinks(currentPage, extra)
	}

	html += `</nav>
    <div class="sidebar-foot">
        <a href="/agentize">Docs</a>
        <a href="/agentize/graph">Graph</a>
    </div>
</aside>`
	return html
}

// navLinks renders a list of sidebar links, marking the active one.
func navLinks(currentPage string, items []NavItem) string {
	html := ""
	for _, item := range items {
		active := ""
		if item.URL == currentPage {
			active = " active"
		}
		html += fmt.Sprintf(
			`<a class="nav-link-item%s" href="%s"><span class="nav-ico">%s</span><span>%s</span></a>`,
			active, item.URL, item.Icon, template.HTMLEscapeString(item.Text))
	}
	return html
}

// topbar renders the page header: a sidebar toggle (mobile), the page title
// (derived from the active nav item) and a theme toggle.
func topbar(currentPage string) string {
	return `<header class="topbar">
        <button id="sidebar-toggle" class="icon-btn" title="Toggle navigation" aria-label="Toggle navigation">☰</button>
        <div class="topbar-title">` + template.HTMLEscapeString(navTitle(currentPage)) + `</div>
        <div class="topbar-actions">
            <button id="theme-toggle" class="icon-btn" title="Toggle theme" aria-label="Toggle theme">◐</button>
        </div>
    </header>`
}

// navTitle returns the human label for the current page, matching against all
// nav items; falls back to "Debug".
func navTitle(currentPage string) string {
	for _, item := range AllNavItems() {
		if item.URL == currentPage {
			return item.Text
		}
	}
	return "Debug"
}

// Header generates the HTML head + opening body, including the Bootstrap CDN,
// our stylesheet and an early theme-init script (shared 'agentize-theme' key
// with the docs UI) to avoid a flash of the wrong theme.
func Header(title string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/css/bootstrap.min.css" rel="stylesheet" integrity="sha384-T3c6CoIi6uLrA9TneNEoa7RxnatzjcDSCmG1MXxSR1GAsXEV/Dwwykc2MPK8M2HN" crossorigin="anonymous">
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.1/font/bootstrap-icons.css">
    <style>%s</style>
    <script>
        (function () {
            var saved = null;
            try { saved = localStorage.getItem('agentize-theme'); } catch (e) {}
            var theme = saved || (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
            document.documentElement.setAttribute('data-bs-theme', theme);
        })();
    </script>
</head>
<body>`, template.HTMLEscapeString(title), GetStyles())
}

// Footer generates the closing scripts and tags.
func Footer() string {
	return fmt.Sprintf(`
    <script src="%s" integrity="%s" crossorigin="anonymous"></script>
    <script>%s</script>
</body>
</html>`, GetBootstrapJS(), GetBootstrapJSIntegrity(), GetScripts())
}

// ContainerStart returns the opening tags for the main container.
func ContainerStart() string {
	return `<div class="container">
    <div class="main-container">`
}

// ContainerEnd returns the closing tags for the main container.
func ContainerEnd() string {
	return `    </div>
</div>`
}

// CardStart returns the opening tags for a card with header.
func CardStart(title, icon string) string {
	return fmt.Sprintf(`<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-%s me-2"></i>%s</h4>
    </div>
    <div class="card-body">`, icon, template.HTMLEscapeString(title))
}

// CardStartWithCount returns opening tags for a card with count in header.
func CardStartWithCount(title, icon string, count int) string {
	return fmt.Sprintf(`<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-%s me-2"></i>%s (%d)</h4>
    </div>
    <div class="card-body">`, icon, template.HTMLEscapeString(title), count)
}

// CardStartWithAction returns opening tags for a card with action button.
func CardStartWithAction(title, icon string, count int, actionURL, actionText string) string {
	return fmt.Sprintf(`<div class="card mb-4">
    <div class="card-header d-flex justify-content-between align-items-center">
        <h5 class="mb-0"><i class="bi bi-%s me-2"></i>%s (%d)</h5>
        <a href="%s" class="btn btn-sm btn-light">%s</a>
    </div>
    <div class="card-body">`, icon, template.HTMLEscapeString(title), count, actionURL, actionText)
}

// CardEnd returns the closing tags for a card.
func CardEnd() string {
	return `    </div>
</div>`
}

// Row returns a Bootstrap row wrapper.
func Row(content string) string {
	return fmt.Sprintf(`<div class="row g-4 mb-4">%s</div>`, content)
}

// Column returns a Bootstrap column wrapper.
func Column(size string, content string) string {
	return fmt.Sprintf(`<div class="%s">%s</div>`, size, content)
}
