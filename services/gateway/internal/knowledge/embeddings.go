package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxEmbeddingErrorBytes = 8 << 10

// OpenAIEmbedder calls the OpenAI embeddings endpoint over plain HTTP.
//
// Hand-rolled rather than pulled from an SDK: this is one request with three fields,
// and keeping it here means the gateway has no vendor client to keep upgrading. Swap
// this type to move to another embedding provider.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewOpenAIEmbedder(baseURL, apiKey, model string, client *http.Client) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		client:  client,
	}
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embeddingRequest{Model: e.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("encode embedding request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build embedding request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+e.apiKey)

	response, err := e.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call embeddings API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"embeddings API returned %d: %s", response.StatusCode, readSnippet(response.Body),
		)
	}

	var decoded embeddingResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf(
			"embeddings API returned %d vectors for %d inputs", len(decoded.Data), len(inputs),
		)
	}

	// The API documents index ordering but does not promise it, and a misaligned
	// vector attaches the wrong meaning to a document rather than failing loudly.
	vectors := make([][]float32, len(inputs))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("embeddings API returned out-of-range index %d", item.Index)
		}
		vectors[item.Index] = item.Embedding
	}
	for index, vector := range vectors {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embeddings API returned no vector for input %d", index)
		}
	}
	return vectors, nil
}

func readSnippet(reader io.Reader) string {
	snippet, err := io.ReadAll(io.LimitReader(reader, maxEmbeddingErrorBytes))
	if err != nil {
		return "<unreadable body>"
	}
	return strings.TrimSpace(string(snippet))
}
