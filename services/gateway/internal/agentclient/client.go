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

type Client interface {
	Do(ctx context.Context, method, path string, body []byte, requestID string) (Response, error)
}

type HTTPClient struct {
	baseURL     *url.URL
	internalKey string
	client      *http.Client
}

func New(rawBaseURL, internalKey string, client *http.Client) (*HTTPClient, error) {
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid agent service URL: %q", rawBaseURL)
	}
	return &HTTPClient{baseURL: baseURL, internalKey: internalKey, client: client}, nil
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
