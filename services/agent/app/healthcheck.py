"""Container healthcheck: `python -m app.healthcheck`.

The worker serves no HTTP, so readiness means "this task queue has a live poller"
rather than "a port is open". Exits non-zero when Temporal is unreachable or nothing is
polling, which is what the orchestrator needs to know.
"""

import asyncio
import sys

from temporalio.api.taskqueue.v1 import TaskQueue
from temporalio.api.workflowservice.v1 import DescribeTaskQueueRequest
from temporalio.client import Client

from .core.settings import get_settings
from .temporal.client import connect


async def has_pollers(client: Client, task_queue: str) -> bool:
    described = await client.workflow_service.describe_task_queue(
        DescribeTaskQueueRequest(namespace=client.namespace, task_queue=TaskQueue(name=task_queue))
    )
    return bool(described.pollers)


async def main() -> int:
    settings = get_settings()
    try:
        client = await connect(settings)
        healthy = await has_pollers(client, settings.temporal_task_queue)
    except Exception as exc:  # noqa: BLE001 - the exit code is the whole contract here
        print(f"unhealthy: {exc}", file=sys.stderr)
        return 1
    if not healthy:
        print(
            f"unhealthy: no pollers on task queue {settings.temporal_task_queue}",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
