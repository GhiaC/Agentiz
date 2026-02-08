package components

import (
	"fmt"
	"html/template"
)

// Button generates a Bootstrap button
func Button(text, url, variant string) string {
	return fmt.Sprintf(`<a href="%s" class="btn btn-%s">%s</a>`,
		url, variant, template.HTMLEscapeString(text))
}

// ButtonSmall generates a small Bootstrap button
func ButtonSmall(text, url, variant string) string {
	return fmt.Sprintf(`<a href="%s" class="btn btn-sm btn-%s">%s</a>`,
		url, variant, template.HTMLEscapeString(text))
}

// ButtonOutline generates an outline button
func ButtonOutline(text, url, variant string) string {
	return fmt.Sprintf(`<a href="%s" class="btn btn-outline-%s">%s</a>`,
		url, variant, template.HTMLEscapeString(text))
}

// ButtonOutlineSmall generates a small outline button
func ButtonOutlineSmall(text, url, variant string) string {
	return fmt.Sprintf(`<a href="%s" class="btn btn-sm btn-outline-%s">%s</a>`,
		url, variant, template.HTMLEscapeString(text))
}

// ViewButton generates a "View" button
func ViewButton(url string) string {
	return ButtonOutlineSmall("View", url, "primary")
}

// OpenButton generates an "Open" button
func OpenButton(url string) string {
	return ButtonOutlineSmall("Open", url, "primary")
}

// ViewDetailsButton generates a "View Details" button
func ViewDetailsButton(url string) string {
	return ButtonOutlineSmall("View Details", url, "primary")
}

// Link generates a simple link
func Link(text, url string) string {
	return fmt.Sprintf(`<a href="%s" class="text-decoration-none">%s</a>`,
		url, template.HTMLEscapeString(text))
}

// LinkWithClass generates a link with custom class
func LinkWithClass(text, url, class string) string {
	return fmt.Sprintf(`<a href="%s" class="%s">%s</a>`,
		url, class, template.HTMLEscapeString(text))
}
