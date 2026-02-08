package components

import (
	"fmt"
	"html/template"
)

// TableConfig holds configuration for table rendering
type TableConfig struct {
	Striped     bool
	Hover       bool
	Small       bool
	Responsive  bool
	AlignMiddle bool
}

// DefaultTableConfig returns the default table configuration
func DefaultTableConfig() TableConfig {
	return TableConfig{
		Striped:     true,
		Hover:       true,
		Small:       false,
		Responsive:  true,
		AlignMiddle: true,
	}
}

// TableStart generates the opening tags for a table
func TableStart(headers []string, config TableConfig) string {
	classes := "table"
	if config.Striped {
		classes += " table-striped"
	}
	if config.Hover {
		classes += " table-hover"
	}
	if config.Small {
		classes += " table-sm"
	}
	if config.AlignMiddle {
		classes += " align-middle"
	}

	html := ""
	if config.Responsive {
		html += `<div class="table-responsive">`
	}

	html += fmt.Sprintf(`<table class="%s">
    <thead>
        <tr>`, classes)

	for _, header := range headers {
		html += fmt.Sprintf(`<th class="text-nowrap">%s</th>`, template.HTMLEscapeString(header))
	}

	html += `
        </tr>
    </thead>
    <tbody>`

	return html
}

// TableStartWithConfig generates table with custom column configurations
func TableStartWithConfig(columns []ColumnConfig, config TableConfig) string {
	classes := "table"
	if config.Striped {
		classes += " table-striped"
	}
	if config.Hover {
		classes += " table-hover"
	}
	if config.Small {
		classes += " table-sm"
	}
	if config.AlignMiddle {
		classes += " align-middle"
	}

	html := ""
	if config.Responsive {
		html += `<div class="table-responsive">`
	}

	html += fmt.Sprintf(`<table class="%s">
    <thead>
        <tr>`, classes)

	for _, col := range columns {
		thClass := ""
		if col.Center {
			thClass = ` class="text-center text-nowrap"`
		} else if col.NoWrap {
			thClass = ` class="text-nowrap"`
		}
		html += fmt.Sprintf(`<th%s>%s</th>`, thClass, template.HTMLEscapeString(col.Header))
	}

	html += `
        </tr>
    </thead>
    <tbody>`

	return html
}

// TableEnd generates the closing tags for a table
func TableEnd(responsive bool) string {
	html := `    </tbody>
</table>`
	if responsive {
		html += `</div>`
	}
	return html
}

// TableRow generates a table row
func TableRow(cells []string) string {
	html := "<tr>"
	for _, cell := range cells {
		html += fmt.Sprintf("<td>%s</td>", cell)
	}
	html += "</tr>"
	return html
}

// TableRowWithClass generates a table row with a custom class
func TableRowWithClass(cells []string, rowClass string) string {
	html := fmt.Sprintf(`<tr class="%s">`, rowClass)
	for _, cell := range cells {
		html += fmt.Sprintf("<td>%s</td>", cell)
	}
	html += "</tr>"
	return html
}

// ColumnConfig holds configuration for a table column
type ColumnConfig struct {
	Header string
	Center bool
	NoWrap bool
	Width  string
}

// EmptyTableMessage generates a message for empty tables
func EmptyTableMessage(message string) string {
	return fmt.Sprintf(`<div class="alert alert-info text-center">
    <i class="bi bi-info-circle me-2"></i>%s
</div>`, template.HTMLEscapeString(message))
}

// ListGroupStart generates opening tags for a list group
func ListGroupStart() string {
	return `<div class="list-group">`
}

// ListGroupEnd generates closing tags for a list group
func ListGroupEnd() string {
	return `</div>`
}

// ListGroupItem generates a list group item
func ListGroupItem(content string) string {
	return fmt.Sprintf(`<div class="list-group-item">%s</div>`, content)
}

// ListGroupItemLink generates a clickable list group item
func ListGroupItemLink(content, url string) string {
	return fmt.Sprintf(`<a href="%s" class="list-group-item list-group-item-action">%s</a>`, url, content)
}
