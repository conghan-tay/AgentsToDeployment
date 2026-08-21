from collections.abc import AsyncIterator, Iterator

import pytest
import pytest_asyncio
from app.core.models import FakeSupportModel
from app.core.schemas import KnowledgeDocument
from app.core.settings import Settings, get_settings
from app.graph.workflow import SupportNodes, build_support_graph
from app.knowledge.repository import MemoryKnowledgeRepository
from app.temporal.workflows import SUPPORT_GRAPH, SupportTicketWorkflow
from app.tools.registry import ToolRegistry, lookup_order
from temporalio.client import Client
from temporalio.contrib.langgraph import LangGraphPlugin
from temporalio.contrib.pydantic import pydantic_data_converter
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Worker

TASK_QUEUE = "support-agent-test"


@pytest.fixture
def task_queue() -> str:
    return TASK_QUEUE


@pytest.fixture(autouse=True)
def test_environment(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    monkeypatch.setenv("ENVIRONMENT", "test")
    monkeypatch.setenv("MODEL_PROVIDER", "fake")
    monkeypatch.delenv("MCP_SERVER_URL", raising=False)
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


@pytest.fixture
def settings() -> Settings:
    return Settings(model_provider="fake", mcp_server_url=None)


@pytest.fixture
def knowledge() -> MemoryKnowledgeRepository:
    repository = MemoryKnowledgeRepository()
    repository.seed(
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
    return repository


@pytest.fixture
def nodes(settings: Settings, knowledge: MemoryKnowledgeRepository) -> SupportNodes:
    return SupportNodes(
        settings=settings,
        model=FakeSupportModel(),
        knowledge=knowledge,
        tools=ToolRegistry([lookup_order]),
    )


@pytest_asyncio.fixture
async def temporal_env() -> AsyncIterator[WorkflowEnvironment]:
    """A time-skipping environment so the approval deadline resolves instantly."""

    async with await WorkflowEnvironment.start_time_skipping(
        data_converter=pydantic_data_converter
    ) as env:
        yield env


@pytest_asyncio.fixture
async def support_worker(
    temporal_env: WorkflowEnvironment, nodes: SupportNodes
) -> AsyncIterator[Client]:
    """Run the real worker wiring — same plugin and graph the deployed worker uses."""

    async with Worker(
        temporal_env.client,
        task_queue=TASK_QUEUE,
        workflows=[SupportTicketWorkflow],
        plugins=[LangGraphPlugin(graphs={SUPPORT_GRAPH: build_support_graph(nodes)})],
    ):
        yield temporal_env.client
