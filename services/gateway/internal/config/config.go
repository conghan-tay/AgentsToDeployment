package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config contains only edge-service concerns: how to reach the agent runtime and the
// knowledge store, plus the policies the public API enforces. Model and graph
// configuration belongs to the worker and never leaks into this process.
type Config struct {
	Environment string
	Port        string
	APIKey      string
	RedisURL    string
	RateLimit   int
	// RequestTimeout bounds a single gateway->dependency call. It is not the agent's
	// working time: runs are durable and continue past any HTTP request.
	RequestTimeout time.Duration

	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalAPIKey    string
	TemporalTLS       bool

	// ApprovalTimeoutHours is passed into each workflow. Workflow code cannot read the
	// environment, so this policy is owned here and travels as a workflow argument.
	// 0 waits for a reviewer indefinitely.
	ApprovalTimeoutHours int

	ChromaURL        string
	ChromaTenant     string
	ChromaDatabase   string
	ChromaCollection string
	OpenAIBaseURL    string
	OpenAIAPIKey     string
	// EmbeddingModel must match EMBEDDING_MODEL on the worker; the worker queries the
	// vectors this service writes.
	EmbeddingModel string
}

func FromEnvironment() (Config, error) {
	approvalTimeout, err := envInt("APPROVAL_TIMEOUT_HOURS", 72)
	if err != nil {
		return Config{}, err
	}
	rateLimit, err := envInt("RATE_LIMIT_PER_MINUTE", 60)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment:    envOr("ENVIRONMENT", "development"),
		Port:           envOr("GATEWAY_PORT", "8080"),
		APIKey:         envOr("API_KEY", "local-api-key"),
		RedisURL:       envOr("REDIS_URL", "redis://localhost:6379/0"),
		RateLimit:      rateLimit,
		RequestTimeout: 30 * time.Second,

		TemporalAddress:   envOr("TEMPORAL_ADDRESS", "localhost:7233"),
		TemporalNamespace: envOr("TEMPORAL_NAMESPACE", "default"),
		TemporalTaskQueue: envOr("TEMPORAL_TASK_QUEUE", "support-agent"),
		TemporalAPIKey:    os.Getenv("TEMPORAL_API_KEY"),
		TemporalTLS:       envOr("TEMPORAL_TLS", "false") == "true",

		ApprovalTimeoutHours: approvalTimeout,

		ChromaURL:        envOr("CHROMA_URL", "http://localhost:8000"),
		ChromaTenant:     envOr("CHROMA_TENANT", "default_tenant"),
		ChromaDatabase:   envOr("CHROMA_DATABASE", "default_database"),
		ChromaCollection: envOr("CHROMA_COLLECTION", "support_knowledge"),
		OpenAIBaseURL:    envOr("OPENAI_BASE_URL", "https://api.openai.com"),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		EmbeddingModel:   envOr("EMBEDDING_MODEL", "text-embedding-3-small"),
	}
	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf("API_KEY must not be empty")
	}
	if cfg.Environment == "production" && cfg.APIKey == "local-api-key" {
		return Config{}, fmt.Errorf("demo API keys are not allowed in production")
	}
	if cfg.OpenAIAPIKey == "" {
		// Knowledge ingestion embeds through OpenAI regardless of the chat provider,
		// so an empty key means POST /v1/knowledge can only ever fail.
		return Config{}, fmt.Errorf("OPENAI_API_KEY is required to embed knowledge documents")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative", key)
	}
	return value, nil
}
