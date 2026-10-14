from __future__ import annotations

import pytest
from sqlalchemy import create_engine, inspect
from sqlalchemy.exc import IntegrityError

from app.db import Base
from app.enums import ProposalStatus, RiskLevel, TaskStatus
from app.models import ActionProposal, Task


def test_metadata_creates_all_tables():
    engine = create_engine("sqlite:///:memory:")
    Base.metadata.create_all(engine)
    tables = sorted(
        name for name in inspect(engine).get_table_names() if not name.startswith("sqlite_")
    )
    assert tables == [
        "action_proposals",
        "approvals",
        "audit_events",
        "evidences",
        "execution_records",
        "task_jobs",
        "tasks",
        "verification_records",
    ]


def test_task_defaults_to_queued(session):
    task = Task(request_text="check disk usage")
    session.add(task)
    session.commit()
    session.refresh(task)
    assert task.status == TaskStatus.QUEUED.value
    assert task.trace_id
    assert task.source == "api"
    assert task.created_at is not None
    assert task.updated_at is not None


def test_trace_id_unique(session):
    session.add(Task(trace_id="dup-trace", request_text="first"))
    session.commit()
    session.add(Task(trace_id="dup-trace", request_text="second"))
    with pytest.raises(IntegrityError):
        session.commit()
    session.rollback()


def test_enum_roundtrip(session):
    task = Task(request_text="probe service health", status=TaskStatus.INVESTIGATING.value)
    session.add(task)
    session.flush()
    proposal = ActionProposal(
        task_id=task.id,
        tool_name="rotate_log_file",
        input_json={"path": "/var/log/app/app.log"},
        risk_level=RiskLevel.R3.value,
        rationale="rotate stale log",
        status=ProposalStatus.PENDING_APPROVAL.value,
    )
    session.add(proposal)
    session.commit()

    session.expire_all()
    reloaded_task = session.get(Task, task.id)
    reloaded_proposal = session.get(ActionProposal, proposal.id)

    assert reloaded_task.status == TaskStatus.INVESTIGATING.value
    assert TaskStatus(reloaded_task.status) is TaskStatus.INVESTIGATING
    assert RiskLevel(reloaded_proposal.risk_level) is RiskLevel.R3
    assert ProposalStatus(reloaded_proposal.status) is ProposalStatus.PENDING_APPROVAL


def test_audit_event_composite_index_exists(session):
    indexes = inspect(session.bind).get_indexes("audit_events")
    composite = [ix for ix in indexes if ix["column_names"] == ["trace_id", "seq"]]
    assert composite, "missing composite index on (trace_id, seq)"
    assert bool(composite[0]["unique"]) is True
