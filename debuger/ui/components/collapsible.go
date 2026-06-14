package components

import (
	"fmt"
	"html/template"
)

// Collapsible cards/sections are built on the native HTML <details> element:
// collapsed by default, toggled by the browser with zero JavaScript (so they
// keep working under the page's 30s auto-refresh and need no per-element IDs).
// They are styled to match the regular .card surface — see GetStyles().

// CollapsibleCardStart opens a card whose body is hidden until the user expands
// it. open=false renders it collapsed (the default for the secondary cards on
// the user detail page). metaHTML is trusted markup shown on the right of the
// header (e.g. a count badge); pass "" for none. Pair with CollapsibleCardEnd.
func CollapsibleCardStart(title, icon, metaHTML string, open bool) string {
	openAttr := ""
	if open {
		openAttr = " open"
	}
	return fmt.Sprintf(`<details class="card collapsible-card mb-4"%s>
    <summary class="card-header collapsible-summary">
        <span class="collapsible-title"><i class="bi bi-%s me-2"></i>%s</span>
        <span class="collapsible-meta">%s</span>
    </summary>
    <div class="card-body">`, openAttr, icon, template.HTMLEscapeString(title), metaHTML)
}

// CollapsibleCardStartWithCount is CollapsibleCardStart with a count badge as the
// header meta, mirroring CardStartWithCount.
func CollapsibleCardStartWithCount(title, icon string, count int, open bool) string {
	return CollapsibleCardStart(title, icon, CountBadge(count, "secondary"), open)
}

// CollapsibleCardEnd closes a CollapsibleCardStart.
func CollapsibleCardEnd() string {
	return `    </div>
</details>`
}

// CollapsibleSection is a lighter nested collapsible block, used for each section
// inside the Core System Prompt card. badgesHTML and bodyHTML are trusted markup
// (the caller escapes any user content). open=false renders it collapsed.
func CollapsibleSection(title, badgesHTML, bodyHTML string, open bool) string {
	openAttr := ""
	if open {
		openAttr = " open"
	}
	return fmt.Sprintf(`<details class="collapsible-section"%s>
    <summary class="collapsible-section-summary">
        <span class="collapsible-section-title">%s</span>
        <span class="collapsible-section-badges">%s</span>
    </summary>
    <div class="collapsible-section-body">%s</div>
</details>`, openAttr, template.HTMLEscapeString(title), badgesHTML, bodyHTML)
}
