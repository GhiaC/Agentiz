package components

import (
	"fmt"
	"html/template"
)

// StatCard generates a statistics card
func StatCard(value, label, icon, color string) string {
	return fmt.Sprintf(`
<div class="card text-center h-100 border-%s">
    <div class="card-body d-flex flex-column justify-content-center">
        <h2 class="card-title text-%s mb-3" style="font-size: 2.5rem; font-weight: bold;">%s</h2>
        <p class="card-text mb-3" style="font-size: 1.1rem;">%s %s</p>
    </div>
</div>`, color, color, template.HTMLEscapeString(value), icon, template.HTMLEscapeString(label))
}

// StatCardWithLink generates a statistics card with a link button
func StatCardWithLink(value, label, icon, color, linkURL, linkText string) string {
	return fmt.Sprintf(`
<div class="card text-center h-100 border-%s">
    <div class="card-body d-flex flex-column justify-content-center">
        <h2 class="card-title text-%s mb-3" style="font-size: 2.5rem; font-weight: bold;">%s</h2>
        <p class="card-text mb-3" style="font-size: 1.1rem;">%s %s</p>
        <a href="%s" class="btn btn-sm btn-outline-%s mt-auto">%s</a>
    </div>
</div>`, color, color, template.HTMLEscapeString(value), icon, template.HTMLEscapeString(label), linkURL, color, linkText)
}

// StatCardWithSubtext generates a statistics card with subtext
func StatCardWithSubtext(value, label, icon, color, subtext string) string {
	return fmt.Sprintf(`
<div class="card text-center h-100 border-%s">
    <div class="card-body d-flex flex-column justify-content-center">
        <h2 class="card-title text-%s mb-3" style="font-size: 2.5rem; font-weight: bold;">%s</h2>
        <p class="card-text mb-3" style="font-size: 1.1rem;">%s %s</p>
        <small class="text-muted">%s</small>
    </div>
</div>`, color, color, template.HTMLEscapeString(value), icon, template.HTMLEscapeString(label), template.HTMLEscapeString(subtext))
}

// InfoCard generates an information card with title and content
func InfoCard(title, content, icon string) string {
	return fmt.Sprintf(`
<div class="card h-100">
    <div class="card-body text-center">
        <div class="mb-3" style="font-size: 3rem;">%s</div>
        <h6 class="card-title">%s</h6>
        <p class="card-text text-muted small text-justify">%s</p>
    </div>
</div>`, icon, template.HTMLEscapeString(title), template.HTMLEscapeString(content))
}

// LinkCard generates a clickable card with link
func LinkCard(title, content, icon, linkURL string) string {
	return fmt.Sprintf(`
<a href="%s" class="card text-decoration-none text-dark h-100">
    <div class="card-body text-center">
        <div class="mb-3" style="font-size: 3rem;">%s</div>
        <h6 class="card-title">%s</h6>
        <p class="card-text text-muted small text-justify">%s</p>
    </div>
</a>`, linkURL, icon, template.HTMLEscapeString(title), template.HTMLEscapeString(content))
}

// ConfigCard generates a card showing configuration values
func ConfigCard(title string, items []ConfigItem) string {
	html := fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-gear-fill me-2"></i>%s</h5>
    </div>
    <div class="card-body">
        <table class="table table-sm config-table mb-0">
            <tbody>`, template.HTMLEscapeString(title))

	for _, item := range items {
		html += fmt.Sprintf(`
                <tr>
                    <td class="fw-bold" style="width: 40%%;">%s</td>
                    <td><span class="config-value">%s</span></td>
                </tr>`, template.HTMLEscapeString(item.Label), template.HTMLEscapeString(item.Value))
	}

	html += `
            </tbody>
        </table>
    </div>
</div>`

	return html
}

// ConfigItem represents a configuration item for display
type ConfigItem struct {
	Label string
	Value string
}
