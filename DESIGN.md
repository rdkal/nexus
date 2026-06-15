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
| **Project** | The named root of a deployment tree. Every `nexus.yaml` belongs to a project whose name is the final path segment of its git URL — globally unique within a nexus instance |
| **App** | An included project. When one project includes another, the included deployment becomes an app addressed by the include alias the parent assigns |
| **Deployment** | The rollout unit nexus manages. One `nexus.yaml` = one deployment: a single build step, a set of volumes, and one or more services — all versioned and deployed together |
| **Service** | A named long-running process within a deployment. All services in a deployment share the same build and worktree |
| **Volume** | A named directory outside all worktrees that persists across deployments |
| **Include** | A declaration that pulls another repo into the deployment tree under a local alias. The alias becomes the address segment for all resources in that sub-deployment |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA. One active worktree per deployment |
| **Bind** | A wiring declaration at the include site that resolves a service's abstract dependency alias to a concrete resource |

A deployment is the rollout unit: all its services are built, stopped, and started together.
`depends_on` between services expresses startup ordering only — there is no restart propagation.
Included deployments are fully independent; they watch their own refs and deploy on their own schedule.

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --project github.com/myorg/system-a \
  --project github.com/myorg/system-b
```

`--project` can be given multiple times. Each URL is a root deployment that nexus
clones and watches independently. Projects can also be added or removed after
installation with `nexus project add <url>` and `nexus project remove <url>`.

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

Spec paths appear in `url:` fields inside `includes:` and in `--project` flags at install
time. Nexus resolves the actual transport (SSH, HTTPS, local) from the git CLI configuration,
so no scheme is needed. Spec paths are only ever used for git operations — cloning, polling,
worktree checkout. They play no role in identifying resources at runtime.

### Resource names

Resource names identify everything within the nexus universe.

**Project name**: defaults to the final path segment of the spec path, but can be
overridden with an explicit `name:` field in `nexus.yaml`:

```
github.com/myorg/my-system           →  my-system      (default)
github.com/nexus-community/postgres  →  postgres       (default)
github.com/myorg/monorepo/services/api  →  api         (default)
```

Project names are **globally unique** within a nexus instance.

**Include alias**: the key used in the parent's `includes:` map. When a project includes
another, the alias becomes the address segment for that sub-deployment. It must be unique
within the parent project alongside all service and volume names.

**Resource name**: service and volume names declared in `nexus.yaml`. Must be unique within
their deployment — no two resources of any type (services, volumes, or include aliases) in
the same `nexus.yaml` may share a name.

**Full resource address**: `<project-name>/<alias-or-resource>`, with no type segment.
The project name is always the first segment:

```
my-system/db              the "db" include within project my-system
my-system/db/postgres     service "postgres" in the db include
my-system/db/data         volume "data" in the db include
my-system/api/api-server  service "api-server" in the api include
my-system/api/uploads     volume "uploads" in the api include
```

**Short name**: a bare name with no slash. Within a deployment, resources can be
referenced by short name alone — sibling services, sibling volumes, and sibling includes.
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
├── repos/                                           keyed by spec path (URL without scheme)
│   │
│   │   Bare clones live at the spec-path and are shared read-only git object stores.
│   │   Worktrees live under the root deployment's spec-path, named by include alias.
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
│       ├── db/                                      worktrees for the "db" include
│       │   └── worktrees/
│       │       └── <sha>/
│       └── api/                                     worktrees for the "api" include
│           ├── worktrees/
│           │   └── <sha>/
│           └── shared-lib/                          worktrees for api's "shared-lib" include
│               └── worktrees/
│                   └── <sha>/
│
├── volumes/                                         keyed by full resource address
│   └── my-system/
│       ├── db/
│       │   └── data/                                volume "data" in the db include
│       └── api/
│           └── uploads/                             volume "uploads" in the api include
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

`repos/` is keyed by spec path so bare clones are shared across all includes of the same
URL. `volumes/` and `logs/` are keyed by resource address starting from the root project
name. Operationally: volumes are the only thing that must be backed up; repos and logs
can be freely wiped and rebuilt.

---

## nexus.yaml Specification

Every managed repo has a `nexus.yaml` at its root. The project name defaults to the
final segment of the spec path but can be overridden with a top-level `name:` field.

### Minimal example (aggregator only)

```yaml
# spec path: github.com/myorg/my-system
# project name: my-system

