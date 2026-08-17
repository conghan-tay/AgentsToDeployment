package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/example/support-agent/services/gateway/internal/agentclient"
	"github.com/example/support-agent/services/gateway/internal/config"
	"github.com/example/support-agent/services/gateway/internal/httpapi"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.FromEnvironment()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	upstream, err := agentclient.New(
		cfg.AgentServiceURL,
		cfg.InternalAPIKey,
		&http.Client{Timeout: cfg.RequestTimeout},
		&http.Client{Timeout: cfg.StreamTimeout},
	)
	if err != nil {
		logger.Error("invalid agent client configuration", "error", err)
		os.Exit(1)
	}
	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Error("invalid Redis URL", "error", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()
	limiter := httpapi.NewRedisRateLimiter(redisClient, cfg.RateLimit, time.Minute)
	server := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: httpapi.New(
			cfg.APIKey, upstream, limiter, logger, cfg.RequestTimeout, cfg.StreamTimeout,
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("gateway listening", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
