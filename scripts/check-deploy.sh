#!/usr/bin/env bash
# Static checks for the systemd deployment files. It does not start the
# service; it catches a unit that lost its containment or a script with a
# syntax error before a host is touched.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT="$ROOT/deploy/gradops.service"
ENV_FILE="$ROOT/deploy/gradops.env.example"
INSTALL="$ROOT/deploy/install.sh"

fail() {
  echo "deploy check failed: $*" >&2
  exit 1
}

for file in "$UNIT" "$ENV_FILE" "$INSTALL"; do
  [[ -f "$file" ]] || fail "missing $file"
done

for directive in \
  'User=gradops' \
  'Group=gradops' \
  'NoNewPrivileges=yes' \
  'CapabilityBoundingSet=' \
  'PrivateTmp=yes' \
  'ProtectSystem=full' \
  'ProtectHome=yes' \
  'Delegate=yes' \
  'DelegateSubgroup=supervisor' \
  'ProtectControlGroups=no' \
  'RestrictNamespaces=no' \
  'RestrictSUIDSGID=yes' \
  'KillMode=control-group' \
  'MemoryMax=' \
  'TasksMax='; do
  grep -Eq "^${directive}" "$UNIT" || fail "unit is missing: $directive"
done

if grep -Eq '^User=root$' "$UNIT"; then
  fail "the unit must not run as root"
fi
if grep -Eq '^CapabilityBoundingSet=.*[A-Za-z]' "$UNIT"; then
  fail "CapabilityBoundingSet must be empty"
fi

grep -q 'GRADOPS_API_TOKEN=' "$ENV_FILE" || fail "environment example lacks the API token"
grep -q 'GRADOPS_ADDR=127.0.0.1' "$ENV_FILE" || fail "environment example should default to loopback"
grep -q 'GRADOPS_RUNTIME=cgroup-v2' "$ENV_FILE" || fail "environment example should enable cgroup runtime"
grep -q 'GRADOPS_SANDBOX=bubblewrap' "$ENV_FILE" || fail "environment example should enable bubblewrap"
grep -q 'GRADOPS_DB_PATH=/var/lib/gradops/' "$ENV_FILE" || fail "database should live in StateDirectory"

bash -n "$INSTALL"
bash -n "$0"

if command -v systemd-analyze >/dev/null 2>&1; then
  tmp="$(mktemp --suffix=.service)"
  trap 'rm -f "$tmp"' EXIT
  sed -e 's#^ExecStart=/usr/local/bin/gradops#ExecStart=/bin/true#' \
      -e 's#^User=gradops#User=nobody#' \
      -e 's#^Group=gradops#Group=nogroup#' \
      "$UNIT" > "$tmp"
  if ! output="$(systemd-analyze verify "$tmp" 2>&1)"; then
    echo "$output" >&2
    fail "systemd-analyze verify rejected the unit"
  fi
  if grep -Eq 'Failed to parse|Unknown lvalue|Invalid setting' <<<"$output"; then
    echo "$output" >&2
    fail "systemd-analyze reported an unusable directive"
  fi
fi

echo "deploy check ok"
