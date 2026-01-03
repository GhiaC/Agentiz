package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ghiac/agentize/config"
	"github.com/ghiac/agentize/documents"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
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
	// GetRegisteredTools returns the list of registered tool names (optional)
	// If not implemented, returns nil
	GetRegisteredTools() []string
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
	http.HandleFunc("/graph", s.handleGraph)
	http.HandleFunc("/docs", s.handleDocs)
	http.HandleFunc("/health", s.handleHealth)

	address := s.config.GetAddress()
	log.Printf("Starting HTTP server on %s", address)
	log.Printf("Available endpoints:")
	log.Printf("  GET  /graph - Graph visualization")
	log.Printf("  GET  /docs - Knowledge tree documentation")
	log.Printf("  GET  /health - Health check")

	return http.ListenAndServe(address, nil)
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

	// Get registered tools - first try from AgentizeInterface if it implements GetRegisteredTools
	var registeredTools []string
	if registeredToolsGetter, ok := s.ag.(interface{ GetRegisteredTools() []string }); ok {
		registeredTools = registeredToolsGetter.GetRegisteredTools()
	} else if s.engine != nil {
		// Fallback to engine's function registry
		functionRegistry := s.engine.GetFunctionRegistry()
		if functionRegistry != nil {
			registeredTools = functionRegistry.GetAllRegistered()
		}
	}

	// Generate HTML with registered tools information
	html, err := doc.GenerateHTMLWithRegisteredTools(registeredTools)
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

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
