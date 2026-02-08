package components

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
)

// Expandable generates expandable/collapsible content
func Expandable(shortContent, fullContent string, maxLength int) template.HTML {
	if len(fullContent) <= maxLength {
		return template.HTML(template.HTMLEscapeString(fullContent))
	}

	shortEscaped := template.HTMLEscapeString(shortContent)
	fullEscaped := template.HTMLEscapeString(fullContent)

	// Generate unique ID
	id := generateUniqueID()

	return template.HTML(fmt.Sprintf(`<span class="expandable-content" id="%s">
    <span class="short-content">%s</span>
    <span class="expand-icon"></span>
    <div class="full-content">%s</div>
</span>`, id, shortEscaped, fullEscaped))
}

// ExpandableWithPreview generates expandable content with automatic preview
func ExpandableWithPreview(content string, maxLength int) template.HTML {
	if len(content) <= maxLength {
		return template.HTML(template.HTMLEscapeString(content))
	}

	shortContent := content[:maxLength] + "..."
	return Expandable(shortContent, content, maxLength)
}

// ExpandablePre generates expandable content in a pre tag
func ExpandablePre(content string, maxHeight int) string {
	escaped := template.HTMLEscapeString(content)
	return fmt.Sprintf(`<pre class="p-3 bg-light rounded" style="max-height: %dpx; overflow-y: auto; white-space: pre-wrap; word-wrap: break-word;">%s</pre>`,
		maxHeight, escaped)
}

// CodeBlock generates a code block with background
func CodeBlock(content string) string {
	return fmt.Sprintf(`<code class="d-block p-2 bg-light rounded text-break">%s</code>`,
		template.HTMLEscapeString(content))
}

// InlineCode generates inline code
func InlineCode(content string) string {
	return fmt.Sprintf(`<code>%s</code>`, template.HTMLEscapeString(content))
}

// PreBlock generates a pre block for formatted content
func PreBlock(content string) string {
	return fmt.Sprintf(`<pre class="mb-0 mt-1" style="max-height: 150px; overflow-y: auto; font-size: 0.85em;">%s</pre>`,
		template.HTMLEscapeString(content))
}

// generateUniqueID generates a unique ID for HTML elements
func generateUniqueID() string {
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return "expand-" + hex.EncodeToString(bytes)
}

// TruncatedText generates text with ellipsis if too long
func TruncatedText(text string, maxLength int) string {
	if len(text) <= maxLength {
		return template.HTMLEscapeString(text)
	}
	return template.HTMLEscapeString(text[:maxLength]) + "..."
}

// TruncatedLink generates a truncated text inside a link
func TruncatedLink(text, url string, maxLength int) string {
	displayText := text
	if len(text) > maxLength {
		displayText = text[:maxLength] + "..."
	}
	return fmt.Sprintf(`<a href="%s" class="text-decoration-none">%s</a>`,
		url, template.HTMLEscapeString(displayText))
}
