"""Shared Temporal client construction for the worker and the container healthcheck."""

from temporalio.client import Client
from temporalio.contrib.pydantic import pydantic_data_converter

from ..core.settings import Settings


async def connect(settings: Settings) -> Client:
    """Connect to Temporal with the payload converter the workflow contract depends on.

    The pydantic converter lets app.core.schemas models cross the workflow and activity
    boundaries directly. It encodes plain JSON, which is what the Go gateway's default
    converter produces and expects.
    """

    return await Client.connect(
        settings.temporal_address,
        namespace=settings.temporal_namespace,
        api_key=settings.temporal_api_key or None,
        tls=settings.temporal_tls or bool(settings.temporal_api_key),
        data_converter=pydantic_data_converter,
    )
