import asyncio
import json
import re
import time
from collections.abc import AsyncIterator
from typing import Any
from uuid import uuid4

import structlog
from fastapi import APIRouter, Depends, Header, HTTPException, Request, status
from fastapi.responses import StreamingResponse
from langgraph.types import Command

from ..core.runs import BackgroundRuns
from ..core.schemas import (
    ApprovalDecision,
    HealthResponse,
    KnowledgeUpsertRequest,
    KnowledgeUpsertResponse,
    PendingAction,
    RunResponse,
    RunStatus,
    TicketRequest,
)
from ..core.settings import Settings, get_settings

logger = structlog.get_logger(__name__)

router = APIRouter()


def require_internal_key(
    x_internal_api_key: str = Header(default=""), settings: Settings = Depends(get_settings)
) -> None:
    if x_internal_api_key != settings.internal_api_key:
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="invalid internal key")


def _config(ticket_id: str) -> dict[str, Any]:
    return {"configurable": {"thread_id": ticket_id}}


def _graph_input(ticket_id: str, payload: TicketRequest) -> dict[str, Any]:
    return {
        "ticket_id": ticket_id,
        "customer_id": payload.customer_id,
        "message": payload.message,
        "order_id": payload.order_id,
        "metadata": payload.metadata,
    }


def _sse(event: str, data: str) -> str:
    return f"event: {event}\ndata: {data}\n\n"


def _pending_from_result(result: dict[str, Any]) -> PendingAction | None:
    interrupts = result.get("__interrupt__", ())
    if not interrupts:
        return None
    value = interrupts[0].value
    return PendingAction.model_validate(value)


def _citations(answer: str | None) -> list[str]:
    return list(dict.fromkeys(re.findall(r"\[([^\]]+)\]", answer or "")))


def _response(ticket_id: str, result: dict[str, Any]) -> RunResponse:
    pending = _pending_from_result(result)
    answer = result.get("final_answer")
    classification = result.get("classification")
    run_status = (
        RunStatus.WAITING_APPROVAL if pending else RunStatus(result.get("status", "completed"))
    )
    return RunResponse(
        ticket_id=ticket_id,
        status=run_status,
        answer=answer,
        category=classification.get("category") if classification else None,
        pending_action=pending,
        citations=_citations(answer or result.get("draft")),
    )


@router.get("/healthz", response_model=HealthResponse)
async def health() -> HealthResponse:
    return HealthResponse(status="ok")


@router.post(
    "/internal/v1/runs",
    response_model=RunResponse,
    dependencies=[Depends(require_internal_key)],
)
async def create_run(payload: TicketRequest, request: Request) -> RunResponse:
    started = time.perf_counter()
    request_id = request.headers.get("X-Request-ID", "unknown")
    ticket_id = str(uuid4())
    result = await request.app.state.graph.ainvoke(
        _graph_input(ticket_id, payload), _config(ticket_id)
    )
    response = _response(ticket_id, result)
    logger.info(
        "support_run_completed",
        request_id=request_id,
        ticket_id=ticket_id,
        status=response.status,
        category=response.category,
        duration_ms=round((time.perf_counter() - started) * 1000),
    )

    return response


@router.post(
    "/internal/v1/runs/async",
    response_model=RunResponse,
    status_code=status.HTTP_202_ACCEPTED,
    dependencies=[Depends(require_internal_key)],
)
async def create_run_async(payload: TicketRequest, request: Request) -> RunResponse:
    """Accept a ticket and run the graph in the background.

    The caller gets a ticket id immediately and follows the run on the streaming
    endpoint, so request duration stops being tied to workflow duration.
    """

    request_id = request.headers.get("X-Request-ID", "unknown")
    ticket_id = str(uuid4())
    runs: BackgroundRuns = request.app.state.runs
    runs.start(
        ticket_id,
        request.app.state.graph.ainvoke(_graph_input(ticket_id, payload), _config(ticket_id)),
    )
    logger.info("support_run_accepted", request_id=request_id, ticket_id=ticket_id)
    return RunResponse(ticket_id=ticket_id, status=RunStatus.RUNNING)


