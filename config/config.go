package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the application configuration
type Config struct {
	// HTTP server configuration
	HTTP HTTPConfig

	// Feature flags
	Features FeatureFlags

	// Knowledge tree path
	KnowledgePath string
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
	}

	// HTTP is enabled if both HTTP config and feature flag are enabled
	cfg.HTTP.Enabled = cfg.HTTP.Enabled && cfg.Features.HTTPServerEnabled

	return cfg, nil
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