includes:
  db:                                         # include alias — "db" becomes the address segment
    url: github.com/nexus-community/postgres
    ref: "@v15"
  api:
    url: github.com/myorg/api
    ref: "@main"
    bind:
      database: db/postgres                   # alias "database" → postgres service in db include
```

### Full example (repo with services)

```yaml
# spec path: github.com/myorg/api
# project name: api

includes:
  shared-lib:
    url: github.com/myorg/shared-lib
    ref: "@main"

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
    depends_on:
      - database        # abstract alias — resolved by bind: at the include site
      - api-worker      # short name — sibling service in this deployment

  api-worker:
    run: celery -A app.tasks worker --concurrency 4
    depends_on:
      - database
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

A parent that includes this under alias `db` addresses its resources as
`<root>/db/postgres` and `<root>/db/data`.

---

### Field Reference

#### `name` (string, optional)

Overrides the project name. Defaults to the final segment of the spec path. Must be
globally unique within the nexus instance.

```yaml
name: my-custom-name
```

#### `includes` (map)

Key: local alias for this include. Becomes the address segment for all resources in the
sub-deployment. Must be unique within this `nexus.yaml` alongside all service and volume names.

| Field | Required | Description |
|---|---|---|
| `url` | yes | Spec path (no scheme). Nexus resolves the transport from git CLI config. Used only for git operations — plays no role in addressing |
| `ref` | no | Ref to track, prefixed with `@`. Defaults to `@main`. See ref syntax below |
| `bind` | no | Map of `alias: <target>` resolving dependency aliases. Resolution rules TBD — see Open Questions |

**Ref syntax:**

| Value | Behaviour |
|---|---|
| `@main` | Track the tip of branch `main`. Redeploys on every new commit |
| `@v15` | Pin to tag `v15`. Redeploys only if the tag is moved (rare) |
| `@latest` | Track the highest semver tag. Uses `git ls-remote --tags --sort=-version:refname`, takes the top result |

#### `volumes` (map)

Key: volume name. Unique within this deployment alongside service names and include aliases.
Exposed as `$NEXUS_VOLUME_<NAME>` (uppercased) in build and service commands. Stored at
`$NEXUS_HOME/volumes/<resource-address>/`. Currently no sub-fields; value is an empty map `{}`.

#### `services` (map)

Key: service name. Unique within this deployment alongside volume names and include aliases.

| Field | Required | Description |
|---|---|---|
| `run` | yes | Shell command. Spawned with `sh -c` from the worktree root |
| `depends_on` | no | List of names this service needs before it starts. Bare name = sibling; other forms TBD |

---

## Naming Rules

- **Project names** are globally unique within a nexus instance. Nexus validates at startup
  and rejects configurations where two projects share a name.
- **Resource names** — services, volumes, and include aliases — must all be unique within
  the same `nexus.yaml`. A service and a volume may not share a name; nor may a service and
  an include alias.
- No reserved words. Without type segments in addresses there is no ambiguity to guard against.

---

## Dependency Resolution

`depends_on` and `bind:` express service dependencies. Full resolution rules are TBD
(see Open Questions). The established semantics:

- **Short name** (no slash): sibling service in the same deployment. Always unambiguous.
- **Cross-deployment**: use a `bind:` alias at the include site. The parent maps an abstract
  alias to a concrete resource; the included service uses the bare alias as a short name in
  `depends_on`.
- Full address forms and `bind:` path syntax are TBD.

### What `depends_on` does

- **Startup ordering**: dependency services start before this service
- **Shutdown ordering**: dependency services stop after this service (reverse of startup order)
- **Nothing else**: if a dependency crashes or redeploys, dependent services are not touched.
  Services are expected to handle reconnection on their own.

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

**Independent polling**: each deployment polls its own ref independently. An include is
only redeployed when its own ref changes — not when its parent or a sibling redeploys.

**Queuing rule**: at most one pending SHA per deployment is held. If a new commit arrives
while a build is in progress, it replaces the queued SHA. Nexus always converges to the
latest commit without processing every intermediate one.

### Sequence

