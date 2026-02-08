package components

import (
	"fmt"
	"html/template"
)

// BreadcrumbItem represents a breadcrumb navigation item
type BreadcrumbItem struct {
	Label  string
	URL    string
	Active bool
}

// Breadcrumb generates a Bootstrap breadcrumb navigation
func Breadcrumb(items []BreadcrumbItem) string {
	html := `<nav aria-label="breadcrumb" class="mb-4">
    <ol class="breadcrumb">`

	for _, item := range items {
		if item.Active {
			html += fmt.Sprintf(`
        <li class="breadcrumb-item active">%s</li>`, template.HTMLEscapeString(item.Label))
		} else {
			html += fmt.Sprintf(`
        <li class="breadcrumb-item"><a href="%s">%s</a></li>`, item.URL, template.HTMLEscapeString(item.Label))
		}
	}

	html += `
    </ol>
</nav>`

	return html
}

// SimpleBreadcrumb generates a breadcrumb from path segments
// Example: SimpleBreadcrumb("Dashboard", "/agentize/debug", "Users", "/agentize/debug/users", "John")
// Last item is always active
func SimpleBreadcrumb(items ...string) string {
	if len(items) == 0 {
		return ""
	}

	breadcrumbItems := []BreadcrumbItem{}

	for i := 0; i < len(items); i += 2 {
		label := items[i]
		url := ""
		active := i+1 >= len(items) // Last item is active

		if i+1 < len(items) {
			url = items[i+1]
		}

		breadcrumbItems = append(breadcrumbItems, BreadcrumbItem{
			Label:  label,
			URL:    url,
			Active: active,
		})
	}

	// If odd number of items, last one is active without URL
	if len(items)%2 == 1 {
		breadcrumbItems[len(breadcrumbItems)-1].Active = true
	}

	return Breadcrumb(breadcrumbItems)
}
