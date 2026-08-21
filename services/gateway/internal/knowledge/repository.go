// Package knowledge owns knowledge-base ingestion.
//
// Writing knowledge is plain CRUD — embed, then upsert — so it runs directly in the
// gateway rather than through a Temporal workflow. The Python worker only reads; see
// services/agent/app/knowledge/repository.py for the matching search side.
package knowledge

import (
	"context"

	"github.com/example/support-agent/services/gateway/internal/api"
)

// Repository is the write side of the knowledge base. It mirrors the Python
// KnowledgeRepository seam so handlers stay testable without Chroma or OpenAI.
type Repository interface {
	Upsert(ctx context.Context, documents []api.KnowledgeDocument) (int, error)
}

// Embedder turns document text into vectors.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}
