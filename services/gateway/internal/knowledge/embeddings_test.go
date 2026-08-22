package knowledge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedSendsModelAndInputs(t *testing.T) {
	var captured map[string]any
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`,
		))
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(server.URL, "sk-test", "text-embedding-3-small", http.DefaultClient)
	vectors, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}

	if authorization != "Bearer sk-test" {
		t.Fatalf("authorization = %q", authorization)
	}
	if captured["model"] != "text-embedding-3-small" {
		t.Fatalf("model = %v", captured["model"])
	}
	if len(vectors) != 2 || vectors[0][0] != 0.1 || vectors[1][1] != 0.4 {
		t.Fatalf("vectors = %v", vectors)
	}
}

func TestEmbedReordersByIndex(t *testing.T) {
	// The API documents index ordering but does not promise it, and a misaligned vector
	// would silently attach the wrong meaning to a document.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"data":[{"index":1,"embedding":[9.0]},{"index":0,"embedding":[1.0]}]}`,
		))
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(server.URL, "sk-test", "m", http.DefaultClient)
	vectors, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}

	if vectors[0][0] != 1.0 || vectors[1][0] != 9.0 {
		t.Fatalf("vectors were not reordered by index: %v", vectors)
	}
}

func TestEmbedRejectsAMismatchedVectorCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1.0]}]}`))
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(server.URL, "sk-test", "m", http.DefaultClient)
	if _, err := embedder.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected an error when the vector count does not match the input count")
	}
}

func TestEmbedSurfacesAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(server.URL, "sk-test", "m", http.DefaultClient)
	_, err := embedder.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestEmbedWithNoInputsMakesNoRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(server.URL, "sk-test", "m", http.DefaultClient)
	vectors, err := embedder.Embed(context.Background(), nil)
	if err != nil || vectors != nil {
		t.Fatalf("vectors = %v, err = %v", vectors, err)
	}
	if called {
		t.Fatal("an empty batch should not reach the API")
	}
}
