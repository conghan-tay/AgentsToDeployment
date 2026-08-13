from app.main import create_app
from fastapi.testclient import TestClient


def test_ticket_and_approval_lifecycle() -> None:
    with TestClient(create_app()) as client:
        headers = {"X-Internal-API-Key": "test-internal-key"}
        seed = client.post(
            "/internal/v1/knowledge",
            headers=headers,
            json={
                "documents": [
                    {
                        "id": "refund-policy",
                        "title": "Refund policy",
                        "content": "Refunds require approval.",
                        "source": "refund-policy",
                    }
                ]
            },
        )
        assert seed.status_code == 200

        created = client.post(
            "/internal/v1/runs",
            headers=headers,
            json={
                "customer_id": "customer-1",
                "message": "I want a refund",
                "order_id": "order-1",
            },
        )
        assert created.status_code == 200
        assert created.json()["status"] == "waiting_approval"
        ticket_id = created.json()["ticket_id"]

        approved = client.post(
            f"/internal/v1/runs/{ticket_id}/decision",
            headers=headers,
            json={"decision": "approve", "reviewer": "manager"},
        )
        assert approved.status_code == 200
        assert approved.json()["status"] == "completed"


def test_internal_auth_is_required() -> None:
    with TestClient(create_app()) as client:
        response = client.post("/internal/v1/runs", json={"customer_id": "c-1", "message": "hello"})
        assert response.status_code == 401
