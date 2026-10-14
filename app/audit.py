from __future__ import annotations

import hashlib
import json
from typing import Any

from sqlalchemy.orm import Session

from app.models import AuditEvent

ZERO_HASH = "0" * 64


def stable_hash(payload: Any) -> str:
    data = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(data.encode("utf-8")).hexdigest()


class AuditLog:
    def __init__(self, session: Session) -> None:
        self.session = session

    def append(
        self,
        trace_id: str,
        stage: str,
        event_type: str,
        message: str,
        payload: dict | None = None,
    ) -> AuditEvent:
        prev = (
            self.session.query(AuditEvent)
            .filter_by(trace_id=trace_id)
            .order_by(AuditEvent.id.desc())
            .first()
        )
        prev_hash = prev.event_hash if prev else ZERO_HASH
        seq = (prev.seq + 1) if prev else 1
        data = payload or {}
        payload_hash = stable_hash(
            {
                "seq": seq,
                "stage": stage,
                "event_type": event_type,
                "message": message,
                "payload": data,
            }
        )
        event_hash = stable_hash({"prev_hash": prev_hash, "payload_hash": payload_hash})
        ev = AuditEvent(
            trace_id=trace_id,
            seq=seq,
            stage=stage,
            event_type=event_type,
            message=message,
            payload_json=data,
            prev_hash=prev_hash,
            payload_hash=payload_hash,
            event_hash=event_hash,
        )
        self.session.add(ev)
        self.session.flush()
        return ev

    def verify_chain(self, trace_id: str) -> dict:
        events = (
            self.session.query(AuditEvent)
            .filter_by(trace_id=trace_id)
            .order_by(AuditEvent.seq.asc())
            .all()
        )
        previous_hash = ZERO_HASH
        entries: list[dict] = []
        valid = True
        for ev in events:
            expected_payload_hash = stable_hash(
                {
                    "seq": ev.seq,
                    "stage": ev.stage,
                    "event_type": ev.event_type,
                    "message": ev.message,
                    "payload": ev.payload_json,
                }
            )
            expected_event_hash = stable_hash(
                {"prev_hash": previous_hash, "payload_hash": expected_payload_hash}
            )
            prev_ok = ev.prev_hash == previous_hash
            payload_ok = ev.payload_hash == expected_payload_hash
            event_ok = ev.event_hash == expected_event_hash
            entry_valid = prev_ok and payload_ok and event_ok
            valid = valid and entry_valid
            entries.append(
                {
                    "chain_id": ev.id,
                    "seq": ev.seq,
                    "prev_ok": prev_ok,
                    "payload_ok": payload_ok,
                    "event_ok": event_ok,
                    "valid": entry_valid,
                    "stored_event_hash": ev.event_hash,
                    "expected_event_hash": expected_event_hash,
                }
            )
            previous_hash = ev.event_hash
        return {
            "trace_id": trace_id,
            "valid": valid,
            "entry_count": len(entries),
            "head_hash": previous_hash if events else ZERO_HASH,
            "entries": entries,
        }
