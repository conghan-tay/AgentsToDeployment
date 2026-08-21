package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/support-agent/services/gateway/internal/api"
	"github.com/example/support-agent/services/gateway/internal/tickets"
)

type fakeTickets struct {
	started   *api.TicketRequest
	decided   *api.ApprovalDecision
	ticketID  string
	state     api.RunState
	returnErr error
}

func (f *fakeTickets) Start(_ context.Context, request api.TicketRequest) (api.RunState, error) {
	f.started = &request
	if f.returnErr != nil {
		return api.RunState{}, f.returnErr
	}
	return f.state, nil
}

func (f *fakeTickets) Get(_ context.Context, ticketID string) (api.RunState, error) {
	f.ticketID = ticketID
	if f.returnErr != nil {
		return api.RunState{}, f.returnErr
	}
	return f.state, nil
}

func (f *fakeTickets) Decide(
	_ context.Context, ticketID string, decision api.ApprovalDecision,
) (api.RunState, error) {
	f.ticketID = ticketID
	f.decided = &decision
	if f.returnErr != nil {
		return api.RunState{}, f.returnErr
	}
	return f.state, nil
}

type fakeKnowledge struct {
	documents []api.KnowledgeDocument
	returnErr error
}

func (f *fakeKnowledge) Upsert(
	_ context.Context, documents []api.KnowledgeDocument,
) (int, error) {
	f.documents = documents
	if f.returnErr != nil {
		return 0, f.returnErr
	}
	return len(documents), nil
}

func newHandler(service *fakeTickets, repository *fakeKnowledge) http.Handler {
	if service == nil {
		service = &fakeTickets{}
	}
	if repository == nil {
		repository = &fakeKnowledge{}
	}
	return New(Options{
		APIKey:    "secret",
		Tickets:   service,
		Knowledge: repository,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:   time.Second,
	})
}

