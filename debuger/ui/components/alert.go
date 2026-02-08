package components

import (
	"fmt"
	"html/template"
)

// Alert generates a Bootstrap alert
func Alert(message, variant string) string {
	return fmt.Sprintf(`<div class="alert alert-%s">%s</div>`, variant, template.HTMLEscapeString(message))
}

// AlertWithIcon generates an alert with an icon
func AlertWithIcon(message, icon, variant string) string {
	return fmt.Sprintf(`<div class="alert alert-%s">
    <i class="bi bi-%s me-2"></i>%s
</div>`, variant, icon, template.HTMLEscapeString(message))
}

// InfoAlert generates an info alert
func InfoAlert(message string) string {
	return AlertWithIcon(message, "info-circle", "info")
}

// WarningAlert generates a warning alert
func WarningAlert(message string) string {
	return AlertWithIcon(message, "exclamation-triangle", "warning")
}

// SuccessAlert generates a success alert
func SuccessAlert(message string) string {
	return AlertWithIcon(message, "check-circle", "success")
}

// DangerAlert generates a danger alert
func DangerAlert(message string) string {
	return AlertWithIcon(message, "x-circle", "danger")
}

// AlertCentered generates a centered alert
func AlertCentered(message, variant string) string {
	return fmt.Sprintf(`<div class="alert alert-%s text-center">
    <i class="bi bi-info-circle me-2"></i>%s
</div>`, variant, template.HTMLEscapeString(message))
}

// NoteAlert generates a note-style alert
func NoteAlert(title, message string) string {
	return fmt.Sprintf(`<div class="alert alert-warning mb-3">
    <i class="bi bi-exclamation-triangle me-2"></i><strong>%s:</strong> %s
</div>`, template.HTMLEscapeString(title), template.HTMLEscapeString(message))
}
