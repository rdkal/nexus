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
| **Deployment** | The unit nexus manages. One `nexus.yaml` = one deployment: a single build step, a set of volumes, and one or more services — all versioned and deployed together as a single atomic unit |
| **Service** | A named long-running process within a deployment. All services in a deployment share the same build and worktree |
| **Include** | A reference to another repo pulled into the deployment tree, given a local name at the include site. Included deployments are independent — they only redeploy when their own ref changes |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA. One active worktree per deployment |
| **Volume** | A directory outside all worktrees that persists across deployments |
| **Bind** | A wiring declaration at the include site that resolves a service's abstract dependency name to a concrete service in the tree |

A deployment is the rollout unit: all its services are built, stopped, and started together.
`depends_on` between services expresses startup ordering only — there is no restart propagation.
Included deployments are fully independent; they watch their own refs and deploy on their own schedule.

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --source https://github.com/myorg/system-a \
  --source https://github.com/myorg/system-b
```

`--source` can be given multiple times. Each is an independently watched deployment
tree, addressed from its own URL root. Sources can also be added or removed after
installation with `nexus source add <url>` and `nexus source remove <url>`.

The install script:

1. Installs `nexus-launcher` to `~/.local/bin/nexus-launcher` (never updated again — see Self-Update)
2. Installs the initial `nexus` binary to `$NEXUS_HOME/bin/nexus`
3. Creates `$NEXUS_HOME/` directory structure
4. Registers and starts a user-mode service pointing at `nexus-launcher`:
   - Linux: `systemctl --user enable/start nexus`
   - macOS: `launchctl load ~/Library/LaunchAgents/nexus.plist`
5. Clones each `--source` repo and begins watching it

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

## Resource Addressing

A deployment's address is its source URL with the scheme stripped:

```
https://github.com/myorg/my-system  →  github.com/myorg/my-system
```

Include names form path segments beneath it. Resources are addressed as:

```
github.com/myorg/my-system/db/services/postgres
github.com/myorg/my-system/api/volumes/uploads
github.com/myorg/my-system/api/shared-lib/services/something
```

The URL of the included repo is only used to know what to clone — it plays no role
in addressing. The same repo included twice under different names becomes two
independent deployments at two distinct addresses with separate worktrees and volumes.

---

## Directory Layout

Three top-level trees, each mirroring the same address structure:

```
$NEXUS_HOME/                                         default: ~/.nexus
│
├── bin/
│   └── nexus                                        nexus daemon binary
│
├── nexus.db                                         sqlite: deployment state, service state
│
├── repos/
│   │
│   │   Bare clones are stored at the URL-addressed path and shared across all
│   │   includes of the same URL — they are read-only git object stores.
│   │   Worktrees are created per include-path instance so each deployment gets
│   │   its own isolated working directory regardless of the source URL.
│   │
│   ├── github.com/nexus-community/postgres/
│   │   └── .git/                                    bare clone (shared git objects)
│   │
│   ├── github.com/myorg/api/
│   │   └── .git/                                    bare clone
│   │
│   └── github.com/myorg/my-system/
│       ├── .git/                                    root deployment bare clone
│       ├── worktrees/
│       │   └── <sha>/                               root's worktree
│       ├── db/                                      include named "db" (cloned from postgres URL)
│       │   └── worktrees/
│       │       └── <sha>/                           db's own worktree
│       ├── db-replica/                              same URL, separate worktree
│       │   └── worktrees/
│       │       └── <sha>/
│       └── api/
│           ├── worktrees/
│           │   └── <sha>/
│           └── shared-lib/                          api's own include
│               └── worktrees/
│                   └── <sha>/
│
├── volumes/                                         persistent data, survives re-deployments
│   └── github.com/myorg/my-system/
│       ├── db/
│       │   └── data/                                volume "data" declared in db's nexus.yaml
│       └── api/
│           └── uploads/                             volume "uploads" declared in api's nexus.yaml
│
└── logs/                                            build and service logs
    └── github.com/myorg/my-system/
        ├── <sha>-build.log                          build log for root deployment
        ├── db/
        │   ├── <sha>-build.log
        │   └── services/
        │       └── postgres/
        │           └── current.log                  rotated service log
        └── api/
            ├── <sha>-build.log
            └── services/
                └── api-server/
                    └── current.log
