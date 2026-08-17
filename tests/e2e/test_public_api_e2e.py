import json
import os
from typing import Any

import httpx
import pytest

pytestmark = [
    pytest.mark.e2e,
    pytest.mark.skipif(os.getenv("RUN_E2E") != "1", reason="set RUN_E2E=1 for stack tests"),
]

BASE_URL = os.getenv("API_BASE_URL", "http://localhost:8080")
HEADERS = {"X-API-Key": os.getenv("API_KEY", "local-api-key")}


def assert_success(response: httpx.Response) -> None:
    """Surface the most common local E2E configuration error without a long traceback."""

    if response.status_code == httpx.codes.UNAUTHORIZED:
        pytest.fail(
            "E2E request was unauthorized. Ensure API_KEY used by pytest matches the "
            "API_KEY used to start the gateway; `make test-e2e` loads both from .env.",
            pytrace=False,
        )
    response.raise_for_status()


def read_until_result(response: httpx.Response) -> dict[str, Any]:
    """Consume server-sent events until the terminal frame arrives.

    A `result` event ends the run; an `error` or `timeout` event is a test failure.
    """

    event = "message"
    for line in response.iter_lines():
        if line.startswith("event: "):
            event = line.removeprefix("event: ")
        elif line.startswith("data: "):
            data = line.removeprefix("data: ")
            if event == "result":
                return json.loads(data)
            if event in {"error", "timeout"}:
                pytest.fail(f"stream ended with {event}: {data}", pytrace=False)
    pytest.fail("stream closed without a result event", pytrace=False)


def test_public_async_ticket_streams_until_completion() -> None:
    with httpx.Client(base_url=BASE_URL, headers=HEADERS, timeout=60) as client:
        created = client.post(
            "/v1/tickets/async",
            json={
                "customer_id": "e2e-async-customer",
                "message": "Where is my order?",
                "order_id": "e2e-async-1",
            },
        )
        assert_success(created)
        assert created.status_code == httpx.codes.ACCEPTED
        payload = created.json()
        assert payload["status"] == "running"
        ticket_id = payload["ticket_id"]

        with client.stream("GET", f"/v1/tickets/{ticket_id}/stream") as stream:
            assert_success(stream)
            assert stream.headers["content-type"].startswith("text/event-stream")
            # Status ticks are not asserted on: a fast run can complete before the first poll.
            result = read_until_result(stream)

        assert result["ticket_id"] == ticket_id
        assert result["status"] == "completed"
        assert result["answer"]


def test_public_refund_approval_lifecycle() -> None:
    with httpx.Client(base_url=BASE_URL, headers=HEADERS, timeout=30) as client:
        seeded = client.post(
            "/v1/knowledge",
            json={
                "documents": [
                    {
                        "id": "refund-policy",
                        "title": "Refund policy",
                        "content": "Refunds require a human approval within 30 days.",
                        "source": "refund-policy",
                    }
                ]
            },
        )
        assert_success(seeded)

        created = client.post(
            "/v1/tickets",
            json={
                "customer_id": "e2e-customer",
                "message": "Please refund order e2e-123",
                "order_id": "e2e-123",
            },
        )
        assert_success(created)
        payload = created.json()
        assert payload["status"] == "waiting_approval"
        assert payload["pending_action"]["action"] == "refund"

        approved = client.post(
            f"/v1/tickets/{payload['ticket_id']}/decision",
            json={"decision": "approve", "reviewer": "e2e-reviewer"},
        )
        assert_success(approved)
        assert approved.json()["status"] == "completed"
