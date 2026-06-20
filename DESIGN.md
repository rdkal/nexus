# Nexus — Design Document

## Overview

Nexus is a git-native process manager and deployment system. It turns a collection
of git repositories into a self-managing, continuously-deployed system running
directly on the host — no containers, no cloud primitives. The only delivery
mechanism is git.

A single `curl` invocation installs the daemon. From that point, everything —
including updates to nexus itself — is driven by commits.

Nexus runs entirely in user space. No root required.

---

## Core Concepts

| Concept | Description |
|---|---|
| **Project** | The fundamental unit in nexus. Defined by a `nexus.yaml`: a name, an optional build step, volumes, services, and nested projects. Projects compose recursively — a project's `projects:` field lists other projects, each of which is the same type. A nested project is either **external** (has a `src:` pointing to another git repo) or **inline** (defined directly in the parent, no separate repo) |
| **Deployment** | One running instance of a project at a specific commit SHA. One `nexus.yaml` = one deployment: build, volumes, and services all versioned and rolled out together |
| **Service** | A named long-running process within a deployment. All services in a deployment share the same build and worktree |
| **Volume** | A named directory outside all worktrees that persists across deployments |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA. One active worktree per deployment |

A deployment is the rollout unit: all its services are built, stopped, and started together.
Services that depend on each other are expected to retry on their own — no startup ordering.
External nested projects are independent — they watch their own refs and deploy on their own schedule.
Inline nested projects share the parent's worktree and are deployed together with it.

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --project github.com/myorg/system-a \
  --project github.com/myorg/system-b \
  --project github.com/myorg/system-c:my-custom-name   # optional name override
```

`--project` can be given multiple times. The spec path after `:` sets a custom project
name; omitting it defaults to the final path segment. Projects can also be added or
removed after installation:

```sh
nexus project add github.com/myorg/system-a
nexus project add github.com/myorg/system-a:my-custom-name
nexus project remove my-system
```

The install script:

1. Installs `nexus-launcher` to `~/.local/bin/nexus-launcher` (never updated again — see Self-Update)
2. Installs the initial `nexus` binary to `$NEXUS_HOME/bin/nexus`
3. Creates `$NEXUS_HOME/` directory structure
4. Registers and starts a user-mode service pointing at `nexus-launcher`:
   - Linux: `systemctl --user enable/start nexus`
   - macOS: `launchctl load ~/Library/LaunchAgents/nexus.plist`
5. Clones each `--project` repo and begins watching it

No root or sudo required.

---

## NEXUS_HOME

All nexus runtime state lives under a single configurable directory.

| Precedence | Source |
|---|---|
| 1 | `--home <path>` flag on the nexus binary |
| 2 | `NEXUS_HOME` environment variable |
| 3 | `~/.nexus` (default) |

Using a custom `NEXUS_HOME` lets you run multiple isolated nexus instances on one machine
or relocate state to a larger disk.

---

## Names

There are two distinct kinds of names in nexus. They serve different purposes and must not be confused.

### Spec paths

A spec path identifies a `nexus.yaml` by where it lives — without any protocol prefix:

```
github.com/myorg/my-system
github.com/nexus-community/postgres
github.com/myorg/monorepo/services/api
```

Spec paths appear in `src:` fields inside `projects:` and in `--project` flags at install
time. Nexus resolves the actual transport (SSH, HTTPS, local) from the git CLI configuration,
so no scheme is needed. Spec paths are only ever used for git operations — cloning, polling,
worktree checkout. They play no role in identifying resources at runtime.

Inline projects — `projects:` entries without a `src:` — have no spec path and no
independent git identity. They are defined entirely within the parent `nexus.yaml`.

### Resource names

Resource names identify everything within the nexus universe.

**Project name**: for external projects, defaults to the final path segment of the spec
path, but can be overridden at the point of adding or nesting — never inside `nexus.yaml`
itself. Keeping the name out of the file is what makes a `nexus.yaml` fully composable:
the same file can be added as a root project under any name, or nested under any alias.
Inline projects have no global project name — they are only addressable through their
parent.

```
github.com/myorg/my-system           →  my-system      (default)
github.com/nexus-community/postgres  →  postgres       (default)
github.com/myorg/monorepo/services/api  →  api         (default)
```

Project names are **globally unique** within a nexus instance.

**Project alias**: the key used in the parent's `projects:` map. When a project nests
another, the alias becomes the address segment for that sub-deployment. It must be unique
within the parent project alongside all service and volume names.

**Resource name**: service and volume names declared in `nexus.yaml`. Must be unique within
their deployment — no two resources of any type (services, volumes, or project aliases) in
the same `nexus.yaml` may share a name.

**Full resource address**: `<project-name>/<alias-or-resource>`, with no type segment.
The project name is always the first segment:

```
my-system/db              the "db" nested project within my-system
my-system/db/postgres     service "postgres" in the db project
my-system/db/data         volume "data" in the db project
my-system/api/api-server  service "api-server" in the api project
my-system/api/uploads     volume "uploads" in the api project
```

**Short name**: a bare name with no slash. Within a deployment, resources can be
referenced by short name alone — sibling services, sibling volumes, and sibling projects.
The presence of a `/` distinguishes a full address from a short name.

---

## Directory Layout

Three top-level trees, each with a distinct addressing scheme:

```
$NEXUS_HOME/                                         default: ~/.nexus
│
├── bin/
│   └── nexus                                        nexus daemon binary
│
├── nexus.db                                         sqlite: deployment state, service state
│
├── repos/                                           keyed by spec path — external projects only
│   │
│   │   Bare clones live at the spec-path and are shared read-only git object stores.
│   │   Worktrees live under the root deployment's spec-path, named by project alias.
│   │   Inline projects have no entry here — they share the parent's worktree.
│   │
│   ├── github.com/nexus-community/postgres/
│   │   └── .git/                                    bare clone (shared git objects)
│   │
│   ├── github.com/myorg/api/
│   │   └── .git/                                    bare clone
│   │
│   └── github.com/myorg/my-system/
│       ├── .git/                                    bare clone for my-system
│       ├── worktrees/
│       │   └── <sha>/                               my-system's own worktree
│       ├── db/                                      worktrees for the "db" nested project
│       │   └── worktrees/
│       │       └── <sha>/
│       └── api/                                     worktrees for the "api" nested project
│           ├── worktrees/
│           │   └── <sha>/
│           └── shared-lib/                          worktrees for api's "shared-lib" project
│               └── worktrees/
│                   └── <sha>/
│
├── volumes/                                         keyed by full resource address
│   └── my-system/
│       ├── db/
│       │   └── data/                                volume "data" in the db project
│       └── api/
│           └── uploads/                             volume "uploads" in the api project
│
└── logs/                                            keyed by full resource address
    └── my-system/
        ├── <sha>-build.log
        ├── db/
        │   ├── <sha>-build.log
        │   └── postgres/
        │       └── current.log                      service log (rotated)
        └── api/
            ├── <sha>-build.log
            └── api-server/
                └── current.log
