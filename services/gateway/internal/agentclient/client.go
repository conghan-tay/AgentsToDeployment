package agentclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxUpstreamResponseBytes = 2 << 20

type Response struct {
	StatusCode int
	Body       []byte
}

// StreamResponse hands the caller an open body. Unlike Response it is not read into
// memory, so the caller must close it.
type StreamResponse struct {
	StatusCode int
	Body       io.ReadCloser
}

type Client interface {
	Do(ctx context.Context, method, path string, body []byte, requestID string) (Response, error)
	Stream(ctx context.Context, path, requestID string) (StreamResponse, error)
}

type HTTPClient struct {
	baseURL      *url.URL
	internalKey  string
	client       *http.Client
	streamClient *http.Client
}

// New takes a separate streamClient because the request client's timeout covers the whole
// response body, which would sever a long server-sent-event stream mid-run.
func New(rawBaseURL, internalKey string, client, streamClient *http.Client) (*HTTPClient, error) {
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid agent service URL: %q", rawBaseURL)
	}
	return &HTTPClient{
		baseURL:      baseURL,
		internalKey:  internalKey,
		client:       client,
		streamClient: streamClient,
	}, nil
}

func (c *HTTPClient) Do(
	ctx context.Context, method, path string, body []byte, requestID string,
) (Response, error) {
	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-API-Key", c.internalKey)
	req.Header.Set("X-Request-ID", requestID)

	resp, err := c.client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("call agent service: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponseBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read agent response: %w", err)
	}
	return Response{StatusCode: resp.StatusCode, Body: responseBody}, nil
}

func (c *HTTPClient) Stream(
	ctx context.Context, path, requestID string,
) (StreamResponse, error) {
	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return StreamResponse{}, fmt.Errorf("build upstream stream request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Internal-API-Key", c.internalKey)
	req.Header.Set("X-Request-ID", requestID)

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return StreamResponse{}, fmt.Errorf("stream from agent service: %w", err)
	}
	// The body stays open; the caller copies it to the client and closes it.
	return StreamResponse{StatusCode: resp.StatusCode, Body: resp.Body}, nil
}
