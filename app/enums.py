from __future__ import annotations

import enum


class TaskStatus(str, enum.Enum):
    QUEUED = "QUEUED"
    INVESTIGATING = "INVESTIGATING"
    AWAITING_APPROVAL = "AWAITING_APPROVAL"
    EXECUTING = "EXECUTING"
    VERIFYING = "VERIFYING"
    SEALED = "SEALED"
    REJECTED = "REJECTED"
    BLOCKED = "BLOCKED"
    ROLLED_BACK = "ROLLED_BACK"
    FAILED = "FAILED"


class RiskLevel(str, enum.Enum):
    R0 = "R0"   # 只读观测
    R1 = "R1"   # 可逆副作用
    R2 = "R2"   # 需审批副作用
    R3 = "R3"   # 高风险副作用
    R4 = "R4"   # 危险/越权，直接拒绝


class ProposalStatus(str, enum.Enum):
    DRAFT = "DRAFT"
    PENDING_APPROVAL = "PENDING_APPROVAL"
    APPROVED = "APPROVED"
    REJECTED = "REJECTED"
    DENIED_BY_POLICY = "DENIED_BY_POLICY"
    EXECUTED = "EXECUTED"
    FAILED = "FAILED"


class JobStatus(str, enum.Enum):
    QUEUED = "QUEUED"
    RUNNING = "RUNNING"
    SUCCEEDED = "SUCCEEDED"
    FAILED = "FAILED"
    CANCELLED = "CANCELLED"
