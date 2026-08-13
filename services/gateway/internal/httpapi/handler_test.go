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
	lastPath string
	response agentclient.Response
}

func (f *fakeClient) Do(
	_ context.Context, _ string, path string, _ []byte, _ string,
) (agentclient.Response, error) {
	f.lastPath = path
	return f.response, nil
}

func TestAuthenticationIsRequired(t *testing.T) {
	client := &fakeClient{}
	handler := New(
		"secret", client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second,
	)
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
	handler := New(
		"secret", client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second,
	)
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

func TestMalformedJSONIsRejectedBeforeUpstream(t *testing.T) {
	client := &fakeClient{}
	handler := New(
		"secret", client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets", strings.NewReader(`{"message":`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