func do(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("X-API-Key", "secret")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAuthenticationIsRequired(t *testing.T) {
	handler := newHandler(nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/tickets/ticket-1", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestHealthzDoesNotRequireAuthentication(t *testing.T) {
	handler := newHandler(nil, nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestCreateTicketAcceptsAndReturnsTicketID(t *testing.T) {
	service := &fakeTickets{
		state: api.RunState{TicketID: "ticket-1", Status: api.StatusRunning},
	}
	handler := newHandler(service, nil)

	recorder := do(
		handler, http.MethodPost, "/v1/tickets",
		`{"customer_id":"c-1","message":"Where is my order?","order_id":"order-1"}`,
	)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var state api.RunState
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if state.TicketID != "ticket-1" || state.Status != api.StatusRunning {
		t.Fatalf("state = %+v", state)
	}
	if service.started == nil || service.started.CustomerID != "c-1" {
		t.Fatalf("request was not forwarded: %+v", service.started)
	}
	// Defaults are applied at the edge so the workflow never sees an empty channel.
	if service.started.Channel != "api" {
		t.Fatalf("channel = %q, want %q", service.started.Channel, "api")
	}
}

func TestMalformedJSONIsRejectedBeforeTheAgentRuntime(t *testing.T) {
	service := &fakeTickets{}
	handler := newHandler(service, nil)

	recorder := do(handler, http.MethodPost, "/v1/tickets", `{"message":`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if service.started != nil {
		t.Fatal("invalid request reached the agent runtime")
	}
}

func TestValidationRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name   string
		target string
		body   string
		want   string
	}{
		{
			name:   "missing customer id",
			target: "/v1/tickets",
			body:   `{"message":"hello"}`,
			want:   "customer_id",
		},
		{
			name:   "empty message",
			target: "/v1/tickets",
			body:   `{"customer_id":"c-1","message":""}`,
			want:   "message",
		},
		{
			name:   "unknown channel",
			target: "/v1/tickets",
			body:   `{"customer_id":"c-1","message":"hi","channel":"carrier-pigeon"}`,
			want:   "channel",
		},
		{
			name:   "unknown decision",
			target: "/v1/tickets/ticket-1/decision",
			body:   `{"decision":"maybe","reviewer":"m-1"}`,
			want:   "decision",
		},
		{
			name:   "missing reviewer",
			target: "/v1/tickets/ticket-1/decision",
			body:   `{"decision":"approve"}`,
			want:   "reviewer",
		},
		{
			name:   "empty document list",
			target: "/v1/knowledge",
			body:   `{"documents":[]}`,
			want:   "documents",
		},
		{
			name:   "document missing content",
			target: "/v1/knowledge",
			body:   `{"documents":[{"id":"a","title":"A","content":"","source":"a"}]}`,
			want:   "documents[0].content",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeTickets{}
			repository := &fakeKnowledge{}
			handler := newHandler(service, repository)

			recorder := do(handler, http.MethodPost, testCase.target, testCase.body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), testCase.want) {
				t.Fatalf("body = %s, want mention of %q", recorder.Body.String(), testCase.want)
			}
			if service.started != nil || service.decided != nil || repository.documents != nil {
				t.Fatal("invalid request reached a downstream dependency")
			}
		})
	}
}

func TestUnknownTicketIsNotFound(t *testing.T) {
	handler := newHandler(&fakeTickets{returnErr: tickets.ErrNotFound}, nil)

	recorder := do(handler, http.MethodGet, "/v1/tickets/ticket-missing", "")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestDecisionOnARunningTicketIsAConflict(t *testing.T) {
	handler := newHandler(&fakeTickets{returnErr: tickets.ErrNotAwaiting}, nil)

	recorder := do(
		handler, http.MethodPost, "/v1/tickets/ticket-1/decision",
		`{"decision":"approve","reviewer":"m-1"}`,
	)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

func TestAgentRuntimeFailureIsABadGateway(t *testing.T) {
	handler := newHandler(&fakeTickets{returnErr: errors.New("connection refused")}, nil)

	recorder := do(handler, http.MethodGet, "/v1/tickets/ticket-1", "")

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	// The upstream error text must not leak to the caller.
	if strings.Contains(recorder.Body.String(), "connection refused") {
		t.Fatalf("body leaked upstream detail: %s", recorder.Body.String())
	}
}

func TestTimeoutIsAGatewayTimeout(t *testing.T) {
	handler := newHandler(&fakeTickets{returnErr: context.DeadlineExceeded}, nil)

	recorder := do(handler, http.MethodGet, "/v1/tickets/ticket-1", "")

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusGatewayTimeout)
	}
}

func TestDecisionIsForwardedToTheNamedTicket(t *testing.T) {
	service := &fakeTickets{
		state: api.RunState{TicketID: "ticket-7", Status: api.StatusRunning},
	}
	handler := newHandler(service, nil)

	recorder := do(
		handler, http.MethodPost, "/v1/tickets/ticket-7/decision",
		`{"decision":"approve","reviewer":"manager-7","comment":"Policy verified"}`,
	)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.ticketID != "ticket-7" {
		t.Fatalf("ticket id = %q", service.ticketID)
	}
	if service.decided == nil || service.decided.Decision != "approve" {
		t.Fatalf("decision = %+v", service.decided)
	}
}

func TestKnowledgeUpsertReportsCount(t *testing.T) {
	repository := &fakeKnowledge{}
	handler := newHandler(nil, repository)

	recorder := do(
		handler, http.MethodPost, "/v1/knowledge",
		`{"documents":[{"id":"a","title":"A","content":"body","source":"a"}]}`,
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response api.KnowledgeUpsertResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Upserted != 1 {
		t.Fatalf("upserted = %d, want 1", response.Upserted)
	}
}

func TestTicketIDsWithPathSeparatorsAreRejected(t *testing.T) {
	service := &fakeTickets{}
	handler := newHandler(service, nil)

	recorder := do(handler, http.MethodGet, "/v1/tickets/..%2Fadmin", "")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if service.ticketID != "" {
		t.Fatalf("traversal attempt reached the agent runtime: %q", service.ticketID)
	}
}
