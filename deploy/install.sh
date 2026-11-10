#!/usr/bin/env bash
# Install Grad-Ops as a hardened systemd service.
#
# Usage: sudo deploy/install.sh /path/to/gradops
#
# The script creates the non-root "gradops" account, installs the binary and
# unit, seeds /etc/gradops/gradops.env on first install, and enables the
# service. Existing environment files are never overwritten.
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "install.sh must run as root" >&2
  exit 1
fi

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${1:-}"
if [[ -z "$BIN" ]]; then
  echo "usage: $0 /path/to/gradops" >&2
  exit 1
fi
if [[ ! -x "$BIN" ]]; then
  echo "binary is not executable: $BIN" >&2
  exit 1
fi
for tool in systemctl getent groupadd useradd install; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 1
  fi
done

if ! getent group gradops >/dev/null; then
  groupadd --system gradops
fi
if ! id -u gradops >/dev/null 2>&1; then
  useradd --system --gid gradops --home-dir /var/lib/gradops \
    --no-create-home --shell /usr/sbin/nologin gradops
fi

install -d -o gradops -g gradops -m 0750 /var/lib/gradops
install -d -o root -g gradops -m 0750 /etc/gradops
install -m 0755 "$BIN" /usr/local/bin/gradops

if [[ ! -f /etc/gradops/gradops.env ]]; then
  install -o root -g gradops -m 0600 \
    "$SOURCE_DIR/gradops.env.example" /etc/gradops/gradops.env
  echo "created /etc/gradops/gradops.env; review it before exposing the API"
else
  chown root:gradops /etc/gradops/gradops.env
  chmod 0600 /etc/gradops/gradops.env
fi

install -o root -g root -m 0644 "$SOURCE_DIR/gradops.service" /etc/systemd/system/gradops.service
systemctl daemon-reload
systemctl enable --now gradops.service

echo "gradops.service installed."
echo "The default listener is 127.0.0.1:8080. Set GRADOPS_API_TOKEN before"
echo "binding to a non-loopback address, then: systemctl restart gradops.service"
