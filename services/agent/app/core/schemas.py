from datetime import UTC, datetime
from enum import StrEnum
from typing import Any, Literal

from pydantic import BaseModel, Field


class RunStatus(StrEnum):
    RUNNING = "running"
    COMPLETED = "completed"
    WAITING_APPROVAL = "waiting_approval"
    REJECTED = "rejected"
    FAILED = "failed"


class TicketRequest(BaseModel):
    customer_id: str = Field(min_length=1, max_length=100)
    message: str = Field(min_length=1, max_length=8_000)
    order_id: str | None = Field(default=None, max_length=100)
    channel: Literal["api", "email", "chat"] = "api"
    metadata: dict[str, Any] = Field(default_factory=dict)


class ApprovalDecision(BaseModel):
    decision: Literal["approve", "reject"]
    reviewer: str = Field(min_length=1, max_length=100)
    comment: str | None = Field(default=None, max_length=1_000)


class PendingAction(BaseModel):
    action: str
    arguments: dict[str, Any]
    reason: str


class RunResponse(BaseModel):
    ticket_id: str
    status: RunStatus
    answer: str | None = None
    category: str | None = None
    pending_action: PendingAction | None = None
    citations: list[str] = Field(default_factory=list)
    created_at: datetime = Field(default_factory=lambda: datetime.now(UTC))


class KnowledgeDocument(BaseModel):
    id: str = Field(min_length=1, max_length=200)
    title: str = Field(min_length=1, max_length=300)
    content: str = Field(min_length=1, max_length=50_000)
    source: str = Field(min_length=1, max_length=500)
    metadata: dict[str, Any] = Field(default_factory=dict)


class KnowledgeUpsertRequest(BaseModel):
    documents: list[KnowledgeDocument] = Field(min_length=1, max_length=100)


class KnowledgeUpsertResponse(BaseModel):
    upserted: int


class HealthResponse(BaseModel):
    status: Literal["ok", "degraded"]
    service: str = "agent"
