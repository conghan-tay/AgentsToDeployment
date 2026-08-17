package config

import (
	"fmt"
	"os"
	"time"
)

// Config contains only edge-service concerns. Model and workflow configuration
// belongs to the Python service and never leaks into the public API process.
type Config struct {
	Environment     string
	Port            string
	APIKey          string
	InternalAPIKey  string
	AgentServiceURL string
	RedisURL        string
	RateLimit       int
	RequestTimeout  time.Duration
	// StreamTimeout bounds a server-sent-event connection. The agent caps its own stream
	// first (STREAM_TIMEOUT_SECONDS, 300s by default); this is the edge backstop.
	StreamTimeout time.Duration
}

func FromEnvironment() (Config, error) {
	cfg := Config{
		Environment:     envOr("ENVIRONMENT", "development"),
		Port:            envOr("GATEWAY_PORT", "8080"),
		APIKey:          envOr("API_KEY", "local-api-key"),
		InternalAPIKey:  envOr("INTERNAL_API_KEY", "local-internal-key"),
		AgentServiceURL: envOr("AGENT_SERVICE_URL", "http://localhost:8000"),
		RedisURL:        envOr("REDIS_URL", "redis://localhost:6379/0"),
		RateLimit:       60,
		RequestTimeout:  90 * time.Second,
		StreamTimeout:   330 * time.Second,
	}
	if cfg.APIKey == "" || cfg.InternalAPIKey == "" {
		return Config{}, fmt.Errorf("API_KEY and INTERNAL_API_KEY must not be empty")
	}
	if cfg.Environment == "production" &&
		(cfg.APIKey == "local-api-key" || cfg.InternalAPIKey == "local-internal-key") {
		return Config{}, fmt.Errorf("demo API keys are not allowed in production")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