```

`repos/` is keyed by spec path so bare clones are shared across all projects with the same
URL. `volumes/` and `logs/` are keyed by resource address starting from the root project
name. Operationally: volumes are the only thing that must be backed up; repos and logs
can be freely wiped and rebuilt.

---

## nexus.yaml Specification

A `nexus.yaml` defines one project: its build, volumes, services, and nested projects.
For external projects, nexus fetches the file from the git repo identified by the spec
path — the file lives at the path within the repo that matches the spec path's subdirectory.
Inline projects are defined directly inside their parent's `nexus.yaml` with no file of
their own. Neither kind declares a project name — naming always happens at the site where
a project is added or nested. The schema is self-referential: a project's `projects:`
field lists other projects, each described the same way. Each nested project is either
**external** (has a `src:`) or **inline** (no `src:`).

### Minimal example (external projects only)

```yaml
# spec path: github.com/myorg/my-system
# project name: my-system

projects:
  db:                                         # external — src: points to another repo
    src: github.com/nexus-community/postgres
    ref: "@v15"
  api:
    src: github.com/myorg/api
    ref: "@main"
```

### Full example (external + inline projects)

```yaml
# spec path: github.com/myorg/api
# project name: api

projects:
  shared-lib:                                 # external project
    src: github.com/myorg/shared-lib
    ref: "@main"
  metrics:                                    # inline project — no src:, lives in this worktree
    services:
      exporter:
        run: ./scripts/metrics-exporter.sh

# Runs once inside the new worktree before any services are started.
# Non-zero exit aborts the deployment; currently running services are untouched.
build: pip install -e . && alembic upgrade head

# Persistent directories. Survive across deployments.
# Exposed as $NEXUS_VOLUME_<NAME> (uppercased).
volumes:
  uploads: {}

# Named long-running processes. Key is the service name.
services:
  api-server:
    run: uvicorn app:main --host 0.0.0.0 --port 8080

  api-worker:
    run: celery -A app.tasks worker --concurrency 4
```

### Community/reusable example

```yaml
# spec path: github.com/nexus-community/postgres
# project name: postgres

