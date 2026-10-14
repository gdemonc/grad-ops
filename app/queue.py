from __future__ import annotations

from datetime import datetime, timedelta

from sqlalchemy import and_, or_, update
from sqlalchemy.orm import Session

from app.audit import AuditLog
from app.enums import JobStatus, TaskStatus
from app.models import Task, TaskJob, utcnow

TERMINAL_TASK_STATUSES = {
    TaskStatus.SEALED.value,
    TaskStatus.REJECTED.value,
    TaskStatus.ROLLED_BACK.value,
    TaskStatus.FAILED.value,
}


class JobStateError(RuntimeError):
    pass


class TaskQueue:
    def __init__(self, session: Session) -> None:
        self.session = session

    def enqueue(self, request_text: str, source: str = "api") -> Task:
        task = Task(request_text=request_text, source=source)
        self.session.add(task)
        self.session.flush()
        job = TaskJob(
            task_id=task.id,
            status=JobStatus.QUEUED.value,
            available_at=utcnow(),
        )
        self.session.add(job)
        AuditLog(self.session).append(
            task.trace_id,
            "queue",
            "task.enqueued",
            f"任务 {task.id} 已入队",
            {"task_id": task.id, "source": source, "request_text": request_text},
        )
        self.session.flush()
        return task

    def enqueue_resume(self, task: Task) -> TaskJob:
        job = TaskJob(
            task_id=task.id,
            status=JobStatus.QUEUED.value,
            available_at=utcnow(),
        )
        self.session.add(job)
        AuditLog(self.session).append(
            task.trace_id,
            "queue",
            "task.enqueued",
            f"任务 {task.id} 恢复入队",
            {"task_id": task.id, "resumed": True},
        )
        self.session.flush()
        return job

    def claim_next(
        self, worker_id: str, *, lease_seconds: int = 300, now: datetime | None = None
    ) -> TaskJob | None:
        moment = now if now is not None else utcnow()
        lease_until = moment + timedelta(seconds=lease_seconds)
        claimable = or_(
            and_(TaskJob.status == JobStatus.QUEUED.value, TaskJob.available_at <= moment),
            and_(
                TaskJob.status == JobStatus.RUNNING.value,
                TaskJob.lease_expires_at.isnot(None),
                TaskJob.lease_expires_at < moment,
            ),
        )
        candidates = (
            self.session.query(TaskJob)
            .filter(claimable)
            .order_by(TaskJob.id.asc())
            .limit(8)
            .all()
        )
        for candidate in candidates:
            statement = (
                update(TaskJob)
                .where(TaskJob.id == candidate.id, claimable)
                .values(
                    status=JobStatus.RUNNING.value,
                    lease_owner=worker_id,
                    lease_expires_at=lease_until,
                    attempt_count=TaskJob.attempt_count + 1,
                )
                .execution_options(synchronize_session=False)
            )
            result = self.session.execute(statement)
            if result.rowcount == 1:
                self.session.expire(candidate)
                return candidate
        return None

    def heartbeat(self, job_id: int, worker_id: str, *, lease_seconds: int = 300) -> bool:
        moment = utcnow()
        statement = (
            update(TaskJob)
            .where(
                TaskJob.id == job_id,
                TaskJob.status == JobStatus.RUNNING.value,
                TaskJob.lease_owner == worker_id,
            )
            .values(lease_expires_at=moment + timedelta(seconds=lease_seconds))
            .execution_options(synchronize_session=False)
        )
        result = self.session.execute(statement)
        job = self.session.get(TaskJob, job_id)
        if job is not None:
            self.session.expire(job)
        return result.rowcount == 1

    def mark_succeeded(self, job_id: int, worker_id: str) -> TaskJob:
        job = self._owned_job(job_id, worker_id)
        job.status = JobStatus.SUCCEEDED.value
        self.session.flush()
        return job

    def mark_failed(self, job_id: int, worker_id: str, error: str) -> TaskJob:
        job = self._owned_job(job_id, worker_id)
        job.status = JobStatus.FAILED.value
        job.last_error = error
        task = self.session.get(Task, job.task_id)
        if task is not None and task.status not in TERMINAL_TASK_STATUSES:
            task.status = TaskStatus.FAILED.value
            task.updated_at = utcnow()
        self.session.flush()
        return job

    def release_for_retry(
        self, job_id: int, worker_id: str, *, delay_seconds: int = 0
    ) -> TaskJob:
        job = self._owned_job(job_id, worker_id)
        job.status = JobStatus.QUEUED.value
        job.lease_owner = None
        job.lease_expires_at = None
        job.available_at = utcnow() + timedelta(seconds=delay_seconds)
        self.session.flush()
        return job

    def _owned_job(self, job_id: int, worker_id: str) -> TaskJob:
        job = self.session.get(TaskJob, job_id)
        if job is None:
            raise JobStateError(f"任务作业不存在：{job_id}")
        if job.lease_owner != worker_id:
            raise JobStateError(f"任务作业 {job_id} 不由 {worker_id} 持有租约")
        return job
