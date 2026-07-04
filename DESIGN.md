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

Nexus is distributed as source and built on the host: the installer requires `go`
(>= 1.22) and `git` on `PATH`. No prebuilt binaries and no root are needed. Binaries
are produced with `go install` into `$NEXUS_HOME/bin` (from the published module by
default, or from a local checkout via `NEXUS_SRC`).

The install script:

1. Builds `nexus-pm` into `$NEXUS_HOME/bin/nexus-pm` (the process manager — thin, stable, rarely updated)
2. Builds the initial `nexus` runtime into `$NEXUS_HOME/bin/nexus`
3. Creates `$NEXUS_HOME/` directory structure
4. Registers each `--project` repo via `nexus project add`
5. Installs and starts a user-mode service pointing at `nexus-pm`:
   - Linux: `systemctl --user enable --now nexus` (unit at `~/.config/systemd/user/nexus.service`)
   - macOS: `launchctl load ~/Library/LaunchAgents/com.rdkal.nexus.plist`
   - Neither available: prints instructions to run `nexus-pm` manually

`nexus-pm` starts and supervises the `nexus` runtime automatically at boot.

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
│   ├── nexus-pm                                     process manager binary (systemd target)
│   └── nexus                                        runtime binary (supervised by nexus-pm)
│
├── nexus.db                                         sqlite: deployment state, service state
├── nexus-pm.sock                                    process manager API (spawn/stop/status)
├── nexus.sock                                       runtime API (projects/services/logs)
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

`nexus-pm` owns all OS processes. The `nexus` runtime delegates process management to it
over `nexus-pm.sock` — nexus holds no process handles itself. This means the runtime can be
freely restarted (for updates) without touching any supervised service.

`nexus-pm` also supervises the `nexus` runtime binary as a hardcoded service: it starts nexus
at boot and restarts it on crash, just like any user service.

- Services are direct child processes of `nexus-pm`
- On unexpected exit: restart with exponential backoff — 1s, 2s, 4s … cap 60s
- More than 5 crashes in 60 seconds → service marked `degraded`, no further auto-restart, UI alert
- During planned SHUTDOWN: SIGTERM, then SIGKILL after grace period (default 30s)

---

## Nexus Self-Update

### Three-process design

```
$NEXUS_HOME/bin/nexus-pm        process manager — the systemd target, rarely updated
$NEXUS_HOME/bin/nexus           runtime — updated by deployments
nexus-web (optional)            Python frontend — a normal nexus project, deployed like any other
```

`nexus-pm` is the only process systemd manages. It starts `nexus` as a supervised service and
restarts it on crash — exactly like any user service. This is the key property: restarting nexus
has no effect on user services, because nexus-pm holds all process handles.

### Self-update flow

Nexus tracks itself as a project with no services — only a build step:

```yaml
# github.com/rdkal/nexus — nexus.yaml
build: ./scripts/build.sh   # compiles new binary, atomically swaps $NEXUS_HOME/bin/nexus
# no services: — nexus-pm owns the nexus process directly
```

When a new nexus commit lands:

1. **BUILD**: new binary compiled and atomically written to `$NEXUS_HOME/bin/nexus`.
2. **PROMOTE**: SHA recorded in `nexus.db`. Old worktree removed.
3. nexus recognises the deployed project as *itself* and calls `POST /runtime/restart`
   on `nexus-pm.sock`.
4. `nexus-pm` SIGTERMs the current nexus runtime and starts a new one from the new binary.
5. New nexus connects to `nexus-pm`, recovers all project state from `nexus.db`. User services
   kept running through steps 3–4 — nexus-pm never touched them.

No launcher script. No "skip STARTUP" special case. No state handshake between old and new nexus.

**Identifying self**: a project is nexus itself when its spec path matches nexus's own
repository (`github.com/rdkal/nexus`). This is overridable with `NEXUS_SELF_SPEC` for forks,
and setting it empty disables self-update restarts entirely. Only a project matching this
spec path triggers a runtime restart after deploying; every other project deploys normally.

