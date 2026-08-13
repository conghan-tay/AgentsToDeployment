import pytest
from app.core.models import FakeSupportModel
from app.core.schemas import KnowledgeDocument
from app.core.settings import Settings
from app.graph.workflow import build_graph
from app.knowledge.repository import MemoryKnowledgeRepository
from app.tools.registry import ToolRegistry, lookup_order
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command


async def make_graph():
    settings = Settings(
        model_provider="fake",
        checkpointer_backend="memory",
        knowledge_backend="memory",
        mcp_server_url=None,
    )
    knowledge = MemoryKnowledgeRepository()
    await knowledge.upsert(
        [
            KnowledgeDocument(
                id="refund-policy",
                title="Refund policy",
                content="Refunds need approval within 30 days.",
                source="refund-policy",
            ),
            KnowledgeDocument(
                id="shipping-policy",
                title="Shipping policy",
                content="Shipping takes 3 to 5 business days.",
                source="shipping-policy",
            ),
        ]
    )
    return build_graph(
        settings=settings,
        model=FakeSupportModel(),
        knowledge=knowledge,
        tools=ToolRegistry([lookup_order]),
        checkpointer=InMemorySaver(),
    )


@pytest.mark.asyncio
async def test_read_only_order_lookup_completes_without_approval() -> None:
    graph = await make_graph()
    config = {"configurable": {"thread_id": "order-ticket"}}

    result = await graph.ainvoke(
        {
            "ticket_id": "order-ticket",
            "customer_id": "customer-1",
            "message": "Where is my order?",
            "order_id": "order-123",
        },
        config,
    )

    assert result["status"] == "completed"
    assert result["tool_results"][0]["name"] == "lookup_order"
    assert not result.get("__interrupt__")


@pytest.mark.asyncio
async def test_refund_pauses_then_resumes_after_approval() -> None:
    graph = await make_graph()
    config = {"configurable": {"thread_id": "refund-ticket"}}

    paused = await graph.ainvoke(
        {
            "ticket_id": "refund-ticket",
            "customer_id": "customer-1",
            "message": "Please refund my order",
            "order_id": "order-123",
        },
        config,
    )

    assert paused["__interrupt__"][0].value["action"] == "refund"

    completed = await graph.ainvoke(
        Command(resume={"decision": "approve", "reviewer": "manager-1"}), config
    )

    assert completed["status"] == "completed"
    assert completed["action_result"]["name"] == "refund"
    assert "Approved" in completed["final_answer"]
