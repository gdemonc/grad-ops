#!/usr/bin/env bash
# Verify the installed Grad-Ops service and its per-action cgroup v2 path.
#
# Usage: sudo scripts/verify-cgroup.sh [unit]
#
# This checks the unit's delegation properties and the delegated cgroup
# subtree, then runs the real shell integration tests against that subtree.
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "verify-cgroup.sh must run as root" >&2
  exit 1
fi

UNIT="${1:-gradops.service}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO="${GO:-$(command -v go || true)}"
if [[ -z "$GO" ]]; then
  echo "go not found; set GO=/path/to/go" >&2
  exit 1
fi

fail() {
  echo "cgroup check failed: $*" >&2
  exit 1
}

echo "==> unit properties"
systemctl show "$UNIT" -p Delegate -p DelegateSubgroup -p ProtectControlGroups \
  -p MemoryMax -p TasksMax -p MainPID

delegate="$(systemctl show "$UNIT" -p Delegate --value)"
[[ "$delegate" == "yes" ]] || fail "Delegate is $delegate, want yes"
subgroup="$(systemctl show "$UNIT" -p DelegateSubgroup --value)"
[[ "$subgroup" == "supervisor" ]] || fail "DelegateSubgroup is $subgroup, want supervisor"
protect="$(systemctl show "$UNIT" -p ProtectControlGroups --value)"
[[ "$protect" == "no" ]] || fail "ProtectControlGroups is $protect, want no"

echo "==> delegated cgroup"
control_group="$(systemctl show "$UNIT" -p ControlGroup --value)"
if [[ -z "$control_group" ]]; then
  main_pid="$(systemctl show "$UNIT" -p MainPID --value)"
  control_group="$(sed -n 's/^0:://p' "/proc/$main_pid/cgroup")"
  control_group="${control_group%/supervisor}"
fi
cgroup="/sys/fs/cgroup${control_group}"
[[ -d "$cgroup" ]] || fail "cgroup $cgroup does not exist"
echo "    $cgroup"
echo -n "    controllers: "; cat "$cgroup/cgroup.controllers"
echo -n "    subtree_control: "; cat "$cgroup/cgroup.subtree_control"
grep -qw memory "$cgroup/cgroup.subtree_control" || fail "memory controller is not enabled"
grep -qw pids "$cgroup/cgroup.subtree_control" || fail "pids controller is not enabled"

quota="$(grep -E '^GRADOPS_CGROUP_CPU_QUOTA_MICROS=' /etc/gradops/gradops.env 2>/dev/null | tail -1 | cut -d= -f2- || true)"
period="$(grep -E '^GRADOPS_CGROUP_CPU_PERIOD_MICROS=' /etc/gradops/gradops.env 2>/dev/null | tail -1 | cut -d= -f2- || true)"
quota="${quota:-0}"
period="${period:-100000}"
if [[ "$quota" != "0" ]]; then
  grep -qw cpu "$cgroup/cgroup.subtree_control" || fail "cpu controller is not enabled while a quota is configured"
fi

echo "==> shell integration tests"
(
  cd "$ROOT"
  GRADOPS_TEST_CGROUP_ROOT="$cgroup" \
  GRADOPS_TEST_CGROUP_CPU_QUOTA_MICROS="$quota" \
  GRADOPS_TEST_CGROUP_CPU_PERIOD_MICROS="$period" \
  "$GO" test ./internal/shell -count=1 -v \
    -run 'TestSessionRunsInADedicatedCgroup|TestCgroupTimeoutKillsTheGroup|TestCgroupCPUQuotaIsApplied|TestCgroupMemoryLimitIsEnforced|TestCgroupTimeoutKillsAnEscapedProcessGroup'
)

leftover="$(find "$cgroup" -maxdepth 1 -type d -name 'gradops-*' -print)"
[[ -z "$leftover" ]] || fail "leftover child cgroups:\n$leftover"

echo "==> cgroup check ok"