**Surviving the restart**: because the restart deliberately interrupts the runtime, two
properties keep the system consistent. The database is opened in WAL mode with a
`busy_timeout` and a single writer, so a brief overlap between the old and new nexus process
never corrupts state or errors out. And worktree checkout is idempotent — a deploy interrupted
after checkout but before promotion is recovered by reusing the existing worktree on restart,
rather than failing because it "already exists".

### nexus-web as a normal project

The web UI is not bundled or special-cased. It is simply a project added by the operator:

```
nexus project add github.com/rdkal/nexus-web --ref @latest
```

Its `nexus.yaml` looks like any other service project:

```yaml
build: pip install -r requirements.txt
services:
  web:
    run: python -m nexus_web --socket $NEXUS_HOME/nexus.sock --port 7777
```

`nexus-pm` manages the `web` process like any other service. Deploying or removing nexus-web
has zero effect on the runtime or any other project.

**Key invariant**: all state lives in `nexus.db` and on disk. On restart, nexus reconstructs
everything from those sources — no handshake with the old process needed.

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

### nexus-pm (Go)

The process manager is a thin Go binary. It owns:

- OS process lifecycles for all services (spawn, SIGTERM/SIGKILL, restart backoff, degraded detection)
- The hardcoded `nexus` runtime service — starts it at boot, restarts on crash
- A `nexus-pm.sock` HTTP API used by the runtime to spawn/stop/query services

nexus-pm never reads `nexus.db` or runs git commands. It is intentionally minimal so it
rarely needs updating — updating nexus-pm is the one event that briefly restarts all services.

### nexus (Go)

The runtime is written in Go. It owns:

- Git polling via `git ls-remote`
- Deployment lifecycle (detect, checkout, build, swap, verify, rollback)
- State persistence in SQLite (`nexus.db`)
- `nexus.sock` HTTP API (projects, history, services, logs, redeploy, restart)

The runtime delegates all process management to nexus-pm via `RemoteSupervisor` — a thin HTTP
client implementing the same `Spawn/Stop/Status` interface as the in-process supervisor, but
sending requests to `nexus-pm.sock`. The runtime holds no OS process handles.

The runtime never binds a public network port.

**Nested projects at runtime.** Each project — root or external sub-project — runs the same
poller + deploy-loop pair, keyed by resource address (`my-system`, `my-system/db`). After a
project deploys, the runtime reconciles the external sub-projects declared in its freshly
deployed `nexus.yaml`: newly declared ones are cloned and get their own poller/deploy-loop
(so they track their own ref independently); ones dropped from the config have their services
stopped and their loops cancelled, recursively. A sub-project's worktree is placed under the
root deployment's repo dir (`repos/<root-spec>/<alias>/worktrees/<sha>`) via the deploy
request's alias chain. Root projects persist their active SHA in the `projects` table; sub-projects
are not stored there (they are discovered from a parent's config, not managed independently),
so their active SHA is derived from the latest `active` row in the `deployments` table.
Inline sub-projects (no `src:`, sharing the parent worktree) are not yet implemented.

### nexus-web (Python)

The web UI is written in Python using [iris](https://rdkal.github.io/iris). It is an **optional**
project added by the operator — not bundled or special-cased in either nexus-pm or nexus.
It connects to `$NEXUS_HOME/nexus.sock` and serves the public HTTP interface on port 7777.

### nexus.sock API

The runtime exposes an HTTP API over the Unix socket:

```
GET  /projects
GET  /projects/<address>
GET  /projects/<address>/history
POST /projects/<address>/redeploy
GET  /projects/<address>/services
GET  /projects/<address>/services/<name>/log
POST /projects/<address>/services/<name>/restart
```

### nexus-pm.sock API

The process manager API is used exclusively by the nexus runtime:

```
POST   /services/{key}      spawn a service (no-op if already running)
DELETE /services/{key}      stop a service (blocks until exited)
GET    /services/{key}      service status
POST   /runtime/restart     stop and restart the nexus runtime binary
```

Both sockets are internal interfaces — their shape can change freely without affecting users.

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
- Self-update via nexus-pm (three-process design: nexus-pm → nexus → user services)
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