```
1. DETECT
   └── New SHA queued for deployment

2. CHECKOUT
   └── git worktree add <worktree-path> <sha>
       (objects read from the spec-path bare clone;
        worktree placed under the root deployment's repo dir, named by include alias)

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
   ├── 4a. SHUTDOWN current services (in reverse dependency order)
   │       SIGTERM to each supervised service
   │       Wait (daemon-wide grace period, default 30s)
   │       SIGKILL anything still alive
   │
   └── 4b. STARTUP new services (in dependency order)
           Spawn each service's `run` from the new worktree
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

Nexus manages itself by including its own repo:

```yaml
includes:
  nexus:
    url: github.com/rdkal/nexus
    ref: "@main"
```

`github.com/rdkal/nexus` — nexus.yaml:
```yaml
build: ./scripts/build.sh   # compiles new binary to $NEXUS_HOME/bin/nexus.next,
                             # then atomically moves it to $NEXUS_HOME/bin/nexus
services:
  nexus-daemon:
    run: $NEXUS_HOME/bin/nexus daemon
```

When a new nexus commit lands:

1. **BUILD**: new binary compiled and atomically written to `$NEXUS_HOME/bin/nexus`.
   The old binary is already in memory — replacing the file has no effect on the running process.
2. **SHUTDOWN**: nexus sends SIGTERM to its own `nexus-daemon` service (itself).
   It writes all pending state to `nexus.db` and exits.
3. The OS init system sees the process exit and restarts `nexus-launcher`, which
   exec's `$NEXUS_HOME/bin/nexus` — now the new binary.
4. **STARTUP**: new nexus reads `nexus.db`, reconstructs the full service tree, and
   resumes supervision of all other services.

Nexus does **not** try to spawn `nexus-daemon` as a child process. The OS service unit
owns the restart. Nexus recognises it is managing itself and skips the STARTUP step for
`nexus-daemon`, leaving that entirely to the init system.

**Key invariant**: all state lives in `nexus.db` and the filesystem. The new process
reconstructs everything from disk with no handshake with the old one. Other services
keep running through the brief daemon restart.

---

## Web UI

Nexus serves a minimal HTTP UI on a configurable port (default `7777`).
HTTP only, no authentication — intended for private network use.

The UI URL scheme mirrors the resource name tree.

### Pages

| Page | Content |
|---|---|
| `/` | All projects, current SHA per deployment, service health |
| `/<project-name>` | Project detail: history, current SHA, build log |
| `/<project-name>/<alias>` | App detail for an included deployment |
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

```
GET  /api/<project-name>
GET  /api/<project-name>/history
POST /api/<project-name>/redeploy
GET  /api/<project-name>/services
GET  /api/<project-name>/services/<name>
POST /api/<project-name>/services/<name>/restart
```

---

## v1 Scope

**In scope:**
- Install script (`curl … | sh`) with multiple `--project` flags
- `nexus.yaml` with `includes`, `build`, `volumes`, `services`, `depends_on`, `bind`
- Project-name-based resource addressing; volumes and logs namespaced by resource address
- Bare clones at spec-path, worktrees per deployment instance named by include alias
- Git polling via `git ls-remote` (30s), worktree-based deployments
- Build → SIGTERM old → start new lifecycle with rollback
- Dependency-ordered startup and shutdown (no restart propagation)
- Commit queuing (latest-wins, depth 1)
- Process supervision with restart backoff and degraded state
- Daemon-wide 30s shutdown grace period
- Self-update via thin launcher
- `NEXUS_HOME` configuration
- Web UI (read-only + manual redeploy trigger)
- REST API

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

1. **`depends_on` and `bind:` resolution**: full rules for how `depends_on` entries resolve
   across project boundaries and what path syntax `bind:` values use are TBD. The short-name
   form (sibling service, no slash) and the alias-via-bind mechanism are established;
   everything else is deferred.

2. **Cross-deployment volume paths**: a service in one deployment sometimes needs the volume
   path of another (e.g. api needs postgres's socket path). Extending `bind:` to wire volume
   paths would solve this: `bind: { db-data: db/data }` → injects `$NEXUS_BIND_DB_DATA=<path>`.
   Deferred to v2; v1 services coordinate via well-known host paths or out-of-band env vars.

3. **`@latest` tie-breaking**: non-semver tag names that sort equally under `version:refname`
   fall back to tag creation date. Decided — no further action needed.
