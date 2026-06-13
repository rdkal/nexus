# Nexus — Design Document

## Overview

Nexus is a git-native process manager and deployment system. It turns a collection
of git repositories into a self-managing, continuously-deployed system running
directly on the host — no containers, no cloud primitives. The only delivery
mechanism is git; the only configuration format is YAML.

A single `curl` invocation installs the daemon. From that point, everything —
including updates to nexus itself — is driven by commits.

Nexus runs entirely in user space. No root required.

---

## Core Concepts

| Concept | Description |
|---|---|
| **Deployment** | The unit nexus manages: a build command, named services, and volumes, all versioned together at a git ref |
| **Include** | A reference to another repo (and its `nexus.yaml`) pulled into the current deployment tree, given a local name at the include site |
| **Service** | A named long-running process within a deployment. Nexus starts it, supervises it, and stops it |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA. One active worktree per deployment |
| **Volume** | A directory that lives outside worktrees and persists across deployments |

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --source https://github.com/myorg/my-system
```

The install script:

1. Installs the `nexus` binary to `~/.local/bin/nexus`
2. Creates `~/.nexus/` (or `$NEXUS_HOME`) for all runtime state
3. Registers and starts a **user-mode** service:
   - Linux: `systemctl --user enable/start nexus`
   - macOS: `launchctl load ~/Library/LaunchAgents/nexus.plist`
4. Clones the given `--source` repo as the root of the deployment tree

No root or sudo required.

---

## NEXUS_HOME

All nexus runtime state lives under a single configurable directory.

| Precedence | Source |
|---|---|
| 1 | `--home <path>` flag on the nexus binary |
| 2 | `NEXUS_HOME` environment variable |
| 3 | `~/.nexus` (default) |

Using `NEXUS_HOME` lets you run multiple isolated nexus instances on the same machine,
or relocate state to a larger disk.

---

## nexus.yaml Specification

Every repo nexus manages has a `nexus.yaml` at its root. There is no `name` field —
deployments get their name from the parent that includes them. The root deployment
has no name (it is the root).

```yaml
# nexus.yaml

# Pull in other repos. Each becomes an independently managed deployment,
# identified by the name given here.
includes:
  - name: api
    url: https://github.com/myorg/api-service
    ref: main
  - name: postgres
    url: https://github.com/nexus-community/postgres
    ref: v15

# Optional build command. Runs once inside the new worktree before services start.
# Non-zero exit aborts the deployment; current services keep running.
build: pip install -e . && alembic upgrade head

# Persistent directories. Survive across deployments.
# Accessible as $NEXUS_VOLUME_<NAME> (uppercased) in build and service commands.
volumes:
  - name: data
    path: $NEXUS_HOME/volumes/api-data

# Named long-running processes. Nexus starts, supervises, and stops them.
services:
  - name: api
    run: uvicorn app:main --host 0.0.0.0 --port 8080
  - name: worker
    run: celery -A app.tasks worker --concurrency 4
```

A `nexus.yaml` that only has `includes` and no `build`/`services` is valid —
useful for a root config that is purely an aggregator.

### Field Reference

#### `includes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Local identifier for this deployment. Used in logs, UI, and path namespacing |
| `url` | yes | Git-cloneable URL |
| `ref` | no | Branch, tag, or SHA. Defaults to `main` |

#### `volumes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Identifier. Exposed as `$NEXUS_VOLUME_<NAME>` in build and service commands |
| `path` | yes | Absolute path or path containing `$NEXUS_HOME`. Created on first use if absent |

#### `services[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique within this deployment. Used in logs and the UI |
| `run` | yes | Shell command. Runs with `sh -c` from the worktree root |

---

## Environment Injected into Build and Services

```sh
NEXUS_HOME=<path>              # e.g. /home/user/.nexus
NEXUS_SHA=<full-commit-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_VOLUME_<NAME>=<path>     # one per declared volume, name uppercased
```

---

## Deployment Lifecycle

### New commit detected

```
1. DETECT
   ├── Poll `git fetch origin <ref>` every 30s
   └── New SHA → enqueue (see Queuing below)

2. CHECKOUT
   └── git worktree add $NEXUS_HOME/worktrees/<slug>/<sha> <sha>

3. BUILD  (runs inside the new worktree)
   ├── sh -c "<build command>"
   ├── Capture stdout/stderr to $NEXUS_HOME/logs/<slug>/<sha>-build.log
   ├── Exit 0 → proceed
   └── Non-zero → remove worktree, mark SHA as failed, keep current running

4. SWAP
   ├── 4a. SHUTDOWN current services
   │       SIGTERM to every supervised service
   │       Wait (daemon-wide grace period, default 30s)
   │       SIGKILL anything still alive
   │
   └── 4b. STARTUP new services
           Spawn each service's `run` command from the new worktree
           Register PIDs with the supervisor

5. VERIFY  (5-second window)
   ├── If any service exits within 5s → ROLLBACK
   └── All alive → proceed

6. PROMOTE
   └── Record new SHA as active in nexus.db

7. CLEANUP
   └── git worktree remove <old-sha>  (old worktree deleted)
```

### Rollback

If VERIFY fails:

1. SIGTERM + SIGKILL all services from the failed deployment
2. Restart each service from the **previous** worktree (retained until now)
3. Mark new SHA as `failed` in nexus.db
4. Failed worktree kept on disk until next successful deployment (for log inspection)
5. Alert shown in the UI

### Queuing

If a new commit arrives while a build is already in progress:

