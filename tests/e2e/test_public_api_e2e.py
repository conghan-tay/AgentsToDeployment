import os

import httpx
import pytest

pytestmark = [
    pytest.mark.e2e,
    pytest.mark.skipif(os.getenv("RUN_E2E") != "1", reason="set RUN_E2E=1 for stack tests"),
]

BASE_URL = os.getenv("API_BASE_URL", "http://localhost:8080")
HEADERS = {"X-API-Key": os.getenv("API_KEY", "local-api-key")}


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
        seeded.raise_for_status()

        created = client.post(
            "/v1/tickets",
            json={
                "customer_id": "e2e-customer",
                "message": "Please refund order e2e-123",
                "order_id": "e2e-123",
            },
        )
        created.raise_for_status()
        payload = created.json()
        assert payload["status"] == "waiting_approval"
        assert payload["pending_action"]["action"] == "refund"

        approved = client.post(
            f"/v1/tickets/{payload['ticket_id']}/decision",
            json={"decision": "approve", "reviewer": "e2e-reviewer"},
        )
        approved.raise_for_status()
        assert approved.json()["status"] == "completed"
