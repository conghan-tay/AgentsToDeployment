import asyncio

import pytest
from app.core.schemas import ApprovalDecision, RunStatus, TicketRequest
from app.temporal.workflows import SupportTicketWorkflow
from temporalio.client import Client, WorkflowHandle


async def _start(
    client: Client,
    task_queue: str,
    ticket_id: str,
    message: str,
    approval_timeout_hours: int = 0,
) -> WorkflowHandle:
    """Start a ticket.

    The deadline defaults to 0 (wait forever) so that the time-skipping server cannot
    fast-forward past it while a test is still arranging its signal. Only the deadline
    test opts into a real timeout.
    """

    return await client.start_workflow(
        SupportTicketWorkflow.run,
        args=[
            TicketRequest(customer_id="customer-1", message=message, order_id="order-123"),
            approval_timeout_hours,
        ],
        id=ticket_id,
        task_queue=task_queue,
    )


async def _wait_for_pause(handle: WorkflowHandle) -> None:
    """Poll the query until the run parks at the approval interrupt."""

    for _ in range(100):
        state = await handle.query(SupportTicketWorkflow.get_state)
        if state.status is RunStatus.WAITING_APPROVAL:
            return
        await asyncio.sleep(0.1)
    pytest.fail("workflow never reached waiting_approval")


@pytest.mark.asyncio
async def test_read_only_order_lookup_completes_without_approval(
    support_worker: Client, task_queue: str
) -> None:
    handle = await _start(support_worker, task_queue, "order-ticket", "Where is my order?")

    state = await handle.result()

    assert state.status is RunStatus.COMPLETED
    assert state.pending_action is None
    assert state.category == "order_status"
    assert state.citations == ["shipping-policy"]


@pytest.mark.asyncio
async def test_refund_pauses_then_resumes_after_approval(
    support_worker: Client, task_queue: str
) -> None:
    handle = await _start(support_worker, task_queue, "refund-ticket", "Please refund my order")
    await _wait_for_pause(handle)

    paused = await handle.query(SupportTicketWorkflow.get_state)
    assert paused.pending_action is not None
    assert paused.pending_action.action == "refund"
    assert paused.pending_action.arguments["customer_id"] == "customer-1"

    await handle.signal(
        SupportTicketWorkflow.submit_decision,
        ApprovalDecision(decision="approve", reviewer="manager-1"),
    )
    state = await handle.result()

    assert state.status is RunStatus.COMPLETED
    assert state.answer is not None
    assert state.answer.startswith("Approved and submitted.")


@pytest.mark.asyncio
async def test_rejected_refund_ends_without_executing_the_action(
    support_worker: Client, task_queue: str
) -> None:
    handle = await _start(support_worker, task_queue, "rejected-ticket", "Please refund my order")
    await _wait_for_pause(handle)

    await handle.signal(
        SupportTicketWorkflow.submit_decision,
        ApprovalDecision(decision="reject", reviewer="manager-1", comment="Outside policy"),
    )
    state = await handle.result()

    assert state.status is RunStatus.REJECTED
    assert state.answer == "The proposed refund was not approved: Outside policy"


@pytest.mark.asyncio
async def test_approval_deadline_auto_rejects(support_worker: Client, task_queue: str) -> None:
    """No reviewer ever responds; the time-skipping server fast-forwards the deadline."""

    handle = await _start(
        support_worker,
        task_queue,
        "timeout-ticket",
        "Please refund my order",
        approval_timeout_hours=72,
    )
    await _wait_for_pause(handle)

    state = await handle.result()

    assert state.status is RunStatus.REJECTED
    assert state.answer is not None
    assert "approval timed out" in state.answer


@pytest.mark.asyncio
async def test_decision_is_ignored_when_not_awaiting_review(
    support_worker: Client, task_queue: str
) -> None:
    """A signal that arrives for a non-paused run must not corrupt the outcome."""

    handle = await _start(support_worker, task_queue, "early-signal-ticket", "Where is my order?")
    await handle.signal(
        SupportTicketWorkflow.submit_decision,
        ApprovalDecision(decision="approve", reviewer="manager-1"),
    )

    state = await handle.result()

    assert state.status is RunStatus.COMPLETED
    assert state.pending_action is None
