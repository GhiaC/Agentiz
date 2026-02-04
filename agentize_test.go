package agentize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	// Create temporary knowledge tree
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	// Create Agentize instance
	ag, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create Agentize: %v", err)
	}

	// Check root node
	root := ag.GetRoot()
	if root == nil {
		t.Fatal("Root node should not be nil")
	}
	if root.Path != "root" {
		t.Errorf("Expected root path 'root', got '%s'", root.Path)
	}

	// Check all nodes are loaded
	allNodes := ag.GetAllNodes()
	if len(allNodes) < 1 {
		t.Fatal("Should have at least root node loaded")
	}

	// Check node paths
	paths := ag.GetNodePaths()
	if len(paths) < 1 {
		t.Fatal("Should have at least one path")
	}
	if paths[0] != "root" {
		t.Errorf("First path should be 'root', got '%s'", paths[0])
	}
}

func TestNewWithOptions(t *testing.T) {
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	opts := &Options{}

	ag, err := NewWithOptions(tmpDir, opts)
	if err != nil {
		t.Fatalf("Failed to create Agentize with options: %v", err)
	}

	if ag == nil {
		t.Error("Expected Agentize instance, got nil")
	}
}

func TestGetNode(t *testing.T) {
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	ag, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create Agentize: %v", err)
	}

	// Get root node
	node, err := ag.GetNode("root")
	if err != nil {
		t.Fatalf("Failed to get root node: %v", err)
	}
	if node.Path != "root" {
		t.Errorf("Expected path 'root', got '%s'", node.Path)
	}

	// Try to get non-existent node
	_, err = ag.GetNode("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent node")
	}
}

func TestReload(t *testing.T) {
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	ag, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create Agentize: %v", err)
	}

	initialCount := len(ag.GetAllNodes())

	// Reload
	if err := ag.Reload(); err != nil {
		t.Fatalf("Failed to reload: %v", err)
	}

	reloadedCount := len(ag.GetAllNodes())
	if reloadedCount != initialCount {
		t.Errorf("Node count should remain the same after reload, got %d vs %d", reloadedCount, initialCount)
	}
}

