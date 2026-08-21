package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"

	"github.com/example/support-agent/services/gateway/internal/config"
	"github.com/example/support-agent/services/gateway/internal/httpapi"
	"github.com/example/support-agent/services/gateway/internal/knowledge"
	"github.com/example/support-agent/services/gateway/internal/tickets"
)

const shutdownGrace = 20 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.FromEnvironment()
	if err != nil {
		return err
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Credentials: func() client.Credentials {
			if cfg.TemporalAPIKey == "" {
				return nil
			}
			return client.NewAPIKeyStaticCredentials(cfg.TemporalAPIKey)
		}(),
		ConnectionOptions: client.ConnectionOptions{TLS: tlsConfig(cfg)},
		Logger:            logger,
	})
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	httpClient := &http.Client{Timeout: cfg.RequestTimeout}
	embedder := knowledge.NewOpenAIEmbedder(
		cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.EmbeddingModel, httpClient,
	)
	knowledgeRepository := knowledge.NewChromaRepository(
		knowledge.ChromaConfig{
			BaseURL:    cfg.ChromaURL,
			Tenant:     cfg.ChromaTenant,
			Database:   cfg.ChromaDatabase,
			Collection: cfg.ChromaCollection,
		},
		embedder,
		httpClient,
	)

	handler := httpapi.New(httpapi.Options{
		APIKey: cfg.APIKey,
		Tickets: tickets.NewTemporalService(
			temporalClient, cfg.TemporalTaskQueue, cfg.ApprovalTimeoutHours,
		),
		Knowledge: knowledgeRepository,
		Health:    healthChecker{temporalClient},
		Limiter:   httpapi.NewRedisRateLimiter(redisClient, cfg.RateLimit, time.Minute),
		Logger:    logger,
		Timeout:   cfg.RequestTimeout,
	})

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(
			"gateway listening",
			"address", server.Addr,
			"temporal", cfg.TemporalAddress,
			"task_queue", cfg.TemporalTaskQueue,
		)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Drain in-flight requests so a deploy does not sever a caller mid-response.
		logger.Info("gateway shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func tlsConfig(cfg config.Config) *tls.Config {
	// Temporal Cloud requires TLS and is always reached with an API key, so enabling
	// TLS implicitly for keyed connections avoids a confusing misconfiguration.
	if !cfg.TemporalTLS && cfg.TemporalAPIKey == "" {
		return nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// healthChecker adapts the Temporal client to the handler's readiness interface.
type healthChecker struct {
	client client.Client
}

func (h healthChecker) CheckHealth(ctx context.Context) error {
	_, err := h.client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}