build: ./scripts/init.sh

volumes:
  data: {}

services:
  postgres:
    run: postgres -D $NEXUS_VOLUME_DATA -c listen_addresses='*' -p 5432
```

A parent that nests this under alias `db` addresses its resources as
`<root>/db/postgres` and `<root>/db/data`.

---

### Field Reference

#### `projects` (map)

Key: local alias for the nested project. Becomes the address segment for all resources
within it. Must be unique within this `nexus.yaml` alongside all service and volume names.

The presence or absence of `src:` determines the project type:

**External project** — has `src:`. Points to another git repo, gets its own worktree,
and is polled and deployed independently.

| Field | Required | Description |
|---|---|---|
| `src` | yes | Spec path (no scheme). Nexus resolves the transport from git CLI config. Used only for git operations — plays no role in addressing |
| `ref` | no | Ref to track, prefixed with `@`. Defaults to `@main`. See ref syntax below |

**Ref syntax:**

| Value | Behaviour |
|---|---|
| `@main` | Track the tip of branch `main`. Redeploys on every new commit |
| `@v15` | Pin to tag `v15`. Redeploys only if the tag is moved (rare) |
| `@latest` | Track the highest semver tag. Uses `git ls-remote --tags --sort=-version:refname`, takes the top result |

**Inline project** — no `src:`. Defined directly in the parent `nexus.yaml`, shares the
parent's worktree, and is deployed as part of the parent. Supports the same fields as a
top-level project (`build:`, `volumes:`, `services:`, `projects:`), but has no independent
git identity and no global project name.

#### `volumes` (map)

Key: volume name. Unique within this deployment alongside service names and project aliases.
Exposed as `$NEXUS_VOLUME_<NAME>` (uppercased) in build and service commands. Stored at
`$NEXUS_HOME/volumes/<resource-address>/`. Currently no sub-fields; value is an empty map `{}`.

#### `services` (map)

Key: service name. Unique within this deployment alongside volume names and project aliases.

| Field | Required | Description |
|---|---|---|
| `run` | yes | Shell command. Spawned with `sh -c`. Working directory is the directory containing the `nexus.yaml` (equals worktree root for single-repo projects; may be a subdirectory for monorepos) |

---

## Naming Rules

- **Project names** are globally unique within a nexus instance. Nexus validates at startup
  and rejects configurations where two projects share a name.
- **Resource names** — services, volumes, and project aliases — must all be unique within
  the same `nexus.yaml`. A service and a volume may not share a name; nor may a service and
  a project alias.
- No reserved words. Without type segments in addresses there is no ambiguity to guard against.

---

## Environment Injected into Build and Services

```sh
NEXUS_HOME=<path>
NEXUS_PROJECT=<project name, e.g. postgres>
NEXUS_SHA=<full-commit-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_VOLUME_<NAME>=<absolute-path>    # one per declared volume; resolves to $NEXUS_HOME/volumes/<resource-address>
```

---

## Deployment Lifecycle

### Triggering

Nexus polls each deployment on a 30-second interval using `git ls-remote`, a lightweight
ref-listing operation that downloads no objects:

```sh
# branch
git ls-remote origin refs/heads/main

# tag
git ls-remote origin refs/tags/v15

# @latest — all tags sorted by version, take the highest
git ls-remote --tags --sort=-version:refname origin 'refs/tags/*'
```

When the resolved SHA differs from the last recorded SHA, a new deployment is enqueued.

**Independent polling**: each external project polls its own ref independently and is only
redeployed when its own ref changes. Inline projects have no ref and are always redeployed
together with their parent.

**Queuing rule**: at most one pending SHA per deployment is held. If a new commit arrives
while a build is in progress, it replaces the queued SHA. Nexus always converges to the
latest commit without processing every intermediate one.

### Sequence

```
1. DETECT
   └── New SHA queued for deployment