```

Each tree mirrors the address structure — the same path segments, just rooted under
`repos/`, `volumes/`, or `logs/`. Operationally this matters: volumes are the only
thing that must be backed up; repos and logs can be freely wiped and rebuilt.

---

## nexus.yaml Specification

Every managed repo has a `nexus.yaml` at its root. The file declares no name for
itself — its address in the system is determined by where and how it is included.

### Minimal example (aggregator only)

```yaml
# github.com/myorg/my-system — root nexus.yaml, wiring only

includes:
  db:                                         # include name — becomes a path segment
    url: https://github.com/nexus-community/postgres
    ref: "@v15"
  db-replica:                                 # same URL, different name → independent deployment
    url: https://github.com/nexus-community/postgres
    ref: "@v15"
    bind:
      primary: db/services/postgres           # parent-relative: sibling include "db", service "postgres"
  api:
    url: https://github.com/myorg/api
    ref: "@main"
    bind:
      database: db/services/postgres          # same: api's "database" alias → db's postgres service
```

### Full example (repo with services)

```yaml
# github.com/myorg/api — included as "api" by a parent

# Pull in further repos as independently managed sub-deployments.
includes:
  shared-lib:
    url: https://github.com/myorg/shared-lib
    ref: "@main"

# Runs once inside the new worktree before any services are started.
# Non-zero exit aborts the deployment; currently running services are untouched.
build: pip install -e . && alembic upgrade head

# Persistent directories. Survive across deployments.
# Exposed as $NEXUS_VOLUME_<NAME> (uppercased). Data lives at
# $NEXUS_HOME/<address-of-this-deployment>/volumes/<name>/
volumes:
  uploads: {}

# Named long-running processes. Key is the service name.
services:
  api-server:
    run: uvicorn app:main --host 0.0.0.0 --port 8080
    depends_on:
      - database        # abstract alias — resolved by bind: at the include site

  api-worker:
    run: celery -A app.tasks worker --concurrency 4
    depends_on:
      - database
      - api-server      # sibling service — bare name resolves within this deployment
```

### Community/reusable example

```yaml
# github.com/nexus-community/postgres — nexus.yaml
# Published as a reusable, includable deployment.
# No knowledge of what it will be named or where it will be included.

build: ./scripts/init.sh   # initialises cluster if data volume is empty

volumes:
  data: {}

services:
  postgres:
    run: postgres -D $NEXUS_VOLUME_DATA -c listen_addresses='*' -p 5432
