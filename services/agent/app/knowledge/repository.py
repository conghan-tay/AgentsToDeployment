import asyncio
import math
import re
from abc import ABC, abstractmethod
from typing import Any

import chromadb
from langchain_openai import OpenAIEmbeddings

from ..core.schemas import KnowledgeDocument
from ..core.settings import Settings


class KnowledgeRepository(ABC):
    @abstractmethod
    async def upsert(self, documents: list[KnowledgeDocument]) -> int: ...

    @abstractmethod
    async def search(self, query: str, limit: int = 4) -> list[dict[str, Any]]: ...


class MemoryKnowledgeRepository(KnowledgeRepository):
    """Lexical in-memory repository for unit tests; it implements the production contract."""

    def __init__(self) -> None:
        self._documents: dict[str, KnowledgeDocument] = {}

    async def upsert(self, documents: list[KnowledgeDocument]) -> int:
        self._documents.update({document.id: document for document in documents})
        return len(documents)

    async def search(self, query: str, limit: int = 4) -> list[dict[str, Any]]:
        terms = set(re.findall(r"\w+", query.lower()))

        def score(document: KnowledgeDocument) -> float:
            text_terms = set(re.findall(r"\w+", f"{document.title} {document.content}".lower()))
            return len(terms & text_terms) / math.sqrt(max(len(text_terms), 1))

        ranked = sorted(self._documents.values(), key=score, reverse=True)
        return [document.model_dump() for document in ranked[:limit] if score(document) > 0]


class ChromaKnowledgeRepository(KnowledgeRepository):
    """Chroma-backed retrieval with explicit OpenAI embeddings.

    Keeping embeddings outside Chroma makes the vector model visible and independently
    configurable. Replace this class to move to pgvector without touching the graph.
    """

    def __init__(self, settings: Settings) -> None:
        client = chromadb.HttpClient(
            host=settings.chroma_host, port=settings.chroma_port, ssl=settings.chroma_ssl
        )
        self._collection = client.get_or_create_collection(
            name=settings.chroma_collection, metadata={"hnsw:space": "cosine"}
        )
        self._embeddings = OpenAIEmbeddings(model="text-embedding-3-small")

    async def upsert(self, documents: list[KnowledgeDocument]) -> int:
        vectors = await self._embeddings.aembed_documents(
            [document.content for document in documents]
        )
        await asyncio.to_thread(
            self._collection.upsert,
            ids=[document.id for document in documents],
            documents=[document.content for document in documents],
            embeddings=vectors,
            metadatas=[
                {"title": document.title, "source": document.source, **document.metadata}
                for document in documents
            ],
        )
        return len(documents)

    async def search(self, query: str, limit: int = 4) -> list[dict[str, Any]]:
        vector = await self._embeddings.aembed_query(query)
        result = await asyncio.to_thread(
            self._collection.query, query_embeddings=[vector], n_results=limit
        )
        documents = result.get("documents", [[]])[0]
        metadata = result.get("metadatas", [[]])[0]
        ids = result.get("ids", [[]])[0]
        return [
            {
                "id": document_id,
                "content": content,
                "source": meta.get("source", "unknown"),
                "title": meta.get("title", document_id),
                "metadata": meta,
            }
            for document_id, content, meta in zip(ids, documents, metadata, strict=True)
        ]


def build_knowledge_repository(settings: Settings) -> KnowledgeRepository:
    if settings.knowledge_backend == "memory":
        return MemoryKnowledgeRepository()
    return ChromaKnowledgeRepository(settings)
