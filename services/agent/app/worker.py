"""Temporal worker entrypoint: `python -m app.worker`.

This process has no HTTP server. It polls a Temporal task queue and executes the
LangGraph nodes registered below as activities. The public API lives entirely in the
Go gateway.
"""

import asyncio
import contextlib
import signal

import structlog
from temporalio.contrib.langgraph import LangGraphPlugin
from temporalio.worker import Worker

from .core.logging import configure_logging
from .core.models import build_model
from .core.settings import get_settings
from .graph.workflow import SupportNodes, build_support_graph
from .knowledge.repository import ChromaKnowledgeRepository
from .temporal.client import connect
from .temporal.workflows import SUPPORT_GRAPH, SupportTicketWorkflow
from .tools.registry import ToolRegistry

logger = structlog.get_logger(__name__)


async def main() -> None:
    settings = get_settings()
    configure_logging(settings.log_level)

    # Built once, here, so every node keeps a live reference to the model, retrieval,
    # and tool adapters. Activities run in this same process, so no dependency has to
    # cross a serialization boundary.
    nodes = SupportNodes(
        settings=settings,
        model=build_model(settings),
        knowledge=ChromaKnowledgeRepository(settings),
        tools=await ToolRegistry.create(settings),
    )

    client = await connect(settings)
    worker = Worker(
        client,
        task_queue=settings.temporal_task_queue,
        workflows=[SupportTicketWorkflow],
        plugins=[LangGraphPlugin(graphs={SUPPORT_GRAPH: build_support_graph(nodes)})],
    )

    logger.info(
        "worker_starting",
        address=settings.temporal_address,
        namespace=settings.temporal_namespace,
        task_queue=settings.temporal_task_queue,
    )

    # Drain in-flight tasks on SIGTERM so a deploy does not abandon running tickets.
    shutdown = asyncio.Event()
    loop = asyncio.get_running_loop()
    for signal_name in (signal.SIGINT, signal.SIGTERM):
        with contextlib.suppress(NotImplementedError):
            loop.add_signal_handler(signal_name, shutdown.set)

    async with worker:
        await shutdown.wait()
    logger.info("worker_stopped")


if __name__ == "__main__":
    asyncio.run(main())
