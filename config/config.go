package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds the application configuration
type Config struct {
	// HTTP server configuration
	HTTP HTTPConfig

	// Feature flags
	Features FeatureFlags

	// Knowledge tree path
	KnowledgePath string

	// Scheduler configuration
	Scheduler SchedulerConfig
}

// HTTPConfig holds HTTP server configuration
type HTTPConfig struct {
	Enabled bool
	Host    string
	Port    int
}

// FeatureFlags holds feature flag settings
type FeatureFlags struct {
	HTTPServerEnabled         bool
	GraphVisualizationEnabled bool
}

// SchedulerConfig holds scheduler configuration
type SchedulerConfig struct {
	Enabled                     bool
	CheckInterval               time.Duration
	FirstSummarizationThreshold int           // Min messages for first summarization (default: 5)
	SubsequentMessageThreshold  int           // Min messages for subsequent summarizations (default: 25)
	SubsequentTimeThreshold     time.Duration // Min time since last summarization (default: 1 hour)
	LastActivityThreshold       time.Duration // Session must be active within this time (default: 1 hour)
	SummaryModel                string
	DisableLogs                 bool // If true, SessionScheduler does not emit any logs
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	cfg := &Config{
		HTTP: HTTPConfig{
			Enabled: getEnvBool("AGENTIZE_HTTP_ENABLED", false),
			Host:    getEnvString("AGENTIZE_HTTP_HOST", "0.0.0.0"),
			Port:    getEnvInt("AGENTIZE_HTTP_PORT", 8080),
		},
		Features: FeatureFlags{
			HTTPServerEnabled:         getEnvBool("AGENTIZE_FEATURE_HTTP", false),
			GraphVisualizationEnabled: getEnvBool("AGENTIZE_FEATURE_GRAPH", true),
		},
		KnowledgePath: getEnvString("AGENTIZE_KNOWLEDGE_PATH", "./knowledge"),
		Scheduler:     loadSchedulerConfig(),
	}

	// HTTP is enabled if both HTTP config and feature flag are enabled
	cfg.HTTP.Enabled = cfg.HTTP.Enabled && cfg.Features.HTTPServerEnabled

	return cfg, nil
}

// loadSchedulerConfig loads scheduler configuration from environment variables
func loadSchedulerConfig() SchedulerConfig {
	// Parse durations from environment (in minutes, convert to time.Duration)
	checkIntervalMinutes := getEnvInt("AGENTIZE_SCHEDULER_CHECK_INTERVAL_MINUTES", 5)
	subsequentTimeThresholdMinutes := getEnvInt("AGENTIZE_SCHEDULER_SUBSEQUENT_TIME_THRESHOLD_MINUTES", 60)
	lastActivityThresholdMinutes := getEnvInt("AGENTIZE_SCHEDULER_LAST_ACTIVITY_THRESHOLD_MINUTES", 60)

	// Scheduler is enabled by default (true), only disable if explicitly set to false via env var
	enabled := true
	if envVal := os.Getenv("AGENTIZE_SCHEDULER_ENABLED"); envVal != "" {
		if enabledVal, err := strconv.ParseBool(envVal); err == nil {
			enabled = enabledVal
		}
	}

	return SchedulerConfig{
		Enabled:                     enabled,
		CheckInterval:               time.Duration(checkIntervalMinutes) * time.Minute,
		FirstSummarizationThreshold: getEnvInt("AGENTIZE_SCHEDULER_FIRST_THRESHOLD", 5),
		SubsequentMessageThreshold:  getEnvInt("AGENTIZE_SCHEDULER_SUBSEQUENT_MESSAGE_THRESHOLD", 25),
		SubsequentTimeThreshold:     time.Duration(subsequentTimeThresholdMinutes) * time.Minute,
		LastActivityThreshold:       time.Duration(lastActivityThresholdMinutes) * time.Minute,
		SummaryModel:                getEnvString("AGENTIZE_SCHEDULER_SUMMARY_MODEL", "gpt-4o-mini"),
		DisableLogs:                 getEnvBool("AGENTIZE_SCHEDULER_DISABLE_LOGS", false),
	}
}

// GetAddress returns the HTTP server address
func (c *Config) GetAddress() string {
	return fmt.Sprintf("%s:%d", c.HTTP.Host, c.HTTP.Port)
}

// Helper functions for environment variables
func getEnvString(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}
