package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/support-agent/services/gateway/internal/api"
)

type stubEmbedder struct {
	inputs  []string
	vectors [][]float32
	err     error
}

func (s *stubEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	s.inputs = inputs
	if s.err != nil {
		return nil, s.err
	}
	if s.vectors != nil {
		return s.vectors, nil
	}
	vectors := make([][]float32, len(inputs))
	for index := range inputs {
		vectors[index] = []float32{float32(index), 0.5}
	}
	return vectors, nil
}

type recordedRequest struct {
	path string
	body map[string]any
}

// chromaStub stands in for Chroma, recording what the repository sends.
func chromaStub(t *testing.T, collectionID string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var recorded []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		recorded = append(recorded, recordedRequest{path: r.URL.Path, body: body})

		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/upsert") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + collectionID + `"}`))
	}))
	t.Cleanup(server.Close)
	return server, &recorded
}

func newRepository(baseURL string, embedder Embedder) *ChromaRepository {
	return NewChromaRepository(
		ChromaConfig{BaseURL: baseURL, Collection: "support_knowledge"},
		embedder,
		http.DefaultClient,
	)
}

func TestUpsertWritesTheShapeThePythonReaderExpects(t *testing.T) {
	server, recorded := chromaStub(t, "collection-1")
	embedder := &stubEmbedder{}
	repository := newRepository(server.URL, embedder)

	count, err := repository.Upsert(context.Background(), []api.KnowledgeDocument{{
		ID:       "refund-policy",
		Title:    "Refund policy",
		Content:  "Refunds require approval.",
		Source:   "refund-policy",
		Metadata: map[string]any{"team": "support"},
	}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	if len(*recorded) != 2 {
		t.Fatalf("requests = %d, want 2 (get-or-create then upsert)", len(*recorded))
	}

	create := (*recorded)[0]
	wantCreate := "/api/v2/tenants/default_tenant/databases/default_database/collections"
	if create.path != wantCreate {
		t.Fatalf("create path = %q, want %q", create.path, wantCreate)
	}
	if create.body["get_or_create"] != true {
		t.Fatalf("get_or_create = %v", create.body["get_or_create"])
	}
	// Distance metric is part of the contract with the Python reader.
	metadata, _ := create.body["metadata"].(map[string]any)
	if metadata["hnsw:space"] != "cosine" {
		t.Fatalf("hnsw:space = %v, want cosine", metadata["hnsw:space"])
	}

	upsert := (*recorded)[1]
	if !strings.HasSuffix(upsert.path, "/collections/collection-1/upsert") {
		t.Fatalf("upsert path = %q", upsert.path)
	}
	ids, _ := upsert.body["ids"].([]any)
	if len(ids) != 1 || ids[0] != "refund-policy" {
		t.Fatalf("ids = %v", ids)
	}
	documents, _ := upsert.body["documents"].([]any)
	if len(documents) != 1 || documents[0] != "Refunds require approval." {
		t.Fatalf("documents = %v", documents)
	}
	metadatas, _ := upsert.body["metadatas"].([]any)
	first, _ := metadatas[0].(map[string]any)
	// ChromaKnowledgeRepository.search reads title and source by name.
	if first["title"] != "Refund policy" || first["source"] != "refund-policy" {
		t.Fatalf("metadata = %v", first)
	}
	if first["team"] != "support" {
		t.Fatalf("custom metadata was dropped: %v", first)
	}
	if embedder.inputs[0] != "Refunds require approval." {
		t.Fatalf("embedded %q, want the document content", embedder.inputs[0])
	}
}

func TestUpsertDoesNotLetDocumentMetadataOverrideTitleOrSource(t *testing.T) {
	server, recorded := chromaStub(t, "collection-1")
	repository := newRepository(server.URL, &stubEmbedder{})

	_, err := repository.Upsert(context.Background(), []api.KnowledgeDocument{{
		ID:      "a",
		Title:   "Real title",
		Content: "body",
		Source:  "real-source",
		// A caller must not be able to desynchronise the fields the reader relies on.
		Metadata: map[string]any{"title": "spoofed", "source": "spoofed"},
	}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	metadatas, _ := (*recorded)[1].body["metadatas"].([]any)
	first, _ := metadatas[0].(map[string]any)
	if first["title"] != "Real title" || first["source"] != "real-source" {
		t.Fatalf("metadata = %v", first)
	}
}

func TestCollectionIsResolvedOnceAndReused(t *testing.T) {
	server, recorded := chromaStub(t, "collection-1")
	repository := newRepository(server.URL, &stubEmbedder{})
	document := []api.KnowledgeDocument{{ID: "a", Title: "A", Content: "b", Source: "a"}}

	for range 3 {
		if _, err := repository.Upsert(context.Background(), document); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	creates := 0
	for _, request := range *recorded {
		if strings.HasSuffix(request.path, "/collections") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("get-or-create calls = %d, want 1", creates)
	}
}

func TestFailedCollectionResolutionStaysRetryable(t *testing.T) {
	var failNext = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/collections") && failNext {
			failNext = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/upsert") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"collection-1"}`))
	}))
	defer server.Close()

	repository := newRepository(server.URL, &stubEmbedder{})
	document := []api.KnowledgeDocument{{ID: "a", Title: "A", Content: "b", Source: "a"}}

	if _, err := repository.Upsert(context.Background(), document); err == nil {
		t.Fatal("expected the first upsert to fail")
	}
	// A cold Chroma must not poison the gateway for the rest of its lifetime.
	if _, err := repository.Upsert(context.Background(), document); err != nil {
		t.Fatalf("second upsert should have succeeded: %v", err)
	}
}

func TestEmbeddingFailureStopsBeforeWriting(t *testing.T) {
	server, recorded := chromaStub(t, "collection-1")
	repository := newRepository(server.URL, &stubEmbedder{err: errors.New("rate limited")})

	_, err := repository.Upsert(context.Background(), []api.KnowledgeDocument{
		{ID: "a", Title: "A", Content: "b", Source: "a"},
	})

	if err == nil {
		t.Fatal("expected an error")
	}
	if len(*recorded) != 0 {
		t.Fatalf("chroma was called despite an embedding failure: %v", *recorded)
	}
}
