package ui

import (
	"fmt"
	"html/template"
)

// RenderPage renders a complete HTML page with the given content
func RenderPage(title, navbar, content string) string {
	return Header(title) + navbar + content + Footer()
}

// NavbarAndBody returns the top navbar plus body layout: when extra nav items
// are registered, they appear in a left sidebar and content is in the main area;
// otherwise only the content is rendered (no sidebar).
func NavbarAndBody(currentPage, content string) string {
	nav := Navbar(currentPage)
	extra := SidebarExtra(currentPage)
	if extra == "" {
		return nav + content
	}
	return nav + `<div class="layout-with-sidebar">` + extra +
		`<div class="main-content-with-sidebar">` + content + `</div></div>`
}

// Header generates the HTML header with Bootstrap CDN
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
</head>
<body>`, template.HTMLEscapeString(title), GetStyles())
}

// Footer generates the HTML footer with scripts
func Footer() string {
	return fmt.Sprintf(`
    <script src="%s" integrity="%s" crossorigin="anonymous"></script>
    <script>%s</script>
</body>
</html>`, GetBootstrapJS(), GetBootstrapJSIntegrity(), GetScripts())
}

// ContainerStart returns the opening tags for the main container
func ContainerStart() string {
	return `<div class="container">
    <div class="main-container">`
}

// ContainerEnd returns the closing tags for the main container
func ContainerEnd() string {
	return `    </div>
</div>`
}

// CardStart returns the opening tags for a card with header
func CardStart(title, icon string) string {
	return fmt.Sprintf(`<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-%s me-2"></i>%s</h4>
    </div>
    <div class="card-body">`, icon, template.HTMLEscapeString(title))
}

// CardStartWithCount returns opening tags for a card with count in header
func CardStartWithCount(title, icon string, count int) string {
	return fmt.Sprintf(`<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-%s me-2"></i>%s (%d)</h4>
    </div>
    <div class="card-body">`, icon, template.HTMLEscapeString(title), count)
}

// CardStartWithAction returns opening tags for a card with action button
func CardStartWithAction(title, icon string, count int, actionURL, actionText string) string {
	return fmt.Sprintf(`<div class="card mb-4">
    <div class="card-header d-flex justify-content-between align-items-center">
        <h5 class="mb-0"><i class="bi bi-%s me-2"></i>%s (%d)</h5>
        <a href="%s" class="btn btn-sm btn-light">%s</a>
    </div>
    <div class="card-body">`, icon, template.HTMLEscapeString(title), count, actionURL, actionText)
}

// CardEnd returns the closing tags for a card
func CardEnd() string {
	return `    </div>
</div>`
}

// Row returns a Bootstrap row wrapper
func Row(content string) string {
	return fmt.Sprintf(`<div class="row g-4 mb-4">%s</div>`, content)
}

// Column returns a Bootstrap column wrapper
func Column(size string, content string) string {
	return fmt.Sprintf(`<div class="%s">%s</div>`, size, content)
}