- The incoming SHA is enqueued (queue depth: 1 — only the latest pending SHA is kept)
- When the current build finishes (success or failure), the queued SHA is processed next
- If another commit arrives while one is already queued, the queued SHA is replaced by the newer one

This means nexus always converges to the latest commit without processing every intermediate SHA.

---

## Includes and Reusable Deployments

The `includes` mechanism is designed to enable third-party, reusable deployments.
For example, a community-maintained Postgres deployment can be included without
any local configuration beyond the include line:

```yaml
# my-system nexus.yaml
includes:
  - name: db
    url: https://github.com/nexus-community/postgres
    ref: v15
  - name: api
    url: https://github.com/myorg/api
    ref: main
```

The `postgres` repo publishes a `nexus.yaml` that defines the service and volumes.
The `name: db` at the include site namespaces everything — worktrees, logs — so
including the same repo twice with different names works cleanly.

Volume path namespacing for included deployments: volumes defined with `$NEXUS_HOME`
in their path naturally pick up the deploying user's home. Absolute paths in
third-party `nexus.yaml` files should be avoided in favour of `$NEXUS_HOME`-relative
paths to stay portable.

---

## Process Supervision

Nexus is the supervisor for all service processes. It does not use systemd units
per service.

- Each service is a direct child process of the nexus daemon
- On unexpected exit: restart with exponential backoff — 1s, 2s, 4s … cap 60s
- Crashes more than 5 times in 60s → service marked `degraded`, no further restarts, UI alert shown
- On planned SHUTDOWN: SIGTERM, then SIGKILL after the daemon-wide grace period (default 30s)

---

## Nexus Self-Update

Nexus uses a **thin launcher** pattern to avoid a circular update problem.

### Components

```
~/.local/bin/nexus-launcher   ← installed once by the install script, never updated by nexus
~/.nexus/bin/nexus            ← the actual nexus binary, updated by deployments
```

The OS service unit (`systemd --user` / `launchd`) points to `nexus-launcher`.
The launcher simply exec's `~/.nexus/bin/nexus`. It never changes.

### Self-update flow

The root `nexus.yaml` (or a dedicated nexus-update repo) includes:

```yaml
build: ./scripts/install-binary.sh   # compiles or downloads new nexus to ~/.nexus/bin/nexus
services:
  - name: nexus
    run: nexus daemon --home $NEXUS_HOME
```

When a new nexus commit lands:

1. The new binary is built/downloaded into `~/.nexus/bin/nexus` during the BUILD step
   (the old binary is still running from the previous worktree's path — it is unaffected)
2. SHUTDOWN: the current nexus service process receives SIGTERM
3. Nexus writes all state to disk, closes file handles, and exits cleanly
4. STARTUP: `nexus daemon` is spawned — the launcher picks up the new binary from `~/.nexus/bin/nexus`
5. The new nexus process reads `nexus.db`, resumes supervision of all other services

The key invariant: nexus state is entirely in `nexus.db` and the filesystem. The
new process can reconstruct full state on startup with no handshake with the old one.

---

## Directory Layout

```
$NEXUS_HOME/                          default: ~/.nexus
  bin/
    nexus                             ← current nexus binary (updated by deployments)
  repos/
    <slug>/
      .git/                           ← bare clone
      worktrees/
        <sha>/                        ← one checkout per active/pending deployment
  volumes/
    <name>/                           ← persistent across deployments
  nexus.db                            ← sqlite: deployments, service state
  logs/
    <slug>/
      <sha>-build.log
      <sha>-<service-name>.log
```

Slug format: for the root deployment, the slug is `root`. For includes, it is the
dot-joined path of names from the root, e.g. `api`, `db`, `api.shared-lib`.

---

## Web UI

Nexus serves a minimal HTTP UI on a configurable port (default `7777`).
HTTP only, no authentication — intended for private network use.

### Pages

| Page | Content |
|---|---|
| `/` | All deployments, current SHA, service health summary |
| `/deployments/<slug>` | Deployment history, build log per SHA, service status |
| `/deployments/<slug>/logs/<sha>/<service>` | Tailed service log |
| `/volumes` | Declared volumes and disk usage |

### API

```
GET  /api/deployments
GET  /api/deployments/<slug>
GET  /api/deployments/<slug>/history
POST /api/deployments/<slug>/redeploy    ← re-run current SHA (e.g. after volume change)
GET  /api/services
POST /api/services/<id>/restart
```

---

## v1 Scope

**In scope:**
- Install script (`curl … | sh`)
- `nexus.yaml` with `includes`, `build`, `volumes`, `services`
- Git polling, worktree-based deployments
- Build → SIGTERM → start lifecycle with rollback
- Commit queuing (latest-wins, depth 1)
- Process supervision with restart backoff
- Daemon-wide shutdown grace period (30s default)
- Self-update via thin launcher pattern
- `NEXUS_HOME` configuration
- Minimal web UI (read-only + manual redeploy)
- REST API

**Explicitly deferred:**
- Flows / pipelines (v2)
- TLS / authentication
- Inbound webhooks (polling only)
- Secret management
- Cross-deployment dependency ordering
- Multi-machine execution
- Windows support

---

## Open Questions

1. **Grace period configuration**: daemon-wide 30s default for v1. Should this be
   overridable per service in v2?

2. **Volume path convention**: should third-party `nexus.yaml` files be expected to
   use `$NEXUS_HOME/volumes/<name>` by convention, or should nexus inject a
   per-include volume root automatically?

3. **Root deployment identity**: the root repo itself is a deployment. Should it have
   a reserved slug (`root`) or derive its slug from the git remote URL?
