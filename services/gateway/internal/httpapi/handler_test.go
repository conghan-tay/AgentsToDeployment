package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/support-agent/services/gateway/internal/agentclient"
)

type fakeClient struct {
	lastPath   string
	response   agentclient.Response
	streamBody string
	streamCode int
}

func (f *fakeClient) Do(
	_ context.Context, _ string, path string, _ []byte, _ string,
) (agentclient.Response, error) {
	f.lastPath = path
	return f.response, nil
}

func (f *fakeClient) Stream(
	_ context.Context, path, _ string,
) (agentclient.StreamResponse, error) {
	f.lastPath = path
	code := f.streamCode
	if code == 0 {
		code = http.StatusOK
	}
	return agentclient.StreamResponse{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(f.streamBody)),
	}, nil
}

func newTestHandler(client agentclient.Client) http.Handler {
	return New(
		"secret",
		client,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Second,
		5*time.Second,
	)
}

func TestAuthenticationIsRequired(t *testing.T) {
	client := &fakeClient{}
	handler := newTestHandler(client)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickets/t-1", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestCreateTicketProxiesValidatedJSON(t *testing.T) {
	client := &fakeClient{response: agentclient.Response{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"ticket_id":"t-1","status":"completed"}`),
	}}
	handler := newTestHandler(client)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if client.lastPath != "/internal/v1/runs" {
		t.Fatalf("path = %q", client.lastPath)
	}
}

func TestCreateTicketAsyncProxiesToAsyncRun(t *testing.T) {
	client := &fakeClient{response: agentclient.Response{
		StatusCode: http.StatusAccepted,
		Body:       []byte(`{"ticket_id":"t-1","status":"running"}`),
	}}
	handler := newTestHandler(client)
	req := httptest.NewRequest(
		http.MethodPost, "/v1/tickets/async", strings.NewReader(`{"message":"hello"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if client.lastPath != "/internal/v1/runs/async" {
		t.Fatalf("path = %q", client.lastPath)
	}
}

func TestGetTicketStreamingForwardsEvents(t *testing.T) {
	client := &fakeClient{
		streamBody: "event: status\ndata: running...\n\n" +
			"event: result\ndata: {\"ticket_id\":\"t-1\",\"status\":\"completed\"}\n\n",
	}
	handler := newTestHandler(client)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickets/t-1/stream", nil)
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	if client.lastPath != "/internal/v1/runs/t-1/stream" {
		t.Fatalf("path = %q", client.lastPath)
	}
	if recorder.Body.String() != client.streamBody {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestGetTicketStreamingPassesUpstreamErrorsThroughAsJSON(t *testing.T) {
	client := &fakeClient{
		streamCode: http.StatusNotFound,
		streamBody: `{"detail":"ticket not found"}`,
	}
	handler := newTestHandler(client)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickets/missing/stream", nil)
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
}

func TestMalformedJSONIsRejectedBeforeUpstream(t *testing.T) {
	client := &fakeClient{}
	handler := newTestHandler(client)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets", strings.NewReader(`{"message":`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
