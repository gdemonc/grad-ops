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

The unit runs as `gradops` with `NoNewPrivileges=yes`. Its bounding set contains
only the capabilities bubblewrap needs inside a private user namespace; there
are no ambient capabilities. That means:

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
- `NoNewPrivileges=yes`; the bounding set contains only the capabilities
  bubblewrap needs inside its private user namespace (`CAP_SYS_ADMIN`,
  `CAP_SYS_CHROOT`, `CAP_SETUID`, `CAP_SETGID`, `CAP_MKNOD`,
  `CAP_DAC_OVERRIDE`), with no ambient capabilities
- `ProtectKernelTunables/Modules/Logs/Clock/Hostname=no` because these mount
  over `/proc` and make a fresh procfs mount inside bubblewrap fail with
  `Operation not permitted`; the per-action sandbox provides its own procfs
- `Delegate=yes`, `DelegateSubgroup=supervisor` and
  `ProtectControlGroups=no` so the service's own PID is outside the delegated
  base cgroup and the service can create one child cgroup per shell
- `RestrictNamespaces=no`, `RestrictSUIDSGID=yes`, `RestrictRealtime=yes`
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

This requires systemd cgroup delegation: the unit sets `Delegate=yes` and moves
its own process into `DelegateSubgroup=supervisor`, leaving the delegated base
empty so controllers can be enabled. The application discovers its base from
`/proc/self/cgroup` and strips the `supervisor` subgroup. For that reason
`ProtectControlGroups=no` is used; it is a deliberate tradeoff in exchange for
writable per-action cgroups. If the delegated subtree is not writable,
startup fails instead of falling back to `direct`.

This path was verified on a real systemd host after installing the unit.
The repeatable check is:

```bash
sudo scripts/verify-cgroup.sh
```

It verifies the unit properties, the delegated controllers, the shell
integration tests and cleanup. To exercise `cpu.max`, set a non-zero
`GRADOPS_CGROUP_CPU_QUOTA_MICROS` in `/etc/gradops/gradops.env`, restart the
service, and run the script again; it then also checks the CPU controller and
`cpu.max`.

Both tests passed; the shell saw `memory.max=67108864` and `pids.max=32`, the
timeout killed the cgroup, and no `gradops-*` child cgroup was left behind.
The same host was then restarted with `GRADOPS_CGROUP_CPU_QUOTA_MICROS=50000`
and `cpu.max=50000 100000` passed; the controller list included `cpu`.

## Bubblewrap sandbox

`gradops.env.example` also enables:

```bash
GRADOPS_SANDBOX=bubblewrap
GRADOPS_BWRAP=/usr/bin/bwrap
GRADOPS_SANDBOX_NETWORK=false
```

Each shell then gets private mount/PID/IPC/UTS/user and network namespaces, a
read-only root, a writable `/tmp`, and read-write bind mounts only for
`GRADOPS_ALLOWED_ROOTS` and the console scratch directory. The network is
loopback-only unless `GRADOPS_SANDBOX_NETWORK=true`.

Because bubblewrap creates namespaces, the unit must not block them:

```ini
RestrictNamespaces=no
```

This is a deliberate tradeoff: `/var/log` write access remains available to
the shell after policy approval, but the root and the rest of the host
filesystem are read-only inside the sandbox.

The app probes bubblewrap at startup and refuses to start if the sandbox cannot
be created. Verify an installed host with:

```bash
sudo systemctl show gradops -p RestrictNamespaces
sudo grep -E '^GRADOPS_SANDBOX|^GRADOPS_BWRAP|^GRADOPS_SANDBOX_NETWORK' /etc/gradops/gradops.env
sudo /usr/local/go/bin/go test ./internal/shell -run TestBubblewrapSessionHasNoHostNetworkOrWritableRoot -v
```

The service-level unit still does not make `/var/log` read-only; the bubblewrap
sandbox is the per-shell filesystem boundary. Namespace/network behavior has
not yet been tested from inside the installed systemd service.

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