@router.get(
    "/internal/v1/runs/{ticket_id}",
    response_model=RunResponse,
    dependencies=[Depends(require_internal_key)],
)
async def get_run(ticket_id: str, request: Request) -> RunResponse:
    snapshot = await request.app.state.graph.aget_state(_config(ticket_id))
    if not snapshot.values:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="ticket not found")
    values = dict(snapshot.values)
    if snapshot.interrupts:
        values["__interrupt__"] = snapshot.interrupts
    return _response(ticket_id, values)


async def _run_events(request: Request, ticket_id: str, settings: Settings) -> AsyncIterator[str]:
    """Emit one server-sent event per poll until the run ends or the stream is capped.

    State comes from the checkpointer rather than from the graph call, so the stream can
    be opened, dropped, and reopened without affecting the run itself.
    """

    graph = request.app.state.graph
    runs: BackgroundRuns = request.app.state.runs
    deadline = time.monotonic() + settings.stream_timeout_seconds
    while True:
        if await request.is_disconnected():
            return
        failure = runs.error(ticket_id)
        if failure is not None:
            yield _sse("error", json.dumps({"ticket_id": ticket_id, "error": failure}))
            return
        snapshot = await graph.aget_state(_config(ticket_id))
        values = dict(snapshot.values)
        if snapshot.interrupts:
            values["__interrupt__"] = snapshot.interrupts
            yield _sse("status", "waiting for approval...")
        elif snapshot.next or "status" not in values:
            # Nodes are still queued, or the run was just accepted: a new thread briefly
            # holds its input with no scheduled task yet, which is not a finished run.
            yield _sse("status", "running...")
        else:
            # Only a terminal node writes `status`, so this is a completed or rejected run.
            yield _sse("result", _response(ticket_id, values).model_dump_json())
            return
        if time.monotonic() >= deadline:
            yield _sse("timeout", "stream closed; reconnect to keep watching this ticket")
            return
        await asyncio.sleep(settings.stream_poll_seconds)


@router.get(
    "/internal/v1/runs/{ticket_id}/stream",
    dependencies=[Depends(require_internal_key)],
)
async def get_run_streaming(
    ticket_id: str, request: Request, settings: Settings = Depends(get_settings)
) -> StreamingResponse:
    snapshot = await request.app.state.graph.aget_state(_config(ticket_id))
    # An unknown ticket must fail before the body starts; a just-accepted run is known to
    # the registry even though it has not written its first checkpoint yet.
    if not snapshot.values and not request.app.state.runs.is_active(ticket_id):
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="ticket not found")
    return StreamingResponse(
        _run_events(request, ticket_id, settings),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


@router.post(
    "/internal/v1/runs/{ticket_id}/decision",
    response_model=RunResponse,
    dependencies=[Depends(require_internal_key)],
)
async def decide_run(ticket_id: str, payload: ApprovalDecision, request: Request) -> RunResponse:
    snapshot = await request.app.state.graph.aget_state(_config(ticket_id))
    if not snapshot.values:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="ticket not found")
    if not snapshot.interrupts:
        raise HTTPException(
            status_code=status.HTTP_409_CONFLICT, detail="ticket is not awaiting review"
        )
    result = await request.app.state.graph.ainvoke(
        Command(resume=payload.model_dump()), _config(ticket_id)
    )
    return _response(ticket_id, result)


@router.post(
    "/internal/v1/knowledge",
    response_model=KnowledgeUpsertResponse,
    dependencies=[Depends(require_internal_key)],
)
async def upsert_knowledge(
    payload: KnowledgeUpsertRequest, request: Request
) -> KnowledgeUpsertResponse:
    count = await request.app.state.knowledge.upsert(payload.documents)
    return KnowledgeUpsertResponse(upserted=count)