```

The community repo declares no names for itself. The parent's `includes:` block
assigns the name, which becomes the path segment in all resource addresses.

---

### Field Reference

#### `includes` (map)

Key: the include name. Must not be a reserved type segment. Becomes a path segment
in the address of every resource in that deployment and its descendants.

| Field | Required | Description |
|---|---|---|
| `url` | yes | Git-cloneable URL. Only used to know what to clone — plays no role in addressing |
| `ref` | no | Ref to track, prefixed with `@`. Defaults to `@main`. See ref syntax below |
| `bind` | no | Map of `alias: <path>` resolving dependency aliases declared in that repo's services. Path is relative to the nexus.yaml that declares the `bind:` — i.e. the parent. `db/services/postgres` means: sibling include named `db`, service `postgres` |

**Ref syntax:**

| Value | Behaviour |
|---|---|
| `@main` | Track the tip of branch `main`. Redeploys on every new commit |
| `@v15` | Pin to tag `v15`. Redeploys only if the tag is moved (rare) |
| `@latest` | Track the highest semver tag. Nexus uses `git ls-remote --tags --sort=-version:refname` and takes the top result. Redeploys when a new tag sorts higher |

#### `volumes` (map)

Key: the volume name. Exposed as `$NEXUS_VOLUME_<NAME>` (uppercased) in build and
service commands. Stored at `$NEXUS_HOME/volumes/<include-path>/<name>/`.
Currently no sub-fields; value is an empty map `{}`.

#### `services` (map)

Key: the service name. Must not be a reserved type segment (`services`, `volumes`, `flows`).
Addressed externally as `<include-path>/services/<name>`.

| Field | Required | Description |
|---|---|---|
| `run` | yes | Shell command. Spawned with `sh -c` from the worktree root |
| `depends_on` | no | List of service names this service needs before it starts. See Dependency Resolution |

---

## Naming Rules

**Reserved segments**: `services`, `volumes`, `flows`.

These are the path segments that separate resource types from resource names in an address.
They must not be used as service names, volume names, or include names.

```
github.com/myorg/my-system/db/services/postgres    valid: include "db", service "postgres"
github.com/myorg/my-system/services/api            valid: root-level service "api"
github.com/myorg/my-system/services/services       INVALID — service name "services" is reserved
github.com/myorg/my-system/volumes/services        INVALID — volume name "services" is reserved
```

An include cannot be named `services`, `volumes`, or `flows` for the same reason — it
would make the address ambiguous at that level.

Nexus validates all names at startup and rejects any `nexus.yaml` that uses a
reserved segment. Adding a new resource type in the future adds one new reserved word.

---

## Dependency Resolution

`depends_on` entries are resolved in this order:

1. **Bare name** — no `/` in the name → sibling service in the same `nexus.yaml`, resolved directly
2. **Bound alias** — name matches a key in the `bind:` map provided by the parent that includes this deployment → resolved to the bound path
3. **Root-relative path** — `<include-name>/.../services/<service-name>`, resolved from the root of the tree → always unambiguous

`bind:` paths are relative to the nexus.yaml that declares them — the parent of the
included deployment. `db/services/postgres` in a root-level `bind:` means: the include
named `db` under the root, then the service `postgres` within it.

`depends_on` explicit paths (those with `/`) are also parent-relative — they resolve
from the same nexus.yaml that lists the service.

There is no short or partial form for cross-deployment references. Either use a
parent-relative path or declare a `bind:` alias at the include site and use the alias
as a bare name.

If a name cannot be resolved, nexus fails at startup with a clear error identifying
the unresolved dependency and the deployment that declared it.

Circular dependencies cause a startup error.

### What depends_on does

- **Startup ordering**: dependency services are started before this service, and
  stopped after this service during shutdown (reverse order)
- **Nothing else**: if a dependency crashes and is restarted by the supervisor, or
  if a dependency's deployment is updated, dependent services are not touched.
  Services are expected to handle reconnection on their own.

---

## Environment Injected into Build and Services

```sh
NEXUS_HOME=<path>
NEXUS_ADDRESS=<include-path of this deployment, e.g. github.com/myorg/my-system/db>
NEXUS_SHA=<full-commit-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_VOLUME_<NAME>=<absolute-path>    # one per declared volume; resolves to $NEXUS_HOME/volumes/<include-path>/<name>
```

---

## Deployment Lifecycle

### Triggering

Nexus polls each repo on a 30-second interval using `git ls-remote`, which is a
lightweight ref-listing operation that downloads no objects:

```sh
# branch
git ls-remote origin refs/heads/main

# tag
git ls-remote origin refs/tags/v15

# @latest — all tags sorted by version, take the highest
git ls-remote --tags --sort=-version:refname origin 'refs/tags/*'
```

When the resolved SHA differs from the last recorded SHA for that deployment,
a new deployment is enqueued.

**Independent polling**: each deployment (each `nexus.yaml`) polls its own ref
independently. An include is only redeployed when its own ref changes — not
when its parent or a sibling is redeployed.

**Queuing rule**: at most one pending SHA per deployment is held. If a new commit
arrives while a build is in progress, it replaces any previously queued SHA. Nexus
always converges to the latest commit without processing every intermediate one.

### Sequence

```
1. DETECT
   └── New SHA queued for repo

2. CHECKOUT
   └── git worktree add $NEXUS_HOME/repos/<include-path>/worktrees/<sha> <sha>
       (git reads objects from the URL-addressed bare clone; worktree is written
        at the include-path so each deployment instance has its own directory)

