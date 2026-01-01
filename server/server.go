package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"agentize/config"
	"agentize/documents"
	"agentize/engine"
	"agentize/fsrepo"
	"agentize/model"
	"agentize/store"
)

// Server represents the HTTP server
type Server struct {
	config *config.Config
	engine *engine.Engine
	ag     AgentizeInterface
}

// AgentizeInterface defines the interface for Agentize instance
type AgentizeInterface interface {
	GetRepository() *fsrepo.NodeRepository
	GetSessionStore() store.SessionStore
	GetToolStrategy() model.MergeStrategy
	GenerateGraphVisualization(filename string, title string) error
	GetAllNodes() map[string]*model.Node
}

// NewServer creates a new HTTP server
func NewServer(cfg *config.Config, ag AgentizeInterface) *Server {
	// Create engine
	eng := engine.NewEngine(
		ag.GetRepository(),
		ag.GetSessionStore(),
		ag.GetToolStrategy(),
	)

	return &Server{
		config: cfg,
		engine: eng,
		ag:     ag,
	}
}

// Start starts the HTTP server
func (s *Server) Start() error {
	if !s.config.HTTP.Enabled {
		log.Println("HTTP server is disabled")
		return nil
	}

	// Setup routes
	http.HandleFunc("/chat", s.handleChat)
	http.HandleFunc("/graph", s.handleGraph)
	http.HandleFunc("/docs", s.handleDocs)
	http.HandleFunc("/health", s.handleHealth)

	address := s.config.GetAddress()
	log.Printf("Starting HTTP server on %s", address)
	log.Printf("Available endpoints:")
	log.Printf("  POST /chat - Chat endpoint (requires query and userID)")
	log.Printf("  GET  /graph - Graph visualization")
	log.Printf("  GET  /docs - Knowledge tree documentation")
	log.Printf("  GET  /health - Health check")

	return http.ListenAndServe(address, nil)
}

// ChatRequest represents a chat request
type ChatRequest struct {
	Query  string `json:"query"`
	UserID string `json:"userID"`
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Action      string                 `json:"action"`
	Message     string                 `json:"message"`
	ToolCall    *engine.ToolCall       `json:"tool_call,omitempty"`
	CurrentNode string                 `json:"current_node"`
	OpenedFiles []string               `json:"opened_files,omitempty"`
	Debug       map[string]interface{} `json:"debug,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// handleChat handles chat requests
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, ChatResponse{
			Error: fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Validate required fields
	if req.Query == "" {
		respondJSON(w, http.StatusBadRequest, ChatResponse{
			Error: "query is required",
		})
		return
	}
	if req.UserID == "" {
		respondJSON(w, http.StatusBadRequest, ChatResponse{
			Error: "userID is required",
		})
		return
	}

	// Get or create session
	session, err := s.getOrCreateSession(req.UserID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, ChatResponse{
			Error: fmt.Sprintf("Failed to get/create session: %v", err),
		})
		return
	}

	// Process the query
	output, err := s.engine.Step(session.SessionID, req.Query)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, ChatResponse{
			Error: fmt.Sprintf("Failed to process query: %v", err),
		})
		return
	}

	// Convert to response
	response := ChatResponse{
		Action:      output.Action,
		Message:     output.Message,
		ToolCall:    output.ToolCall,
		CurrentNode: output.CurrentNode,
		OpenedFiles: output.OpenedFiles,
		Debug:       output.Debug,
	}

	respondJSON(w, http.StatusOK, response)
}

// handleGraph handles graph visualization requests
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.config.Features.GraphVisualizationEnabled {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Graph visualization is disabled",
		})
		return
	}

	// Generate graph to a temporary file
	tmpFile := filepath.Join(os.TempDir(), "agentize_graph.html")
	if err := s.ag.GenerateGraphVisualization(tmpFile, "Knowledge Tree Graph"); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("Failed to generate graph: %v", err),
		})
		return
	}

	// Read and serve the file
	content, err := os.ReadFile(tmpFile)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("Failed to read graph file: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(content)
}

// handleDocs handles documentation requests
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get all nodes from Agentize
	nodes := s.ag.GetAllNodes()
	repo := s.ag.GetRepository()

	// Create document structure
	doc := documents.NewAgentizeDocument(nodes, func(path string) ([]string, error) {
		return repo.GetChildren(path)
	})

	// Generate HTML
	html, err := doc.GenerateHTML()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("Failed to generate documentation: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(html)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// getOrCreateSession gets an existing session or creates a new one
func (s *Server) getOrCreateSession(userID string) (*model.Session, error) {
	// Try to get existing session from store
	sessionStore := s.engine.GetSessionStore()

	// List sessions for user to find active one
	sessions, err := sessionStore.List(userID)
	if err == nil && len(sessions) > 0 {
		// Return the most recent session
		return sessions[len(sessions)-1], nil
	}

	// Create new session
	return s.engine.StartSession(userID)
}

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
