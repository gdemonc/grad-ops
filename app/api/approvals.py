from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Query
from pydantic import BaseModel
from sqlalchemy.orm import Session

from app.db import get_session
from app.enums import ProposalStatus, TaskStatus
from app.executor import issue_approval_token
from app.models import ActionProposal, Approval, Task, utcnow
from app.queue import TaskQueue
from app.worker import Worker

router = APIRouter(prefix="/api", tags=["approvals"])

SessionDep = Annotated[Session, Depends(get_session)]


class CommentRequest(BaseModel):
    comment: str = ""


def _approval_not_found() -> HTTPException:
    return HTTPException(status_code=404, detail="审批记录不存在")


def _approval_locked(approval: Approval) -> HTTPException:
    return HTTPException(
        status_code=409, detail=f"审批状态为 {approval.status}，无法重复处理"
    )


def _load_proposal(session: Session, approval: Approval) -> ActionProposal:
    proposal = session.get(ActionProposal, approval.proposal_id)
    if proposal is None:
        raise HTTPException(status_code=404, detail="处置提案不存在")
    return proposal


@router.get("/approvals")
def list_approvals(
    session: SessionDep,
    status: Annotated[str | None, Query()] = None,
) -> list[dict]:
    query = session.query(Approval)
    if status:
        query = query.filter(Approval.status == status)
    approvals = query.order_by(Approval.id.desc()).all()
    items: list[dict] = []
    for approval in approvals:
        proposal = session.get(ActionProposal, approval.proposal_id)
        if proposal is None:
            continue
        task = session.get(Task, proposal.task_id)
        items.append(
            {
                "id": approval.id,
                "proposal_id": approval.proposal_id,
                "status": approval.status,
                "decided_by": approval.decided_by,
                "decided_at": approval.decided_at,
                "comment": approval.comment,
                "tool_name": proposal.tool_name,
                "risk_level": proposal.risk_level,
                "task_id": proposal.task_id,
                "request_text": task.request_text[:200] if task is not None else "",
            }
        )
    return items


@router.post("/approvals/{approval_id}/approve")
def approve(
    approval_id: int,
    body: CommentRequest,
    session: SessionDep,
) -> dict:
    approval = session.get(Approval, approval_id)
    if approval is None:
        raise _approval_not_found()
    if approval.status != "PENDING":
        raise _approval_locked(approval)
    proposal = _load_proposal(session, approval)
    task = session.get(Task, proposal.task_id)
    if task is None:
        raise HTTPException(status_code=404, detail="任务不存在")

    _raw_token, token_hash = issue_approval_token()
    approval.status = "APPROVED"
    approval.token_hash = token_hash
    approval.decided_at = utcnow()
    approval.decided_by = "operator"
    approval.comment = body.comment
    proposal.status = ProposalStatus.APPROVED.value
    task.status = TaskStatus.EXECUTING.value
    task.updated_at = utcnow()
    session.flush()
    TaskQueue(session).enqueue_resume(task)
    session.commit()
    return {"approval_id": approval.id, "status": "APPROVED", "resumed": True}


@router.post("/approvals/{approval_id}/reject")
def reject(
    approval_id: int,
    body: CommentRequest,
    session: SessionDep,
) -> dict:
    approval = session.get(Approval, approval_id)
    if approval is None:
        raise _approval_not_found()
    if approval.status != "PENDING":
        raise _approval_locked(approval)
    proposal = _load_proposal(session, approval)
    task = session.get(Task, proposal.task_id)
    if task is None:
        raise HTTPException(status_code=404, detail="任务不存在")

    approval.status = "REJECTED"
    approval.decided_at = utcnow()
    approval.decided_by = "operator"
    approval.comment = body.comment
    proposal.status = ProposalStatus.REJECTED.value
    task.status = TaskStatus.REJECTED.value
    task.summary = body.comment
    task.updated_at = utcnow()
    session.commit()
    return {"approval_id": approval.id, "status": "REJECTED", "resumed": False}


@router.post("/tasks/run")
def run_queue(session: SessionDep) -> dict:
    worked = Worker(session).run_once()
    return {"worked": worked}
