package model

import (
	"errors"
	"testing"
)

func TestToolStatus(t *testing.T) {
	tool := Tool{
		Name:        "test_tool",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusActive,
	}

	// Test active tool
	if !tool.IsUsable() {
		t.Error("Active tool should be usable")
	}
	if err := tool.CanUse(); err != nil {
		t.Errorf("Active tool should not return error, got: %v", err)
	}

	// Test temporary disabled
	tool.SetTemporaryDisabled(DisableReasonMaintenance, "Under maintenance")
	if tool.IsUsable() {
		t.Error("Temporary disabled tool should not be usable")
	}
	err := tool.CanUse()
	if err == nil {
		t.Fatal("Temporary disabled tool should return error")
	}
	var disabledErr *ToolDisabledError
	if !errors.As(err, &disabledErr) {
		t.Errorf("Expected ToolDisabledError, got %T", err)
	}
	if disabledErr.DisableReason != DisableReasonMaintenance {
		t.Errorf("Expected reason 'maintenance', got '%s'", disabledErr.DisableReason)
	}
	if disabledErr.ErrorMessage != "Under maintenance" {
		t.Errorf("Expected error message 'Under maintenance', got '%s'", disabledErr.ErrorMessage)
	}

	// Test hidden tool
	tool.SetHidden()
	if tool.IsUsable() {
		t.Error("Hidden tool should not be usable")
	}
	err = tool.CanUse()
	if err == nil {
		t.Fatal("Hidden tool should return error")
	}
	var notFoundErr *ToolNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("Expected ToolNotFoundError for hidden tool, got %T", err)
	}

	// Test set active again
	tool.SetActive()
	if !tool.IsUsable() {
		t.Error("Tool should be usable after SetActive")
	}
}

func TestToolRegistryWithDisabledTools(t *testing.T) {
	registry := NewToolRegistry(MergeStrategyOverride)

	activeTool := Tool{
		Name:        "active_tool",
		Description: "Active tool",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusActive,
	}

	disabledTool := Tool{
		Name:          "disabled_tool",
		Description:   "Disabled tool",
		InputSchema:   map[string]interface{}{},
		Status:        ToolStatusTemporaryDisabled,
		DisableReason: DisableReasonError,
		ErrorMessage:  "Service unavailable",
	}

	hiddenTool := Tool{
		Name:        "hidden_tool",
		Description: "Hidden tool",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusHidden,
	}

	// Add all tools
	if err := registry.AddTool(activeTool); err != nil {
		t.Fatalf("Failed to add active tool: %v", err)
	}
	if err := registry.AddTool(disabledTool); err != nil {
		t.Fatalf("Failed to add disabled tool: %v", err)
	}
	if err := registry.AddTool(hiddenTool); err != nil {
		t.Fatalf("Failed to add hidden tool: %v", err)
	}

	// GetTools should exclude hidden tools
	tools := registry.GetTools()
	if len(tools) != 2 {
		t.Errorf("Expected 2 tools (excluding hidden), got %d", len(tools))
	}

	// Check that hidden tool is not in the list
	for _, tool := range tools {
		if tool.Name == "hidden_tool" {
			t.Error("Hidden tool should not appear in GetTools()")
		}
	}

	// GetToolsIncludingHidden should include all tools
	allTools := registry.GetToolsIncludingHidden()
	if len(allTools) != 3 {
		t.Errorf("Expected 3 tools (including hidden), got %d", len(allTools))
	}

	// GetTool should return all tools (including hidden)
	tool, ok := registry.GetTool("hidden_tool")
	if !ok {
		t.Error("GetTool should return hidden tool")
	}
	if tool.Name != "hidden_tool" {
		t.Errorf("Expected 'hidden_tool', got '%s'", tool.Name)
	}

	// GetActiveTool should only return active tools
	active, ok := registry.GetActiveTool("active_tool")
	if !ok {
		t.Error("GetActiveTool should return active tool")
	}
	if active.Name != "active_tool" {
		t.Errorf("Expected 'active_tool', got '%s'", active.Name)
	}

	// GetActiveTool should not return disabled tool
	_, ok = registry.GetActiveTool("disabled_tool")
	if ok {
		t.Error("GetActiveTool should not return disabled tool")
	}

	// GetActiveTool should not return hidden tool
	_, ok = registry.GetActiveTool("hidden_tool")
	if ok {
		t.Error("GetActiveTool should not return hidden tool")
	}

	// IsToolUsable tests
	if !registry.IsToolUsable("active_tool") {
		t.Error("Active tool should be usable")
	}
	if registry.IsToolUsable("disabled_tool") {
		t.Error("Disabled tool should not be usable")
	}
	if registry.IsToolUsable("hidden_tool") {
		t.Error("Hidden tool should not be usable")
	}
	if registry.IsToolUsable("nonexistent") {
		t.Error("Nonexistent tool should not be usable")
	}

	// CanUseTool tests
	if err := registry.CanUseTool("active_tool"); err != nil {
		t.Errorf("Active tool should not return error, got: %v", err)
	}

	err := registry.CanUseTool("disabled_tool")
	if err == nil {
		t.Fatal("Disabled tool should return error")
	}
	var disabledErr *ToolDisabledError
	if !errors.As(err, &disabledErr) {
		t.Errorf("Expected ToolDisabledError, got %T", err)
	}

	err = registry.CanUseTool("hidden_tool")
	if err == nil {
		t.Fatal("Hidden tool should return error")
	}
	var notFoundErr *ToolNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("Expected ToolNotFoundError for hidden tool, got %T", err)
	}

	err = registry.CanUseTool("nonexistent")
	if err == nil {
		t.Fatal("Nonexistent tool should return error")
	}
	if !errors.As(err, &notFoundErr) {
		t.Errorf("Expected ToolNotFoundError for nonexistent tool, got %T", err)
	}
}

func TestDisableReasons(t *testing.T) {
	tests := []struct {
		reason       DisableReason
		errorMessage string
		expectedMsg  string
	}{
		{DisableReasonMaintenance, "Scheduled maintenance", "maintenance"},
		{DisableReasonError, "API error", "error"},
		{DisableReasonDeprecated, "Use new_tool instead", "deprecated"},
		{DisableReasonRateLimit, "Rate limit exceeded", "rate_limit"},
		{DisableReasonUnavailable, "Service down", "unavailable"},
		{DisableReasonCustom, "Custom reason", "custom"},
	}

	for _, tt := range tests {
		tool := Tool{
			Name:          "test_tool",
			Status:        ToolStatusTemporaryDisabled,
			DisableReason: tt.reason,
			ErrorMessage:  tt.errorMessage,
		}

		err := tool.CanUse()
		if err == nil {
			t.Errorf("Expected error for reason %s", tt.reason)
			continue
		}

		var disabledErr *ToolDisabledError
		if !errors.As(err, &disabledErr) {
			t.Errorf("Expected ToolDisabledError for reason %s, got %T", tt.reason, err)
			continue
		}

		if disabledErr.DisableReason != tt.reason {
			t.Errorf("Expected reason %s, got %s", tt.reason, disabledErr.DisableReason)
		}

		if disabledErr.ErrorMessage != tt.errorMessage {
			t.Errorf("Expected error message '%s', got '%s'", tt.errorMessage, disabledErr.ErrorMessage)
		}

		// Check error string contains reason
		errStr := err.Error()
		if !contains(errStr, string(tt.reason)) {
			t.Errorf("Error message should contain reason '%s', got: %s", tt.reason, errStr)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			containsMiddle(s, substr))))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
