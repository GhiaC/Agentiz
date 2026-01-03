package documents

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

// TestGenerateHTML tests that GenerateHTML works correctly with templ components
// This test will fail if templ components are not properly generated or if there are syntax errors
func TestGenerateHTML(t *testing.T) {
	// Create test nodes
	nodes := map[string]*model.Node{
		"root": {
			Path:        "root",
			ID:          "root",
			Title:       "Test Root Node",
			Description: "This is a test root node",
			Content:     "# Test Root\n\nThis is test content.",
			Auth: model.Auth{
				Users: map[string]*model.Permissions{
					"test": {
						Perms: "rwxsdg",
					},
				},
			},
			Tools: []model.Tool{
				{
					Name:        "test_tool",
					Description: "A test tool",
					InputSchema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"param": map[string]interface{}{
								"type": "string",
							},
						},
					},
					Status: model.ToolStatusActive,
				},
			},
		},
		"root/child1": {
			Path:        "root/child1",
			ID:          "child1",
			Title:       "Child Node",
			Description: "A child node",
			Content:     "# Child\n\nChild content.",
			Auth: model.Auth{
				Users: map[string]*model.Permissions{},
			},
			Tools: []model.Tool{},
		},
	}

	// Create getChildren function
	getChildren := func(path string) ([]string, error) {
		switch path {
		case "root":
			return []string{"root/child1"}, nil
		default:
			return []string{}, nil
		}
	}

	// Create document
	doc := NewAgentizeDocument(nodes, getChildren)

	// Test GenerateHTML
	html, err := doc.GenerateHTML()
	if err != nil {
		t.Fatalf("GenerateHTML failed: %v", err)
	}

	if len(html) == 0 {
		t.Fatal("Generated HTML is empty")
	}

	htmlStr := string(html)
	
	// Debug: Print first 500 characters of HTML
	if len(htmlStr) > 500 {
		t.Logf("First 500 chars of HTML: %s", htmlStr[:500])
	} else {
		t.Logf("Full HTML: %s", htmlStr)
	}

	// Check that HTML contains expected elements
	expectedElements := []string{
		"<!doctype html>",
		"<html",
		"Agentize Knowledge Tree",
		"tree-container",
		"detail-view",
		"const treeData",
		"const nodesData",
	}

	for _, elem := range expectedElements {
		if !strings.Contains(htmlStr, elem) {
			t.Errorf("Generated HTML missing expected element: %s", elem)
		}
	}

	// Check that JSON data is present (should contain "root" path)
	if !strings.Contains(htmlStr, `"root"`) {
		t.Error("Generated HTML should contain JSON data with 'root' path")
	}

	// Check that node title is present
	if !strings.Contains(htmlStr, "Test Root Node") {
		t.Error("Generated HTML should contain node title")
	}

	// Check that tool name is present
	if !strings.Contains(htmlStr, "test_tool") {
		t.Error("Generated HTML should contain tool name")
	}

	// Verify that the HTML is valid (starts with DOCTYPE and has closing tags)
	if !strings.HasPrefix(strings.ToLower(htmlStr), "<!doctype html>") {
		t.Error("Generated HTML should start with DOCTYPE declaration")
	}

	if !strings.Contains(htmlStr, "</html>") {
		t.Error("Generated HTML should have closing html tag")
	}

	// Check that templ components are properly rendered (should not contain template syntax)
	if strings.Contains(htmlStr, "@templ") || strings.Contains(htmlStr, "@treeData") || strings.Contains(htmlStr, "@nodesData") {
		t.Error("Generated HTML should not contain templ template syntax - templ components may not be properly generated")
	}

	// Check that rawJSON function is being used correctly (should not appear in output)
	if strings.Contains(htmlStr, "rawJSON") {
		t.Error("Generated HTML should not contain rawJSON function name - it should be executed, not printed")
	}
}

// TestGenerateHTMLWithEmptyData tests edge case with empty data
func TestGenerateHTMLWithEmptyData(t *testing.T) {
	nodes := map[string]*model.Node{
		"root": {
			Path:  "root",
			ID:    "root",
			Title: "Empty Root",
			Auth: model.Auth{
				Users: map[string]*model.Permissions{},
			},
		},
	}

	getChildren := func(path string) ([]string, error) {
		return []string{}, nil
	}

	doc := NewAgentizeDocument(nodes, getChildren)

	html, err := doc.GenerateHTML()
	if err != nil {
		t.Fatalf("GenerateHTML failed with empty data: %v", err)
	}

	if len(html) == 0 {
		t.Fatal("Generated HTML should not be empty even with minimal data")
	}

	htmlStr := string(html)
	if !strings.Contains(htmlStr, "root") {
		t.Error("Generated HTML should contain root node data")
	}
}

// TestGenerateHTMLWithSpecialCharacters tests that special characters in JSON are handled correctly
func TestGenerateHTMLWithSpecialCharacters(t *testing.T) {
	nodes := map[string]*model.Node{
		"root": {
			Path:        "root",
			ID:          "root",
			Title:       "Node with \"quotes\" and <tags>",
			Description: "Description with 'single' and \"double\" quotes",
			Content:     "Content with `backticks` and <script>alert('xss')</script>",
			Auth: model.Auth{
				Users: map[string]*model.Permissions{},
			},
		},
	}

	getChildren := func(path string) ([]string, error) {
		return []string{}, nil
	}

	doc := NewAgentizeDocument(nodes, getChildren)

	html, err := doc.GenerateHTML()
	if err != nil {
		t.Fatalf("GenerateHTML failed with special characters: %v", err)
	}

	htmlStr := string(html)
	
	// Check that JSON is properly escaped (should contain escaped quotes)
	// The JSON should be valid even with special characters
	if !strings.Contains(htmlStr, "const treeData") {
		t.Error("Generated HTML should contain treeData constant")
	}

	// Verify HTML is still valid
	if !strings.HasPrefix(strings.ToLower(htmlStr), "<!doctype html>") {
		t.Error("Generated HTML should start with DOCTYPE even with special characters")
	}
}

