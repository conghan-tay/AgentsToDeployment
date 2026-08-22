package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/example/support-agent/services/gateway/internal/api"
)

// ChromaConfig describes the collection this gateway writes into.
//
// Collection, DistanceMetric, and the embedding model form a contract with the Python
// worker's reader. Both sides get-or-create the collection, so whichever process boots
// first defines it; if they disagree, retrieval silently returns nonsense.
type ChromaConfig struct {
	BaseURL    string
	Tenant     string
	Database   string
	Collection string
}

// ChromaRepository upserts embedded documents into Chroma's v2 REST API.
type ChromaRepository struct {
	config   ChromaConfig
	embedder Embedder
	client   *http.Client

	// The collection id is resolved on first write rather than at construction, so the
	// gateway still starts and serves health checks when Chroma is down. A mutex rather
	// than sync.Once because a failed resolve must stay retryable.
	mu           sync.Mutex
	collectionID string
}

func NewChromaRepository(
	config ChromaConfig, embedder Embedder, client *http.Client,
) *ChromaRepository {
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	if config.Tenant == "" {
		config.Tenant = "default_tenant"
	}
	if config.Database == "" {
		config.Database = "default_database"
	}
	return &ChromaRepository{config: config, embedder: embedder, client: client}
}

func (r *ChromaRepository) collectionsURL() string {
	return fmt.Sprintf(
		"%s/api/v2/tenants/%s/databases/%s/collections",
		r.config.BaseURL, url.PathEscape(r.config.Tenant), url.PathEscape(r.config.Database),
	)
}

type createCollectionRequest struct {
	Name        string         `json:"name"`
	GetOrCreate bool           `json:"get_or_create"`
	Metadata    map[string]any `json:"metadata"`
}

type collectionResponse struct {
	ID string `json:"id"`
}

func (r *ChromaRepository) resolveCollection(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.collectionID != "" {
		return r.collectionID, nil
	}
	body, err := json.Marshal(createCollectionRequest{
		Name:        r.config.Collection,
		GetOrCreate: true,
		// Cosine distance must match the Python reader's collection metadata.
		Metadata: map[string]any{"hnsw:space": "cosine"},
	})
	if err != nil {
		return "", fmt.Errorf("encode collection request: %w", err)
	}
	var decoded collectionResponse
	if err := r.postJSON(ctx, r.collectionsURL(), body, &decoded); err != nil {
		return "", fmt.Errorf("get or create collection: %w", err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("chroma returned an empty collection id")
	}
	r.collectionID = decoded.ID
	return r.collectionID, nil
}

type upsertRequest struct {
	IDs        []string         `json:"ids"`
	Embeddings [][]float32      `json:"embeddings"`
	Documents  []string         `json:"documents"`
	Metadatas  []map[string]any `json:"metadatas"`
}

func (r *ChromaRepository) Upsert(
	ctx context.Context, documents []api.KnowledgeDocument,
) (int, error) {
	if len(documents) == 0 {
		return 0, nil
	}
	contents := make([]string, len(documents))
	for index, document := range documents {
		contents[index] = document.Content
	}
	vectors, err := r.embedder.Embed(ctx, contents)
	if err != nil {
		return 0, err
	}

	collectionID, err := r.resolveCollection(ctx)
	if err != nil {
		return 0, err
	}

	payload := upsertRequest{
		IDs:        make([]string, len(documents)),
		Embeddings: vectors,
		Documents:  contents,
		Metadatas:  make([]map[string]any, len(documents)),
	}
	for index, document := range documents {
		payload.IDs[index] = document.ID
		// This shape is what ChromaKnowledgeRepository.search reads back: it looks up
		// "title" and "source" by name and passes the rest through.
		metadata := map[string]any{"title": document.Title, "source": document.Source}
		for key, value := range document.Metadata {
			if key == "title" || key == "source" {
				continue
			}
			metadata[key] = value
		}
		payload.Metadatas[index] = metadata
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode upsert request: %w", err)
	}
	upsertURL := fmt.Sprintf("%s/%s/upsert", r.collectionsURL(), url.PathEscape(collectionID))
	if err := r.postJSON(ctx, upsertURL, body, nil); err != nil {
		return 0, fmt.Errorf("upsert documents: %w", err)
	}
	return len(documents), nil
}

func (r *ChromaRepository) postJSON(
	ctx context.Context, endpoint string, body []byte, out any,
) error {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("call chroma: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf(
			"chroma returned %d: %s", response.StatusCode, readSnippet(response.Body),
		)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode chroma response: %w", err)
	}
	return nil
}