3. BUILD  (inside the new worktree)
   ├── sh -c "<build command>"   (skipped if build is not declared)
   ├── stdout/stderr → $NEXUS_HOME/logs/<include-path>/<sha>-build.log
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

The OS service unit points at `nexus-launcher`. The launcher is a minimal shell
script that exec's `$NEXUS_HOME/bin/nexus`. It never changes.

### Self-update flow

The root nexus.yaml includes nexus itself as a named deployment:

```yaml
includes:
  nexus:
    url: https://github.com/rdkal/nexus
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

1. **BUILD**: new binary compiled into `nexus.next`, atomically moved to `nexus`.
   The old binary is already in memory — replacing the file has no effect on the running process.
2. **SHUTDOWN**: nexus sends SIGTERM to its own `nexus-daemon` service (itself).
   It writes all pending state to `nexus.db` and exits.
3. The OS init system sees the process exit and restarts `nexus-launcher`, which
   exec's `$NEXUS_HOME/bin/nexus` — now the new binary.
4. **STARTUP**: new nexus reads `nexus.db`, reconstructs the full service tree,
   and resumes supervision of all other services.

The key point: nexus does **not** try to spawn `nexus-daemon` as a child process.
The OS service unit owns the restart. Nexus recognises it is managing itself and
skips the STARTUP step for `nexus-daemon`, leaving it entirely to the init system.

**Key invariant**: all state lives in `nexus.db` and the filesystem. The new
process reconstructs everything from disk with no handshake with the old one.
Other services keep running through the brief daemon restart.

---

## Web UI

Nexus serves a minimal HTTP UI on a configurable port (default `7777`).
HTTP only, no authentication — intended for private network use.

The UI URL scheme mirrors the resource address tree directly.

### Pages

| Page | Content |
|---|---|
| `/` | Full deployment tree, current SHA per deployment, service health |
| `/<include-path>` | Deployment detail: history, current SHA, build log |
| `/<include-path>/services/<name>` | Service status, restart count, live log tail |
| `/<include-path>/volumes` | Volumes declared in this deployment, disk usage |

Examples:
```
/github.com/myorg/my-system/db
/github.com/myorg/my-system/db/services/postgres
/github.com/myorg/my-system/api/services/api-server
```

### API

```
GET  /api/<include-path>
GET  /api/<include-path>/history
POST /api/<include-path>/redeploy      re-run build + restart services at current SHA
GET  /api/<include-path>/services
GET  /api/<include-path>/services/<name>
POST /api/<include-path>/services/<name>/restart
```

---

## v1 Scope

**In scope:**
- Install script (`curl … | sh`)
- `nexus.yaml` with `includes`, `build`, `volumes`, `services`, `depends_on`, `bind`
- Include-path-based addressing; volumes and logs namespaced by include-path
- Bare clones at URL-path, worktrees shared across instances at the same SHA
- Git polling via `git ls-remote` (30s), worktree-based deployments
- Build → SIGTERM old → start new lifecycle with rollback
- Dependency-ordered startup and shutdown (no restart propagation)
- Commit queuing (latest-wins, depth 1)
- Process supervision with restart backoff and degraded state
- Daemon-wide 30s shutdown grace period
- Self-update via thin launcher
- `NEXUS_HOME` configuration
- Web UI at resource addresses (read-only + manual redeploy trigger)
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

1. **Cross-deployment volume paths**: a service in one deployment sometimes needs the
   volume path of a service in another deployment (e.g. api needs to know where postgres
   wrote its socket or env file). The `bind:` mechanism currently only resolves service
   names for `depends_on`. Extending it to also wire volume paths would solve this:
   `bind: { db-data: db/volumes/data }` → injects `$NEXUS_BIND_DB_DATA=<path>`.
   Deferred to v2; v1 services coordinate via well-known host paths or out-of-band env vars.

2. **`@latest` tie-breaking**: non-semver tag names that sort equally under
   `version:refname` fall back to tag creation date. Decided — no further action needed.
