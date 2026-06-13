package config

import (
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Host != "0.0.0.0" || cfg.HTTP.Port != 8080 {
		t.Errorf("HTTP defaults: %+v", cfg.HTTP)
	}
	if cfg.HTTP.Enabled {
		t.Error("HTTP must be disabled by default")
	}
	if cfg.KnowledgePath != "./knowledge" {
		t.Errorf("KnowledgePath default: %q", cfg.KnowledgePath)
	}
	if !cfg.Scheduler.Enabled {
		t.Error("scheduler must be enabled by default")
	}
	if cfg.Scheduler.CheckInterval != 5*time.Minute {
		t.Errorf("CheckInterval default: %v", cfg.Scheduler.CheckInterval)
	}
	if cfg.Scheduler.FirstSummarizationThreshold != 5 {
		t.Errorf("FirstSummarizationThreshold default: %d", cfg.Scheduler.FirstSummarizationThreshold)
	}
	if cfg.GetAddress() != "0.0.0.0:8080" {
		t.Errorf("GetAddress: %q", cfg.GetAddress())
	}
}

func TestLoad_FromEnvironment(t *testing.T) {
	t.Setenv("AGENTIZE_HTTP_ENABLED", "true")
	t.Setenv("AGENTIZE_FEATURE_HTTP", "true")
	t.Setenv("AGENTIZE_HTTP_HOST", "127.0.0.1")
	t.Setenv("AGENTIZE_HTTP_PORT", "9999")
	t.Setenv("AGENTIZE_KNOWLEDGE_PATH", "/tmp/knowledge")
	t.Setenv("AGENTIZE_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTIZE_SCHEDULER_CHECK_INTERVAL_MINUTES", "10")
	t.Setenv("AGENTIZE_ADMIN_USERNAME", "admin")
	t.Setenv("AGENTIZE_ADMIN_PASSWORD", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.HTTP.Enabled || cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 9999 {
		t.Errorf("HTTP from env: %+v", cfg.HTTP)
	}
	if cfg.KnowledgePath != "/tmp/knowledge" {
		t.Errorf("KnowledgePath from env: %q", cfg.KnowledgePath)
	}
	if cfg.Scheduler.Enabled {
		t.Error("scheduler must honor AGENTIZE_SCHEDULER_ENABLED=false")
	}
	if cfg.Scheduler.CheckInterval != 10*time.Minute {
		t.Errorf("CheckInterval from env: %v", cfg.Scheduler.CheckInterval)
	}
	if cfg.Admin.Username != "admin" || cfg.Admin.Password != "secret" {
		t.Errorf("Admin from env: %+v", cfg.Admin)
	}
}

func TestLoad_HTTPRequiresBothFlags(t *testing.T) {
	t.Setenv("AGENTIZE_HTTP_ENABLED", "true")
	t.Setenv("AGENTIZE_FEATURE_HTTP", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Enabled {
		t.Error("HTTP.Enabled must require both AGENTIZE_HTTP_ENABLED and AGENTIZE_FEATURE_HTTP")
	}
}

func TestLoad_InvalidValuesFallBackToDefaults(t *testing.T) {
	t.Setenv("AGENTIZE_HTTP_PORT", "not-a-number")
	t.Setenv("AGENTIZE_SCHEDULER_ENABLED", "not-a-bool")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("invalid port must fall back to 8080, got %d", cfg.HTTP.Port)
	}
	if !cfg.Scheduler.Enabled {
		t.Error("invalid bool must fall back to default (enabled)")
	}
}
