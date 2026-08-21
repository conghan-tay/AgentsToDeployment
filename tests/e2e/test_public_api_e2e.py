import os
import time

import httpx
import pytest

pytestmark = [
    pytest.mark.e2e,
    pytest.mark.skipif(os.getenv("RUN_E2E") != "1", reason="set RUN_E2E=1 for stack tests"),
]

BASE_URL = os.getenv("API_BASE_URL", "http://localhost:8080")
HEADERS = {"X-API-Key": os.getenv("API_KEY", "local-api-key")}
POLL_TIMEOUT_SECONDS = 90


def assert_success(response: httpx.Response) -> None:
    """Surface the most common local E2E configuration error without a long traceback."""

    if response.status_code == httpx.codes.UNAUTHORIZED:
        pytest.fail(
            "E2E request was unauthorized. Ensure API_KEY used by pytest matches the "
            "API_KEY used to start the gateway; `make test-e2e` loads both from .env.",
            pytrace=False,
        )
    response.raise_for_status()


def poll_until(
    client: httpx.Client, ticket_id: str, status: str, timeout: int = POLL_TIMEOUT_SECONDS
) -> dict:
    """Poll a ticket until it reaches `status`.

    Runs are durable and asynchronous, so the public API reports progress rather than
    blocking. This is exactly what a real client would do.
    """

    deadline = time.monotonic() + timeout
    last: dict = {}
    while time.monotonic() < deadline:
        response = client.get(f"/v1/tickets/{ticket_id}")
        assert_success(response)
        last = response.json()
        if last["status"] == status:
            return last
        if last["status"] in {"completed", "rejected"} and last["status"] != status:
            pytest.fail(f"ticket reached terminal status {last['status']}, wanted {status}")
        time.sleep(1)
    pytest.fail(f"ticket {ticket_id} never reached {status}; last state was {last}")


def test_public_refund_approval_lifecycle() -> None:
    with httpx.Client(base_url=BASE_URL, headers=HEADERS, timeout=30) as client:
        # Written by the gateway straight to Chroma: embed, then upsert.
        #
        # The content deliberately mentions shipping while the ticket below does not.
        # The deterministic model picks its shipping draft only when the word appears
        # in the prompt, and for a refund ticket the prompt's only other source is the
        # retrieved knowledge. A shipping citation in the final answer is therefore
        # proof that the worker read back what the gateway wrote — the one part of the
        # cross-language Chroma contract that unit tests cannot cover.
        seeded = client.post(
            "/v1/knowledge",
            json={
                "documents": [
                    {
                        "id": "refund-policy",
                        "title": "Refund policy",
                        "content": (
                            "Refunds require a human approval within 30 days. "
                            "Shipping delays are the most common reason."
                        ),
                        "source": "refund-policy",
                    }
                ]
            },
        )
        assert_success(seeded)
        assert seeded.json()["upserted"] == 1

        created = client.post(
            "/v1/tickets",
            json={
                "customer_id": "e2e-customer",
                "message": "Please refund order e2e-123",
                "order_id": "e2e-123",
            },
        )
        assert_success(created)
        assert created.status_code == httpx.codes.ACCEPTED
        payload = created.json()
        assert payload["status"] == "running"
        ticket_id = payload["ticket_id"]

        paused = poll_until(client, ticket_id, "waiting_approval")
        assert paused["pending_action"]["action"] == "refund"

        approved = client.post(
            f"/v1/tickets/{ticket_id}/decision",
            json={"decision": "approve", "reviewer": "e2e-reviewer"},
        )
        assert_success(approved)
        assert approved.status_code == httpx.codes.ACCEPTED

        completed = poll_until(client, ticket_id, "completed")
        assert completed["answer"].startswith("Approved and submitted.")
        # Reached only when retrieval returned the seeded document, so this asserts the
        # gateway's write path and the worker's read path agree on the collection and
        # the embedding model.
        assert completed["citations"], "the answer cited nothing, so retrieval returned nothing"
        assert "shipping-policy" in completed["citations"]


def test_decision_on_a_ticket_that_is_not_awaiting_review_conflicts() -> None:
    with httpx.Client(base_url=BASE_URL, headers=HEADERS, timeout=30) as client:
        created = client.post(
            "/v1/tickets",
            json={
                "customer_id": "e2e-customer",
                "message": "Where is my order?",
                "order_id": "e2e-123",
            },
        )
        assert_success(created)
        ticket_id = created.json()["ticket_id"]
        poll_until(client, ticket_id, "completed")

        response = client.post(
            f"/v1/tickets/{ticket_id}/decision",
            json={"decision": "approve", "reviewer": "e2e-reviewer"},
        )

        assert response.status_code == httpx.codes.CONFLICT


def test_unknown_ticket_is_not_found() -> None:
    with httpx.Client(base_url=BASE_URL, headers=HEADERS, timeout=30) as client:
        response = client.get("/v1/tickets/ticket-does-not-exist")

        assert response.status_code == httpx.codes.NOT_FOUND


def test_invalid_request_is_rejected_at_the_edge() -> None:
    """The gateway validates; no workflow should be started for a malformed ticket."""

    with httpx.Client(base_url=BASE_URL, headers=HEADERS, timeout=30) as client:
        response = client.post("/v1/tickets", json={"message": "no customer id"})

        assert response.status_code == httpx.codes.BAD_REQUEST
        assert "customer_id" in response.json()["error"]