func createTestKnowledgeTree(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "agentize-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create root node
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	yamlContent := `id: "root"
title: "Test Root"
description: "Test description"
auth:
  users:
    - user_id: "test"
      can_edit: true
      can_read: true
      can_access_next: true
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte(yamlContent), 0644)
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte("# Root\n\nRoot content."), 0644)
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(`{"tools": []}`), 0644)

	// Create next node
	nextPath := filepath.Join(rootPath, "next")
	os.MkdirAll(nextPath, 0755)
	os.WriteFile(filepath.Join(nextPath, "node.yaml"), []byte(`id: "next"`), 0644)
	os.WriteFile(filepath.Join(nextPath, "node.md"), []byte("# Next\n\nNext content."), 0644)

	return tmpDir
}

// createInfraAgentLikeKnowledgeTree creates a knowledge tree structure similar to InfraAgent
// but without any private/sensitive information - suitable for testing
func createInfraAgentLikeKnowledgeTree(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "agentize-infra-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// ===== ROOT NODE =====
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	rootYAML := `id: "root"
title: "Infrastructure Assistant"
description: "AI Infrastructure Operations Assistant for Kubernetes and Monitoring"
auth:
  default:
    visible_docs: true
    visible_graph: true
`
	os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte(rootYAML), 0644)

	rootMD := `# Infrastructure Operations Assistant

## Role

* You are an **AI Infrastructure Operations Assistant** specialized in managing infrastructure.
* Your mission is to help DevOps engineers and SREs monitor, diagnose, and manage resources.
* Always prioritize **safety** and **best practices** when performing operations.

## Navigation

You can navigate to specialized areas:
- **kubernetes**: Kubernetes cluster operations and resources
- **monitoring**: Monitoring and observability tools
- **documents**: Documentation and reference materials
`
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte(rootMD), 0644)

	rootTools := `{
  "tools": [
    {
      "name": "clarify_question",
      "description": "Clarify question when requests are unclear",
      "input_schema": {
        "type": "object",
        "properties": {
          "statement": {
            "type": "string",
            "description": "Query statement to clarify"
          }
        },
        "required": ["statement"]
      }
    },
    {
      "name": "send_message",
      "description": "Send message to the user",
      "input_schema": {
        "type": "object",
        "properties": {
          "statement": {
            "type": "string",
            "description": "The text message you want to send"
          }
        },
        "required": ["statement"]
      }
    }
  ]
}`
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(rootTools), 0644)

	// ===== KUBERNETES NODE =====
	kubernetesPath := filepath.Join(rootPath, "kubernetes")
	os.MkdirAll(kubernetesPath, 0755)

	kubernetesYAML := `id: "kubernetes"
title: "Kubernetes Operations"
description: "Kubernetes cluster management and operations"
`
	os.WriteFile(filepath.Join(kubernetesPath, "node.yaml"), []byte(kubernetesYAML), 0644)

	kubernetesMD := `# Kubernetes Operations

Kubernetes cluster management and operations.

## Available Operations

- Pod management
- Deployment operations
- Service management
- Namespace operations
`
	os.WriteFile(filepath.Join(kubernetesPath, "node.md"), []byte(kubernetesMD), 0644)

	kubernetesTools := `{
  "tools": [
    {
      "name": "get_namespaces",
      "description": "Get all namespaces in the cluster",
      "input_schema": {
        "type": "object",
        "properties": {},
        "required": []
      }
    },
    {
      "name": "get_namespace_pods",
      "description": "Get all pods in a specific namespace",
      "input_schema": {
        "type": "object",
        "properties": {
          "namespace": {
            "type": "string",
            "description": "The namespace to get pods from"
          }
        },
        "required": ["namespace"]
      }
    },
    {
      "name": "get_deployments",
      "description": "Get all deployments in a specific namespace",
      "input_schema": {
        "type": "object",
        "properties": {
          "namespace": {
            "type": "string",
            "description": "The namespace to get deployments from"
          }
        },
        "required": ["namespace"]
      }
    }
  ]
}`
	os.WriteFile(filepath.Join(kubernetesPath, "tools.json"), []byte(kubernetesTools), 0644)

	// ===== KUBERNETES/DEPLOYMENTS NODE =====
	deploymentsPath := filepath.Join(kubernetesPath, "deployments")
	os.MkdirAll(deploymentsPath, 0755)

	deploymentsYAML := `id: "deployments"
title: "Kubernetes Deployments"
description: "Deployment management and operations"
`
	os.WriteFile(filepath.Join(deploymentsPath, "node.yaml"), []byte(deploymentsYAML), 0644)

	deploymentsMD := `# Kubernetes Deployments

Deployment management and operations.

## Operations

- List deployments
- Scale deployments
- Update deployment images
- Rollout restarts
`
	os.WriteFile(filepath.Join(deploymentsPath, "node.md"), []byte(deploymentsMD), 0644)

	// ===== KUBERNETES/PODS NODE =====
	podsPath := filepath.Join(kubernetesPath, "pods")
	os.MkdirAll(podsPath, 0755)

	podsYAML := `id: "pods"
title: "Kubernetes Pods"
description: "Pod management and operations"
`
	os.WriteFile(filepath.Join(podsPath, "node.yaml"), []byte(podsYAML), 0644)

	podsMD := `# Kubernetes Pods

Pod management and operations.

## Operations

- Get pod status
- View pod logs
- Restart pods
- Delete pods
`
	os.WriteFile(filepath.Join(podsPath, "node.md"), []byte(podsMD), 0644)

	// ===== MONITORING NODE =====
	monitoringPath := filepath.Join(rootPath, "monitoring")
	os.MkdirAll(monitoringPath, 0755)

	monitoringYAML := `id: "monitoring"
title: "Monitoring and Observability"
description: "Monitoring tools and metrics"
`
	os.WriteFile(filepath.Join(monitoringPath, "node.yaml"), []byte(monitoringYAML), 0644)

	monitoringMD := `# Monitoring and Observability

Monitoring tools and metrics collection.

## Available Tools

- Prometheus metrics
- Service level indicators (SLI)
- Health checks
`
	os.WriteFile(filepath.Join(monitoringPath, "node.md"), []byte(monitoringMD), 0644)

	monitoringTools := `{
  "tools": [
    {
      "name": "get_metrics",
      "description": "Get metrics from monitoring system",
      "input_schema": {
        "type": "object",
        "properties": {
          "query": {
            "type": "string",
            "description": "Prometheus query"
          }
        },
        "required": ["query"]
      }
    },
    {
      "name": "get_sli",
      "description": "Get service level indicators",
      "input_schema": {
        "type": "object",
        "properties": {
          "service": {
            "type": "string",
            "description": "Service name"
          }
        },
        "required": ["service"]
      }
    }
  ]
}`
	os.WriteFile(filepath.Join(monitoringPath, "tools.json"), []byte(monitoringTools), 0644)

	// ===== MONITORING/PROMETHEUS NODE =====
	prometheusPath := filepath.Join(monitoringPath, "prometheus")
	os.MkdirAll(prometheusPath, 0755)

	prometheusYAML := `id: "prometheus"
title: "Prometheus"
description: "Prometheus metrics and queries"
`
	os.WriteFile(filepath.Join(prometheusPath, "node.yaml"), []byte(prometheusYAML), 0644)

	prometheusMD := `# Prometheus

Prometheus metrics and querying.

## Features

- Query metrics
- View dashboards
- Alert management
`
	os.WriteFile(filepath.Join(prometheusPath, "node.md"), []byte(prometheusMD), 0644)

	// ===== DOCUMENTS NODE =====
	documentsPath := filepath.Join(rootPath, "documents")
	os.MkdirAll(documentsPath, 0755)

	documentsYAML := `id: "documents"
title: "Documentation"
description: "Documentation and reference materials"
`
	os.WriteFile(filepath.Join(documentsPath, "node.yaml"), []byte(documentsYAML), 0644)

	documentsMD := `# Documentation

Documentation and reference materials.

## Sections

- API documentation
- Service documentation
- Architecture guides
`
	os.WriteFile(filepath.Join(documentsPath, "node.md"), []byte(documentsMD), 0644)

	// ===== DOCUMENTS/API-GATEWAY NODE =====
	apiGatewayPath := filepath.Join(documentsPath, "api-gateway")
	os.MkdirAll(apiGatewayPath, 0755)

	apiGatewayYAML := `id: "api-gateway"
title: "API Gateway"
description: "API Gateway service documentation"
`
	os.WriteFile(filepath.Join(apiGatewayPath, "node.yaml"), []byte(apiGatewayYAML), 0644)

	apiGatewayMD := `# API Gateway

API Gateway service documentation.

## Overview

The API Gateway handles routing and authentication for all API requests.
`
	os.WriteFile(filepath.Join(apiGatewayPath, "node.md"), []byte(apiGatewayMD), 0644)

	return tmpDir
}

// TestScenario_TBD is a test scenario written in TDD approach
// This test will be developed incrementally based on requirements
//
// TDD Workflow:
// 1. Write failing test (RED)
// 2. Write minimal code to pass (GREEN)
// 3. Refactor (REFACTOR)
// 4. Repeat
func TestScenario_TBD(t *testing.T) {
	// ============================================
	// SETUP PHASE
	// ============================================
	// Create a knowledge tree similar to InfraAgent structure
	knowledgePath := createInfraAgentLikeKnowledgeTree(t)
	defer os.RemoveAll(knowledgePath)

	// ============================================
	// TEST SCENARIO STEPS
	// ============================================
	// Step 1: Load the knowledge tree
	t.Run("Step 1: Load Knowledge Tree", func(t *testing.T) {
		// Initialize Agentize instance with the knowledge tree
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize instance: %v", err)
		}

		// Verify Agentize instance is created
		if ag == nil {
			t.Fatal("Agentize instance should not be nil")
		}

		// Verify root node exists
		root := ag.GetRoot()
		if root == nil {
			t.Fatal("Root node should not be nil")
		}
		if root.Path != "root" {
			t.Errorf("Expected root path 'root', got '%s'", root.Path)
		}
		if root.ID != "root" {
			t.Errorf("Expected root ID 'root', got '%s'", root.ID)
		}
		if root.Title != "Infrastructure Assistant" {
			t.Errorf("Expected root title 'Infrastructure Assistant', got '%s'", root.Title)
		}

		// Verify root node has content
		if len(root.Content) == 0 {
			t.Error("Root node should have markdown content")
		}

		// Verify all expected nodes are loaded
		allNodes := ag.GetAllNodes()
		expectedNodePaths := []string{
			"root",
			"root/kubernetes",
			"root/kubernetes/deployments",
			"root/kubernetes/pods",
			"root/monitoring",
			"root/monitoring/prometheus",
			"root/documents",
			"root/documents/api-gateway",
		}

		if len(allNodes) != len(expectedNodePaths) {
			t.Errorf("Expected %d nodes, got %d. Nodes: %v", len(expectedNodePaths), len(allNodes), ag.GetNodePaths())
		}

		// Verify each expected node exists
		for _, expectedPath := range expectedNodePaths {
			node, err := ag.GetNode(expectedPath)
			if err != nil {
				t.Errorf("Failed to get node '%s': %v", expectedPath, err)
				continue
			}
			if node.Path != expectedPath {
				t.Errorf("Node path mismatch: expected '%s', got '%s'", expectedPath, node.Path)
			}
		}

		// Verify node paths are returned in correct order
		paths := ag.GetNodePaths()
		if len(paths) != len(expectedNodePaths) {
			t.Errorf("Expected %d paths, got %d", len(expectedNodePaths), len(paths))
		}

		// Verify root has tools loaded
		if len(root.Tools) == 0 {
			t.Error("Root node should have tools loaded from tools.json")
		}

		// Verify kubernetes node has tools
		kubernetesNode, err := ag.GetNode("root/kubernetes")
		if err != nil {
			t.Fatalf("Failed to get kubernetes node: %v", err)
		}
		if len(kubernetesNode.Tools) == 0 {
			t.Error("Kubernetes node should have tools loaded")
		}

		// Verify monitoring node has tools
		monitoringNode, err := ag.GetNode("root/monitoring")
		if err != nil {
			t.Fatalf("Failed to get monitoring node: %v", err)
		}
		if len(monitoringNode.Tools) == 0 {
			t.Error("Monitoring node should have tools loaded")
		}

		t.Logf("Successfully loaded knowledge tree with %d nodes", len(allNodes))
	})

	// Step 2: Verify node content and structure
	t.Run("Step 2: Verify Node Content and Structure", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize instance: %v", err)
		}

		// Verify root node content
		root, err := ag.GetNode("root")
		if err != nil {
			t.Fatalf("Failed to get root node: %v", err)
		}
		if !strings.Contains(root.Content, "Infrastructure Operations Assistant") {
			t.Error("Root content should contain 'Infrastructure Operations Assistant'")
		}

		// Verify kubernetes node
		kubernetesNode, err := ag.GetNode("root/kubernetes")
		if err != nil {
			t.Fatalf("Failed to get kubernetes node: %v", err)
		}
		if kubernetesNode.Title != "Kubernetes Operations" {
			t.Errorf("Expected kubernetes title 'Kubernetes Operations', got '%s'", kubernetesNode.Title)
		}
		if len(kubernetesNode.Content) == 0 {
			t.Error("Kubernetes node should have markdown content")
		}

		// Verify deployments node
		deploymentsNode, err := ag.GetNode("root/kubernetes/deployments")
		if err != nil {
			t.Fatalf("Failed to get deployments node: %v", err)
		}
		if deploymentsNode.Title != "Kubernetes Deployments" {
			t.Errorf("Expected deployments title 'Kubernetes Deployments', got '%s'", deploymentsNode.Title)
		}

		// Verify monitoring node
		monitoringNode, err := ag.GetNode("root/monitoring")
		if err != nil {
			t.Fatalf("Failed to get monitoring node: %v", err)
		}
		if monitoringNode.Title != "Monitoring and Observability" {
			t.Errorf("Expected monitoring title 'Monitoring and Observability', got '%s'", monitoringNode.Title)
		}

		t.Log("Node content and structure verified successfully")
	})

	// Step 3: Verify tools aggregation
	t.Run("Step 3: Verify Tools Loading", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize instance: %v", err)
		}

		// Verify root tools
		root, err := ag.GetNode("root")
		if err != nil {
			t.Fatalf("Failed to get root node: %v", err)
		}
		if len(root.Tools) < 2 {
			t.Errorf("Root should have at least 2 tools, got %d", len(root.Tools))
		}

		// Check for expected root tools
		toolNames := make(map[string]bool)
		for _, tool := range root.Tools {
			toolNames[tool.Name] = true
		}
		if !toolNames["clarify_question"] {
			t.Error("Root should have 'clarify_question' tool")
		}
		if !toolNames["send_message"] {
			t.Error("Root should have 'send_message' tool")
		}

		// Verify kubernetes tools
		kubernetesNode, err := ag.GetNode("root/kubernetes")
		if err != nil {
			t.Fatalf("Failed to get kubernetes node: %v", err)
		}
		if len(kubernetesNode.Tools) < 3 {
			t.Errorf("Kubernetes should have at least 3 tools, got %d", len(kubernetesNode.Tools))
		}

		// Check for expected kubernetes tools
		k8sToolNames := make(map[string]bool)
		for _, tool := range kubernetesNode.Tools {
			k8sToolNames[tool.Name] = true
		}
		if !k8sToolNames["get_namespaces"] {
			t.Error("Kubernetes should have 'get_namespaces' tool")
		}
		if !k8sToolNames["get_namespace_pods"] {
			t.Error("Kubernetes should have 'get_namespace_pods' tool")
		}
		if !k8sToolNames["get_deployments"] {
			t.Error("Kubernetes should have 'get_deployments' tool")
		}

		// Verify monitoring tools
		monitoringNode, err := ag.GetNode("root/monitoring")
		if err != nil {
			t.Fatalf("Failed to get monitoring node: %v", err)
		}
		if len(monitoringNode.Tools) < 2 {
			t.Errorf("Monitoring should have at least 2 tools, got %d", len(monitoringNode.Tools))
		}

		t.Log("Tools loading verified successfully")
	})

	// ============================================
	// CLEANUP PHASE (handled by defer)
	// ============================================
	t.Log("Knowledge tree loading test completed successfully")
}
