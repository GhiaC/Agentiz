package main

import (
	"flag"
	"log"
	"os"

	"github.com/ghiac/agentize"
	"github.com/ghiac/agentize/config"
	"github.com/ghiac/agentize/server"
)

func main() {
	// Parse command line flags
	knowledgePath := flag.String("knowledge", "", "Path to knowledge tree directory (default: ./knowledge or AGENTIZE_KNOWLEDGE_PATH)")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Override knowledge path if provided via flag
	if *knowledgePath != "" {
		cfg.KnowledgePath = *knowledgePath
	}

	log.Printf("=== Agentize Server ===")
	log.Printf("Knowledge Path: %s", cfg.KnowledgePath)
	log.Printf("HTTP Server Enabled: %v", cfg.HTTP.Enabled)
	log.Printf("Graph Visualization Enabled: %v", cfg.Features.GraphVisualizationEnabled)

	// Check if knowledge path exists
	if _, err := os.Stat(cfg.KnowledgePath); os.IsNotExist(err) {
		log.Fatalf("Knowledge path does not exist: %s", cfg.KnowledgePath)
	}

	// Create Agentize instance
	ag, err := agentize.New(cfg.KnowledgePath)
	if err != nil {
		log.Fatalf("Failed to create Agentize instance: %v", err)
	}

	log.Printf("Loaded %d nodes from knowledge tree", len(ag.GetAllNodes()))

	// Start HTTP server if enabled
	if cfg.HTTP.Enabled {
		srv := server.NewServer(cfg, ag)
		if err := srv.Start(); err != nil {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	} else {
		log.Println("HTTP server is disabled. Set AGENTIZE_HTTP_ENABLED=true and AGENTIZE_FEATURE_HTTP=true to enable.")
		log.Println("Running in library mode...")
		// Keep the process running
		select {}
	}
}
