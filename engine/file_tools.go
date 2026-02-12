package engine

import (
	"fmt"

	"github.com/ghiac/agentize/model"
)

// FileOpener interface defines the contract for file operations
// This allows the Engine to be used for file operations without direct coupling
type FileOpener interface {
	OpenFile(sessionID string, path string) (string, error)
	CloseFile(sessionID string, path string) error
}

// Ensure Engine implements FileOpener
var _ FileOpener = (*Engine)(nil)

// RegisterFileTools registers open_file and close_file tools with the given registry
// The tools use the Engine's OpenFile/CloseFile methods to manage session files
func (e *Engine) RegisterFileTools(registry *model.FunctionRegistry) {
	if registry == nil {
		return
	}

	registry.Register("open_file", "Open File", e.createOpenFileFunction())
	registry.Register("close_file", "Close File", e.createCloseFileFunction())
}

// createOpenFileFunction creates the open_file tool function
func (e *Engine) createOpenFileFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		// Get session ID from injected context
		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		content, err := e.OpenFile(sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error opening file: %v", err), nil
		}

		return fmt.Sprintf("File opened successfully. Content length: %d characters. The file is now available in your context.", len(content)), nil
	}
}

// createCloseFileFunction creates the close_file tool function
func (e *Engine) createCloseFileFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		// Get session ID from injected context
		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		err = e.CloseFile(sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error closing file: %v", err), nil
		}

		return fmt.Sprintf("File closed successfully: %s", path), nil
	}
}

// CreateFileToolsWithOpener creates file tool functions using a custom FileOpener
// This is useful for integrating with different file management systems
func CreateFileToolsWithOpener(opener FileOpener) (openFile, closeFile model.ToolFunction) {
	openFile = func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		content, err := opener.OpenFile(sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error opening file: %v", err), nil
		}

		return fmt.Sprintf("File opened successfully. Content length: %d characters. The file is now available in your context.", len(content)), nil
	}

	closeFile = func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		err = opener.CloseFile(sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error closing file: %v", err), nil
		}

		return fmt.Sprintf("File closed successfully: %s", path), nil
	}

	return openFile, closeFile
}

// Helper function to extract string argument from tool args
func getStringArg(args map[string]interface{}, key string) (string, error) {
	val, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument: %s", key)
	}

	strVal, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}

	return strVal, nil
}

// GetFileToolDefinitions returns the JSON schema definitions for open_file and close_file tools
// These can be added to a node's tools.json
func GetFileToolDefinitions() []model.Tool {
	return []model.Tool{
		{
			Name:        "open_file",
			Description: "Opens a knowledge tree file/node by its path and adds it to the current session context. The file content will be available in the LLM context for subsequent messages.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "The path to the file/node in the knowledge tree (e.g., 'root/kubernetes/pods')",
					},
				},
				"required": []string{"path"},
			},
			Status: "active",
		},
		{
			Name:        "close_file",
			Description: "Closes a previously opened file/node and removes it from the session context. This helps manage context size by removing files that are no longer needed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "The path to the file/node to close",
					},
				},
				"required": []string{"path"},
			},
			Status: "active",
		},
	}
}