2. CHECKOUT  (external projects only)
   └── git worktree add <worktree-path> <sha>
       (objects read from the spec-path bare clone;
        worktree placed under the root deployment's repo dir, named by project alias;
        inline projects skip this step — they use the parent's worktree)

3. BUILD  (inside the new worktree)
   ├── sh -c "<build command>"   (skipped if build is not declared)
   ├── stdout/stderr → $NEXUS_HOME/logs/<resource-address>/<sha>-build.log
   ├── Exit 0 → proceed to SWAP
   └── Non-zero exit:
         remove worktree
         mark SHA as failed in nexus.db
         keep current services running
         alert shown in UI

4. SWAP
   ├── 4a. SHUTDOWN current services
   │       SIGTERM to each supervised service
   │       Wait (daemon-wide grace period, default 30s)
   │       SIGKILL anything still alive
   │
   └── 4b. STARTUP new services
           Spawn each service's `run` from the nexus.yaml directory
           Register PIDs with the supervisor

5. VERIFY  (5-second window)
   ├── Any service exits → ROLLBACK
   └── All alive → proceed

6. PROMOTE
   └── Record new SHA as active in nexus.db

7. CLEANUP
   └── git worktree remove <old-sha>
```

### Rollback

If VERIFY fails:

1. SIGTERM + SIGKILL all services of the failed deployment
2. Restart services from the **previous** worktree (retained until now)
3. Mark new SHA as `failed` in nexus.db
4. Failed worktree kept until next successful deployment (for log inspection)
5. Alert shown in UI

---

## Process Supervision

Nexus is the supervisor for all service processes. No per-service systemd units.

- Services are direct child processes of the nexus daemon
- On unexpected exit: restart with exponential backoff — 1s, 2s, 4s … cap 60s
- More than 5 crashes in 60 seconds → service marked `degraded`, no further auto-restart, UI alert
- During planned SHUTDOWN: SIGTERM, then SIGKILL after grace period (daemon-wide, 30s default)

---

## Nexus Self-Update

Nexus uses a **thin launcher** to avoid a circular restart problem.

### Two-binary design

```
~/.local/bin/nexus-launcher         installed once by install.sh, never updated by nexus
$NEXUS_HOME/bin/nexus               the real daemon binary, updated by deployments
```

The OS service unit points at `nexus-launcher`. The launcher is a minimal shell script
that exec's `$NEXUS_HOME/bin/nexus`. It never changes.

### Self-update flow

Nexus manages itself by nesting its own repo:

```yaml
projects:
  nexus:
    src: github.com/rdkal/nexus
    ref: "@main"
```

`github.com/rdkal/nexus` — nexus.yaml:
```yaml
build: ./scripts/build.sh   # compiles new binary to $NEXUS_HOME/bin/nexus.next,
                             # then atomically moves it to $NEXUS_HOME/bin/nexus
services:
  nexus-daemon:
    run: $NEXUS_HOME/bin/nexus daemon
  nexus-ui:
    run: python -m nexus.ui --socket $NEXUS_HOME/nexus.sock --port 7777
```

When a new nexus commit lands:

1. **BUILD**: new binary compiled and atomically written to `$NEXUS_HOME/bin/nexus`.
   The old binary is already in memory — replacing the file has no effect on the running process.
2. **SHUTDOWN**: nexus sends SIGTERM to its own `nexus-daemon` service (itself).
   It writes all pending state to `nexus.db` and exits.
3. The OS init system sees the process exit and restarts `nexus-launcher`, which
   exec's `$NEXUS_HOME/bin/nexus` — now the new binary.
4. **STARTUP**: new nexus reads `nexus.db`, reconstructs the full service tree, and
   resumes supervision of all other services — including `nexus-ui`, which it starts normally.

Nexus does **not** try to spawn `nexus-daemon` as a child process. The OS service unit
owns the restart. Nexus recognises it is managing itself and skips the STARTUP step for
`nexus-daemon` only, leaving that entirely to the init system. All other services
(including `nexus-ui`) are started as usual.

**Key invariant**: all state lives in `nexus.db` and the filesystem. The new process
reconstructs everything from disk with no handshake with the old one. Other services
keep running through the brief daemon restart.

---

## Web UI

The web UI is a Python ([iris](https://rdkal.github.io/iris)) process that runs as a
nexus-managed service. It connects to the daemon's Unix socket and serves the public
HTTP interface on port 7777. HTTP only, no authentication — intended for private network use.

The daemon never binds a public port. The Python process is the sole HTTP interface.

The URL scheme mirrors the resource name tree.

### Pages

| Page | Content |
|---|---|
| `/` | All projects, current SHA per deployment, service health |
| `/<project-name>` | Project detail: history, current SHA, build log |
| `/<project-name>/<alias>` | Nested project detail |
| `/<project-name>/.../<name>` | Service status and live log tail, or volume info |

Examples:
```
/my-system
/my-system/db
/my-system/db/postgres
/my-system/api/api-server
/my-system/api/uploads
```

### API

Served by the Python process, backed by the daemon socket:

```
GET  /api/<project-name>
GET  /api/<project-name>/history
POST /api/<project-name>/redeploy
GET  /api/<project-name>/services
GET  /api/<project-name>/services/<name>
POST /api/<project-name>/services/<name>/restart
```

---

## Implementation

### Daemon (Go)

The nexus daemon is written in Go. It owns:

- Process supervision and restart backoff
- Git polling via `git ls-remote`
- Deployment lifecycle (detect, checkout, build, swap, verify, rollback)
- State persistence in SQLite (`nexus.db`)
- Unix socket at `$NEXUS_HOME/nexus.sock`

The daemon never binds a public network port. All external access goes through
the Python web UI.

### Web UI (Python)

The web UI is written in Python using [iris](https://rdkal.github.io/iris). It is
defined as a service inside nexus's own `nexus.yaml` and supervised by the daemon
like any other process. It connects to `$NEXUS_HOME/nexus.sock` using standard HTTP
over a Unix socket transport and serves the public interface on port 7777.

### Daemon socket

The daemon exposes an HTTP API over the Unix socket. The Python UI is the only client:

```
GET  /projects
GET  /projects/<address>
GET  /projects/<address>/history
POST /projects/<address>/redeploy
GET  /projects/<address>/services
GET  /projects/<address>/services/<name>/log
POST /projects/<address>/services/<name>/restart
```

This is an internal interface — its shape can change freely without affecting users.

---

## Testing

### Go tests

Go's `testing` package covers all daemon behavior in isolation. Tests run with
`go test ./...` and require no external processes other than `git` itself.

What is covered:

- Ref parsing: `@main`, `@v15`, `@latest` from `git ls-remote` output
- SHA comparison and queuing (latest-wins, at-most-one-pending)
- Deployment lifecycle state machine transitions
- Process supervision: restart backoff timing, degraded state detection
- Socket handler correctness
- Volume and log path derivation from resource addresses
- Project tree loading (external, inline, nested)

Git operations are tested against local bare repos created in `t.TempDir()` — no
network access required.

### pytest e2e tests

pytest covers the full system end-to-end: a real daemon binary managing real git
repos and real processes. Tests live in `tests/` at the repo root.

Setup per test session:

- Temporary `NEXUS_HOME` directory
- Local bare git repos (`file:///...`) standing in for remote repos
- Daemon started as a subprocess; tests communicate via the Unix socket
- Git commits pushed via `git` CLI; assertions made against daemon socket responses
  and the filesystem (worktrees, volumes, logs)

```python
def test_service_starts_after_commit(nexus, repo):
    repo.commit("nexus.yaml", minimal_project("server", run="python -m http.server"))
    nexus.add_project(repo.url)
    assert nexus.wait_for_service("server", state="running", timeout=10)

def test_service_restarts_on_crash(nexus, repo):
    repo.commit("nexus.yaml", minimal_project("crasher", run="exit 1"))
    nexus.add_project(repo.url)
    status = nexus.wait_for_service("crasher", state="degraded", timeout=30)
    assert status["restart_count"] >= 5
```

e2e tests require the compiled daemon binary, Python, and git available in `PATH`.
They are slower and intended to run in CI rather than on every save.

---

## v1 Scope

**In scope:**
- Install script (`curl … | sh`) with multiple `--project` flags
- `nexus.yaml` with `projects` (external and inline), `build`, `volumes`, `services`
- Project-name-based resource addressing; volumes and logs namespaced by resource address
- Bare clones at spec-path, worktrees per external project instance named by project alias
- Git polling via `git ls-remote` (30s), worktree-based deployments
- Build → SIGTERM old → start new lifecycle with rollback
- Commit queuing (latest-wins, depth 1)
- Process supervision with restart backoff and degraded state
- Daemon-wide 30s shutdown grace period
- Self-update via thin launcher
- `NEXUS_HOME` configuration
- Go daemon with Unix socket internal API
- Python/iris web UI as a supervised nexus service (port 7777)
- REST API served by the Python process

**Explicitly deferred:**
- Flows / pipelines
- TLS / authentication on the web UI
- Inbound webhooks (polling only for now)
- Secret management
- Cross-deployment volume path injection (see Open Questions)
- Multi-machine execution
- Windows support

---

## Open Questions

1. **`depends_on` / startup ordering**: not in v1. Services crash-loop until their
   dependencies are available — the process supervisor's exponential backoff handles this.
   Explicit ordering and `bind:` wiring can be added later without breaking existing configs.

2. **Cross-deployment volume paths**: a service sometimes needs the volume path of another
   project (e.g. api needs postgres's socket path). V1 services coordinate via well-known
   host paths or out-of-band env vars. A future `bind:` mechanism could inject these paths.

3. **`@latest` tie-breaking**: non-semver tag names that sort equally under `version:refname`
   fall back to tag creation date. Decided — no further action needed.
