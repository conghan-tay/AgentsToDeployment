import asyncio
from collections import OrderedDict
from collections.abc import Coroutine
from functools import partial
from typing import Any

import structlog

logger = structlog.get_logger(__name__)

RUN_FAILED_MESSAGE = "run failed"


class BackgroundRuns:
    """Tracks graph executions started by the async API.

    Run *progress* is durable: every graph step is written to the checkpointer, so the
    streaming endpoint reads state from there. This registry only answers the two
    questions a checkpoint cannot: "has this run been accepted but not yet written its
    first checkpoint?" and "did the run die?".

    That state is per-process. With more than one agent replica, route a ticket's async
    create and its stream to the same replica, or move this bookkeeping into Redis or a
    runs table. Restarting the process cancels in-flight runs; their last checkpoint
    survives, so a future version can resume them on boot.
    """

    def __init__(self, max_errors: int = 256) -> None:
        self._tasks: dict[str, asyncio.Task[Any]] = {}
        self._errors: OrderedDict[str, str] = OrderedDict()
        self._max_errors = max_errors

    def start(self, ticket_id: str, coroutine: Coroutine[Any, Any, Any]) -> None:
        # The registry holds the only strong reference, so the task cannot be collected
        # mid-run. The done callback removes it again.
        task = asyncio.create_task(coroutine, name=f"run:{ticket_id}")
        self._tasks[ticket_id] = task
        task.add_done_callback(partial(self._finish, ticket_id))

    def is_active(self, ticket_id: str) -> bool:
        return ticket_id in self._tasks

    def error(self, ticket_id: str) -> str | None:
        return self._errors.get(ticket_id)

    async def aclose(self) -> None:
        tasks = list(self._tasks.values())
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self._tasks.clear()

    def _finish(self, ticket_id: str, task: asyncio.Task[Any]) -> None:
        self._tasks.pop(ticket_id, None)
        if task.cancelled():
            return
        failure = task.exception()
        if failure is None:
            return
        logger.error("support_run_failed", ticket_id=ticket_id, exc_info=failure)
        # Clients get a generic message; the exception stays in the logs.
        self._errors[ticket_id] = RUN_FAILED_MESSAGE
        while len(self._errors) > self._max_errors:
            self._errors.popitem(last=False)
