package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/example/support-agent/services/gateway/internal/agentclient"
)

const maxRequestBytes = 1 << 20

const streamCopyBufferBytes = 4 << 10

type Handler struct {
	apiKey        string
	client        agentclient.Client
	limiter       RateLimiter
	logger        *slog.Logger
	timeout       time.Duration
	streamTimeout time.Duration
}

func New(
	apiKey string,
	client agentclient.Client,
	limiter RateLimiter,
	logger *slog.Logger,
	timeout time.Duration,
	streamTimeout time.Duration,
) http.Handler {
	if limiter == nil {
		limiter = allowAllLimiter{}
	}
	h := &Handler{
		apiKey:        apiKey,
		client:        client,
		limiter:       limiter,
		logger:        logger,
		timeout:       timeout,
		streamTimeout: streamTimeout,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /v1/tickets", h.createTicket)
	mux.HandleFunc("POST /v1/tickets/async", h.createTicketAsync)
	mux.HandleFunc("GET /v1/tickets/{ticketID}", h.getTicket)
	mux.HandleFunc("GET /v1/tickets/{ticketID}/stream", h.getTicketStreaming)
	mux.HandleFunc("POST /v1/tickets/{ticketID}/decision", h.decideTicket)
	mux.HandleFunc("POST /v1/knowledge", h.upsertKnowledge)
	return h.requestContext(h.authenticate(mux))
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []byte(`{"status":"ok","service":"gateway"}`))
}

func (h *Handler) createTicket(w http.ResponseWriter, r *http.Request) {
	h.proxyJSON(w, r, "/internal/v1/runs")
}

func (h *Handler) createTicketAsync(w http.ResponseWriter, r *http.Request) {
	h.proxyJSON(w, r, "/internal/v1/runs/async")
}

func (h *Handler) getTicketStreaming(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("ticketID")
	if !validPathID(ticketID) {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	h.proxyStream(w, r, "/internal/v1/runs/"+ticketID+"/stream")
}

func (h *Handler) getTicket(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("ticketID")
	if !validPathID(ticketID) {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	h.proxyJSON(w, r, "/internal/v1/runs/"+ticketID)
}

func (h *Handler) decideTicket(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("ticketID")
	if !validPathID(ticketID) {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	h.proxyJSON(w, r, "/internal/v1/runs/"+ticketID+"/decision")
}

func (h *Handler) upsertKnowledge(w http.ResponseWriter, r *http.Request) {
	h.proxyJSON(w, r, "/internal/v1/knowledge")
}

func (h *Handler) proxyJSON(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	started := time.Now()
	requestID := requestIDFrom(r.Context())

	if r.Method != http.MethodGet && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	if len(body) > 0 && !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "request body must be valid JSON")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	response, err := h.client.Do(ctx, r.Method, upstreamPath, body, requestIDFrom(r.Context()))
	if err != nil {
		h.logger.Error("agent request failed", "error", err, "request_id", requestIDFrom(r.Context()))
		writeError(w, http.StatusBadGateway, "agent service unavailable")
		return
	}
	h.logger.Info(
		"gateway request completed",
		"request_id", requestID,
		"method", r.Method,
		"path", r.URL.Path,
		"upstream_path", upstreamPath,
		"status", response.StatusCode,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	writeJSON(w, response.StatusCode, response.Body)
}

// proxyStream forwards a server-sent-event response chunk by chunk. It deliberately does
// not reuse proxyJSON: the body must not be buffered, and the run may outlive the ordinary
// request timeout.
func (h *Handler) proxyStream(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	started := time.Now()
	requestID := requestIDFrom(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), h.streamTimeout)
	defer cancel()
	upstream, err := h.client.Stream(ctx, upstreamPath, requestID)
	if err != nil {
		h.logger.Error("agent stream request failed", "error", err, "request_id", requestID)
		writeError(w, http.StatusBadGateway, "agent service unavailable")
		return
	}
	defer upstream.Body.Close()
	if upstream.StatusCode != http.StatusOK {
		// Errors such as 404 or 401 stay ordinary JSON responses.
		body, readErr := io.ReadAll(io.LimitReader(upstream.Body, maxRequestBytes))
		if readErr != nil {
			writeError(w, http.StatusBadGateway, "agent service unavailable")
			return
		}
		writeJSON(w, upstream.StatusCode, body)
		return
	}

	controller := http.NewResponseController(w)
	// The server's global write deadline is sized for short requests; a stream needs its own.
	if err := controller.SetWriteDeadline(time.Now().Add(h.streamTimeout)); err != nil {
		h.logger.Warn("stream write deadline unsupported", "error", err, "request_id", requestID)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		h.logger.Error("streaming is unsupported by this writer", "error", err)
		return
	}

	buffer := make([]byte, streamCopyBufferBytes)
	for {
		read, readErr := upstream.Body.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				break
			}
			if flushErr := controller.Flush(); flushErr != nil {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	h.logger.Info(
		"gateway stream completed",
		"request_id", requestID,
		"path", r.URL.Path,
		"upstream_path", upstreamPath,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-API-Key")
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(h.apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		allowed, err := h.limiter.Allow(r.Context(), rateLimitKey(r))
		if err != nil {
			// Rate limiting fails open so a Redis outage does not take down support.
			h.logger.Warn("rate limiter unavailable", "error", err)
		} else if !allowed {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rateLimitKey(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		if addr, err := netip.ParseAddr(forwarded); err == nil {
			return addr.String()
		}
	}
	if host, _, ok := strings.Cut(r.RemoteAddr, ":"); ok {
		return host
	}
	return r.RemoteAddr
}

type contextKey string

const requestIDKey contextKey = "request-id"

func (h *Handler) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 100 {
			requestID = fmt.Sprintf("req-%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID)))
	})
}

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func validPathID(value string) bool {
	return value != "" && len(value) <= 100 && !strings.ContainsAny(value, "/\\")
}

func writeError(w http.ResponseWriter, status int, message string) {
	payload, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		panic(errors.New("marshal static error response"))
	}
	writeJSON(w, status, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
