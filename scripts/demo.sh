#!/usr/bin/env bash
# End-to-end smoke of the read-only loop against a real process.
#
# It builds the binary, starts it on a temporary database, submits a task and
# waits for the worker to investigate and seal it, then prints the evidence, the
# timeline and the audit chain check. It exits non-zero if any of that does not
# happen, so it is usable as a smoke test rather than a demo that always passes.
#
# Usage: scripts/demo.sh [addr]
set -euo pipefail

ADDR="${1:-${GRADOPS_ADDR:-127.0.0.1:8080}}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
BIN="$WORKDIR/gradops"
DB="$WORKDIR/gradops.db"
LOG="$WORKDIR/server.log"

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "==> building"
(cd "$ROOT" && go build -o "$BIN" ./cmd/gradops)

echo "==> starting on $ADDR with database $DB"
GRADOPS_ADDR="$ADDR" \
GRADOPS_DB_PATH="$DB" \
GRADOPS_LOG_LEVEL=warn \
GRADOPS_BASH="${GRADOPS_BASH:-/bin/bash}" \
GRADOPS_WORK_DIR="${GRADOPS_WORK_DIR:-/}" \
"$BIN" >"$LOG" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 100); do
  if curl -fsS "http://$ADDR/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "server exited:" >&2
    cat "$LOG" >&2
    exit 1
  fi
  sleep 0.2
done

echo "==> creating a task"
TASK_ID="$(curl -fsS -X POST "http://$ADDR/api/tasks" \
  -H 'Content-Type: application/json' \
  -d '{"request_text":"check the disk and report usage"}' | sed -n 's/.*"id":\([0-9]*\).*/\1/p')"
if [[ -z "$TASK_ID" ]]; then
  echo "could not read the task id" >&2
  exit 1
fi
echo "    task $TASK_ID"

echo "==> waiting for the worker"
STATUS=""
for _ in $(seq 1 100); do
  STATUS="$(curl -fsS "http://$ADDR/api/tasks/$TASK_ID" | sed -n 's/.*"status":"\([A-Z_]*\)".*/\1/p')"
  case "$STATUS" in
    SEALED|FAILED|REJECTED) break ;;
  esac
  sleep 0.2
done

echo "    status: $STATUS"
echo "==> evidence"
curl -fsS "http://$ADDR/api/tasks/$TASK_ID/evidence"
echo
echo "==> timeline"
curl -fsS "http://$ADDR/api/tasks/$TASK_ID/timeline"
echo
echo "==> audit chain"
AUDIT="$(curl -fsS "http://$ADDR/api/audit/verify")"
echo "$AUDIT"

if [[ "$STATUS" != "SEALED" ]]; then
  echo "expected the task to be sealed, got $STATUS" >&2
  exit 1
fi
if [[ "$AUDIT" != *'"ok":true'* ]]; then
  echo "audit chain check failed" >&2
  exit 1
fi

echo "==> ok"
