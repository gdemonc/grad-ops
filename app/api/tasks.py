from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Query
from pydantic import BaseModel, Field
from sqlalchemy.orm import Session

from app.db import get_session
from app.models import (
    ActionProposal,
    Approval,
    AuditEvent,
    Evidence,
    ExecutionRecord,
    Task,
    VerificationRecord,
)
from app.queue import TaskQueue

router = APIRouter(prefix="/api", tags=["tasks"])

SessionDep = Annotated[Session, Depends(get_session)]


class CreateTaskRequest(BaseModel):
    request_text: str = Field(min_length=1)
    source: str = "api"


def _task_not_found() -> HTTPException:
    return HTTPException(status_code=404, detail="任务不存在")


@router.post("/tasks", status_code=201)
def create_task(body: CreateTaskRequest, session: SessionDep) -> dict:
    task = TaskQueue(session).enqueue(body.request_text, source=body.source)
    session.commit()
    return {"id": task.id, "trace_id": task.trace_id, "status": task.status}


@router.get("/tasks")
def list_tasks(
    session: SessionDep,
    status: Annotated[str | None, Query()] = None,
) -> list[dict]:
    query = session.query(Task)
    if status:
        query = query.filter(Task.status == status)
    tasks = query.order_by(Task.id.desc()).all()
    return [
        {
            "id": task.id,
            "trace_id": task.trace_id,
            "request_text": task.request_text,
            "status": task.status,
            "summary": task.summary,
            "created_at": task.created_at,
        }
        for task in tasks
    ]


@router.get("/tasks/{task_id}")
def get_task(task_id: int, session: SessionDep) -> dict:
    task = session.get(Task, task_id)
    if task is None:
        raise _task_not_found()
    proposals = (
        session.query(ActionProposal)
        .filter_by(task_id=task.id)
        .order_by(ActionProposal.id.asc())
        .all()
    )
    proposal_ids = [proposal.id for proposal in proposals]
    approvals = (
        session.query(Approval)
        .filter(Approval.proposal_id.in_(proposal_ids))
        .order_by(Approval.id.asc())
        .all()
        if proposal_ids
        else []
    )
    executions = (
        session.query(ExecutionRecord)
        .filter(ExecutionRecord.proposal_id.in_(proposal_ids))
        .order_by(ExecutionRecord.id.asc())
        .all()
        if proposal_ids
        else []
    )
    verifications = (
        session.query(VerificationRecord)
        .filter_by(task_id=task.id)
        .order_by(VerificationRecord.id.asc())
        .all()
    )
    evidence = (
        session.query(Evidence)
        .filter_by(task_id=task.id)
        .order_by(Evidence.id.asc())
        .all()
    )
    grounding_event = (
        session.query(AuditEvent)
        .filter_by(trace_id=task.trace_id, event_type="grounding.claims_bound")
        .order_by(AuditEvent.seq.desc())
        .first()
    )
    return {
        "id": task.id,
        "trace_id": task.trace_id,
        "request_text": task.request_text,
        "source": task.source,
        "status": task.status,
        "summary": task.summary,
        "created_at": task.created_at,
        "updated_at": task.updated_at,
        "evidence": [
            {
                "id": item.id,
                "kind": item.kind,
                "summary": item.summary,
                "content_hash": item.content_hash,
                "created_at": item.created_at,
            }
            for item in evidence
        ],
        "proposals": [
            {
                "id": proposal.id,
                "tool_name": proposal.tool_name,
                "risk_level": proposal.risk_level,
                "rationale": proposal.rationale,
                "status": proposal.status,
                "contract_digest": proposal.contract_digest,
                "preconditions_json": proposal.preconditions_json,
                "postconditions_json": proposal.postconditions_json,
            }
            for proposal in proposals
        ],
        "approvals": [
            {
                "id": approval.id,
                "proposal_id": approval.proposal_id,
                "status": approval.status,
                "decided_by": approval.decided_by,
                "comment": approval.comment,
            }
            for approval in approvals
        ],
        "executions": [
            {
                "id": record.id,
                "proposal_id": record.proposal_id,
                "ok": record.ok,
                "exit_code": record.exit_code,
                "stdout": record.stdout,
                "stderr": record.stderr,
                "duration_ms": record.duration_ms,
                "executed_at": record.executed_at,
            }
            for record in executions
        ],
        "verifications": [
            {
                "id": record.id,
                "proposal_id": record.proposal_id,
                "checks_json": record.checks_json,
                "passed": record.passed,
                "note": record.note,
                "verified_at": record.verified_at,
            }
            for record in verifications
        ],
        "grounding_summary": grounding_event.payload_json if grounding_event else None,
    }


@router.get("/tasks/{task_id}/timeline")
def get_task_timeline(task_id: int, session: SessionDep) -> list[dict]:
    task = session.get(Task, task_id)
    if task is None:
        raise _task_not_found()
    events = (
        session.query(AuditEvent)
        .filter_by(trace_id=task.trace_id)
        .order_by(AuditEvent.seq.asc())
        .all()
    )
    return [
        {
            "seq": event.seq,
            "stage": event.stage,
            "event_type": event.event_type,
            "message": event.message,
            "payload_json": event.payload_json,
            "created_at": event.created_at,
        }
        for event in events
    ]
