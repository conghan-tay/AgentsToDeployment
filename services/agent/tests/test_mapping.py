from app.core.schemas import PendingAction, RunStatus
from app.temporal.mapping import citations, to_run_state


def test_citations_are_unique_and_ordered() -> None:
    assert citations("See [refund-policy] and [shipping-policy] and [refund-policy].") == [
        "refund-policy",
        "shipping-policy",
    ]


def test_citations_tolerate_a_missing_answer() -> None:
    assert citations(None) == []


def test_completed_state_reports_category_and_citations() -> None:
    state = to_run_state(
        ticket_id="ticket-1",
        values={
            "status": "completed",
            "final_answer": "Your order ships soon. [shipping-policy]",
            "classification": {"category": "order_status"},
        },
    )

    assert state.status is RunStatus.COMPLETED
    assert state.category == "order_status"
    assert state.citations == ["shipping-policy"]
    assert state.pending_action is None


def test_pending_action_forces_waiting_approval() -> None:
    """The interrupt is not part of graph state, so the caller supplies it."""

    state = to_run_state(
        ticket_id="ticket-2",
        values={"draft": "A refund is proposed. [refund-policy]"},
        pending_action=PendingAction(action="refund", arguments={}, reason="requested"),
    )

    assert state.status is RunStatus.WAITING_APPROVAL
    # While paused there is no final answer yet, so citations come from the draft.
    assert state.citations == ["refund-policy"]


def test_rejected_status_is_preserved() -> None:
    state = to_run_state(
        ticket_id="ticket-3",
        values={"status": "rejected", "final_answer": "The proposed refund was not approved."},
    )

    assert state.status is RunStatus.REJECTED
