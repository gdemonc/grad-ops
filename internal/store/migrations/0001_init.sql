-- 0001_init.sql
--
-- Phase 1 baseline schema: the durable records the control loop needs before
-- any investigation, policy or execution logic exists.
--
-- Rules applied here:
--   * timestamps are canonical UTC text (RFC 3339 with nanoseconds);
--   * every table is STRICT so a wrong type fails instead of being coerced;
--   * audit_events is append-only, enforced with triggers rather than by
--     convention;
--   * a task's state changes and its audit events share one transaction, so
--     the schema rejects a parentless child (foreign keys are enforced by the
--     connection, see store.Open).

CREATE TABLE tasks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    request_text  TEXT    NOT NULL,
    source        TEXT    NOT NULL DEFAULT 'web',
    status        TEXT    NOT NULL,
    summary       TEXT,
    failure_stage TEXT,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    CHECK (length(trim(request_text)) > 0)
) STRICT;

CREATE INDEX idx_tasks_status     ON tasks(status);
CREATE INDEX idx_tasks_created_at ON tasks(created_at);

-- A durable, claimable unit of work. Lease fields are what lets a crashed
-- worker's task be recovered instead of silently lost.
CREATE TABLE task_jobs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id          INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind             TEXT    NOT NULL,
    status           TEXT    NOT NULL DEFAULT 'PENDING',
    lease_owner      TEXT,
    lease_expires_at TEXT,
    attempts         INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL DEFAULT 3,
    available_at     TEXT    NOT NULL,
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    CHECK (attempts >= 0),
    CHECK (max_attempts > 0)
) STRICT;

CREATE INDEX idx_task_jobs_claim ON task_jobs(status, available_at);
CREATE INDEX idx_task_jobs_task  ON task_jobs(task_id);

-- Observed facts. content_hash binds the stored content to the hash the task
-- used, so a conclusion can be tied to the exact observation it cited.
CREATE TABLE evidence (
    id           TEXT    PRIMARY KEY,
    task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    tool         TEXT    NOT NULL,
    command      TEXT,
    observed_at  TEXT    NOT NULL,
    content      TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    truncated    INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_evidence_task ON evidence(task_id);

-- A proposed action and the contract that froze it. contract_digest is what the
-- executor recomputes before running anything.
CREATE TABLE proposals (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    tool            TEXT    NOT NULL,
    arguments       TEXT    NOT NULL,
    risk_level      TEXT    NOT NULL,
    policy_version  TEXT    NOT NULL,
    contract        TEXT    NOT NULL,
    contract_digest TEXT    NOT NULL,
    status          TEXT    NOT NULL,
    rationale       TEXT,
    created_at      TEXT    NOT NULL
) STRICT;

CREATE INDEX idx_proposals_task ON proposals(task_id);

-- One approval decision. token_hash is the only form of the credential that is
-- stored; expires_at and consumed_at bound its use.
CREATE TABLE approvals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    proposal_id INTEGER NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    approver    TEXT,
    comment     TEXT,
    decision    TEXT    NOT NULL,
    token_hash  TEXT,
    expires_at  TEXT,
    consumed_at TEXT,
    created_at  TEXT    NOT NULL
) STRICT;

CREATE INDEX idx_approvals_proposal ON approvals(proposal_id);

-- What actually ran. The outcome is explicit so that an interrupted execution
-- can be recorded as UNKNOWN instead of being mistaken for a failure.
CREATE TABLE execution_records (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    proposal_id      INTEGER NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    outcome          TEXT    NOT NULL,
    exit_code        INTEGER,
    stdout_digest    TEXT,
    stderr_digest    TEXT,
    output_truncated INTEGER NOT NULL DEFAULT 0,
    run_as           TEXT,
    runtime          TEXT,
    started_at       TEXT    NOT NULL,
    finished_at      TEXT,
    duration_ms      INTEGER
) STRICT;

CREATE INDEX idx_execution_proposal ON execution_records(proposal_id);

-- Independent postcondition checks. evidence_id must reference evidence
-- collected after the execution, never the investigation evidence.
CREATE TABLE verification_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    proposal_id INTEGER REFERENCES proposals(id) ON DELETE SET NULL,
    postcondition TEXT  NOT NULL,
    passed      INTEGER NOT NULL,
    evidence_id TEXT,
    checked_at  TEXT    NOT NULL
) STRICT;

CREATE INDEX idx_verification_task ON verification_records(task_id);

-- Append-only audit chain. prev_hash has a UNIQUE constraint so a second event
-- cannot fork from the same predecessor, and event_hash covers the predecessor
-- hash, so editing an old event breaks every later link.
CREATE TABLE audit_events (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER,
    actor        TEXT    NOT NULL,
    event_type   TEXT    NOT NULL,
    message      TEXT    NOT NULL,
    payload      TEXT    NOT NULL,
    payload_hash TEXT    NOT NULL,
    prev_hash    TEXT    NOT NULL UNIQUE,
    event_hash   TEXT    NOT NULL UNIQUE,
    created_at   TEXT    NOT NULL
) STRICT;

CREATE INDEX idx_audit_task ON audit_events(task_id, seq);

CREATE TRIGGER audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;

CREATE TRIGGER audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
