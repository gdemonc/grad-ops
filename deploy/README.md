# Deploying Grad-Ops with systemd

This is the first supported single-host Linux deployment. It runs the Go
service as the dedicated non-root `gradops` account under a hardened systemd
unit. Per-shell resource limits are still applied by the application; the unit
adds a service-wide filesystem, privilege and resource boundary.

## Requirements

- Ubuntu LTS with systemd and util-linux (`prlimit`)
- A built binary: `go build -o bin/gradops ./cmd/gradops`
- Root access for installation

## Install

```bash
sudo deploy/install.sh ./bin/gradops
sudoedit /etc/gradops/gradops.env   # review the listener and token
sudo systemctl restart gradops.service
systemctl status --no-pager gradops.service
```

The installer:

- creates the `gradops` system group and user
- creates `/var/lib/gradops` (database) and `/etc/gradops` (environment)
- installs `/usr/local/bin/gradops`
- installs `/etc/systemd/system/gradops.service`
- writes `/etc/gradops/gradops.env` from the example **only if it is absent**
- enables and starts the service

The environment file is installed as `0600 root:gradops`.

## Listener and API token

The default is `GRADOPS_ADDR=127.0.0.1:8080`. The service refuses to start on a
non-loopback address unless `GRADOPS_API_TOKEN` is set to at least 16
characters.

When a token is set, every `/api` route except `/healthz` and `/api/health`
requires:

```http
Authorization: Bearer <token>
```

The console can read the same value from the browser `localStorage` key
`gradops_api_token`; it is not baked into the frontend bundle.

## Identity

The unit runs as `gradops` with `NoNewPrivileges=yes` and an empty capability
set. That means:

- `GRADOPS_EXECUTOR_USER` must be empty or `gradops`
- the service cannot switch to another user
- `sudo` and setuid helpers are expected to fail

True per-action identity separation needs a privileged broker or a delegated
runtime; it is not implemented yet. Do not weaken the unit by running the whole
service as root to work around this.

## Filesystem and resource boundary

The unit uses:

- `ProtectSystem=full` (read-only `/usr`, `/boot`, `/etc`)
- `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`
- `NoNewPrivileges=yes`, empty `CapabilityBoundingSet`
- `ProtectKernelTunables/Modules/Logs/Clock/Hostname=yes`
- `Delegate=yes`, `DelegateSubgroup=supervisor` and
  `ProtectControlGroups=no` so the service's own PID is outside the delegated
  base cgroup and the service can create one child cgroup per shell
- `RestrictNamespaces=yes`, `RestrictSUIDSGID=yes`, `RestrictRealtime=yes`
- `MemoryMax=1G`, `TasksMax=256`, `LimitNOFILE=1024`
- `KillMode=control-group` so stopping the service also stops shell children

`ProtectSystem=full` leaves `/var` writable because the default policy allows
declared actions under `/var/log` and because the SQLite database lives under
`/var/lib/gradops`. The policy's allowed roots and the action contract are still
the application-level boundary. A stricter `ProtectSystem=strict` deployment
must add explicit `ReadWritePaths=` for every writable target.

## Per-action cgroup v2

`gradops.env.example` enables `GRADOPS_RUNTIME=cgroup-v2`. The service creates a
fresh child cgroup for each shell and writes `memory.max`, `pids.max` and,
when configured, `cpu.max`. The child is placed in that cgroup at clone time
with `CLONE_INTO_CGROUP`, so it does not run outside the boundary first.

This requires systemd cgroup delegation: the unit sets `Delegate=yes`. For that
reason `ProtectControlGroups=no` is used; it is a deliberate tradeoff in exchange
for writable per-action cgroups. If the delegated subtree is not writable,
startup fails instead of falling back to `direct`.

The integration test for a real delegated cgroup skips when the host's cgroup
subtree is not writable. The unit and this path have not yet been installed and
validated on a real systemd host.

The unit still does not add a network or mount namespace and does not make
`/var/log` read-only. Those remain open work.

## Verify

```bash
systemctl status --no-pager gradops.service
systemd-analyze security gradops.service
journalctl -u gradops.service -n 50 --no-pager
curl -fsS http://127.0.0.1:8080/healthz
```

With a token configured, an unauthenticated `/api/tasks` must return 401.

## Uninstall

```bash
sudo systemctl disable --now gradops.service
sudo rm /etc/systemd/system/gradops.service
sudo systemctl daemon-reload
# /etc/gradops and /var/lib/gradops contain configuration and history; keep
# or remove them deliberately.
```
