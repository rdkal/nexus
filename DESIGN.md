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
| **Service** | The smallest deployable atom. A named, long-running process that nexus starts, supervises, and stops |
| **Repo** | A git repository with a `nexus.yaml`. Provides a build context (build command, volumes, worktree) for one or more services |
| **Include** | A reference to another repo pulled into the deployment tree, given a local name at the include site |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA. One active worktree per repo |
| **Volume** | A directory outside all worktrees that persists across deployments |
| **Bind** | A wiring declaration at the include site that resolves a service's abstract dependency name to a concrete service in the tree |

The key distinction: **repos are versioning and build units; services are runtime units.**
Dependencies are between services, not between repos.

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --source https://github.com/myorg/my-system
```

The install script:

1. Installs `nexus-launcher` to `~/.local/bin/nexus-launcher` (never updated again — see Self-Update)
2. Installs the initial `nexus` binary to `$NEXUS_HOME/bin/nexus`
3. Creates `$NEXUS_HOME/` directory structure
4. Registers and starts a user-mode service pointing at `nexus-launcher`:
   - Linux: `systemctl --user enable/start nexus`
   - macOS: `launchctl load ~/Library/LaunchAgents/nexus.plist`
5. Clones the `--source` repo as the root of the deployment tree

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

## Directory Layout

```
$NEXUS_HOME/                               default: ~/.nexus
│
├── bin/
│   └── nexus                              nexus daemon binary (updated by deployments)
│
├── repos/
│   ├── root/
│   │   ├── .git/                          bare clone of root repo
│   │   └── worktrees/
│   │       └── <sha>/                     checked-out worktree for each deployment attempt
│   │
│   ├── <include-name>/                    top-level include, e.g. "api"
│   │   ├── .git/
│   │   └── worktrees/
│   │       └── <sha>/
│   │
│   └── <include-name>.<child-name>/       nested include, dot-joined slug, e.g. "api.shared-lib"
│       ├── .git/
│       └── worktrees/
│           └── <sha>/
│
├── volumes/
│   ├── root/
│   │   └── <volume-name>/                 volumes declared in root nexus.yaml
│   └── <slug>/
│       └── <volume-name>/                 volumes declared in that include's nexus.yaml
│                                          auto-namespaced by slug so the same repo can be
│                                          included twice under different names without conflict
│
├── nexus.db                               sqlite: repos, deployments, services, run history
│
└── logs/
    └── <slug>/
        ├── <sha>-build.log
        └── <sha>-<service-name>.log
```

**Slug rules:**
- Root repo slug: `root`
- Top-level include named `api`: slug is `api`
- An include named `lib` inside `api`: slug is `api.lib`
- Slugs are dot-joined paths from root, matching the `includes` nesting

**Volume namespacing:**
Volumes are automatically placed under `$NEXUS_HOME/volumes/<slug>/`. A community
postgres repo that declares a volume named `data` gets stored at
`$NEXUS_HOME/volumes/db/data` when included as `db`, and at
`$NEXUS_HOME/volumes/db-replica/data` when included again as `db-replica`.
There is no collision. The path in `nexus.yaml` is treated as a name, not an
absolute path — nexus always resolves it to the namespaced location.

---

## nexus.yaml Specification

Every managed repo has a `nexus.yaml` at its root. There is no `name` field —
names come from the include site. The root deployment has no name (it is the root).

### Minimal example (aggregator only)

```yaml
# root nexus.yaml — no services, just wiring
includes:
  - name: db
    url: https://github.com/nexus-community/postgres
    ref: v15
  - name: api
    url: https://github.com/myorg/api
    ref: main
    bind:
      database: db.postgres
```

### Full example (repo with services)

```yaml
# api/nexus.yaml

# Optional: pull in further repos this service depends on at the build level.
# Runtime service dependencies are declared in services[].depends_on instead.
includes:
  - name: shared-lib
    url: https://github.com/myorg/shared-lib
    ref: main

# Runs once inside the new worktree before any services are started.
# Non-zero exit aborts the deployment; currently running services are untouched.
build: pip install -e . && alembic upgrade head

# Persistent directories. Survive across deployments.
# Namespaced automatically under $NEXUS_HOME/volumes/<slug>/<name>/.
# Exposed to build and service commands as $NEXUS_VOLUME_<NAME> (uppercased).
volumes:
  - name: uploads

# Named long-running processes.
services:
  - name: api-server
    run: uvicorn app:main --host 0.0.0.0 --port 8080
    depends_on:
      - database        # abstract alias — resolved by bind: at the include site

  - name: api-worker
    run: celery -A app.tasks worker --concurrency 4
    depends_on:
      - database
      - api-server      # sibling service — bare name resolves within this repo
```

### Community/reusable example

```yaml
# nexus-community/postgres nexus.yaml
# Published as a reusable, includable deployment.

build: ./scripts/init.sh   # initialises cluster if data volume is empty

volumes:
  - name: data             # stored at $NEXUS_HOME/volumes/<slug>/data

services:
  - name: postgres
    run: postgres -D $NEXUS_VOLUME_DATA -c listen_addresses='*' -p 5432
```

No hardcoded names, no knowledge of who includes it. The include site names it.

---

### Field Reference

#### `includes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Local name for this include. Forms the slug segment. Used to address its services |
| `url` | yes | Git-cloneable URL |
| `ref` | no | Branch, tag, or SHA. Defaults to `main` |
| `bind` | no | Map of `alias: <slug>.<service>` that resolves dependency aliases declared in that repo's services |

#### `volumes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Identifier. Exposed as `$NEXUS_VOLUME_<NAME>` (uppercased). Stored at `$NEXUS_HOME/volumes/<slug>/<name>` |

#### `services[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique within this repo. Addressed externally as `<slug>.<name>` |
| `run` | yes | Shell command. Spawned with `sh -c` from the worktree root |
| `depends_on` | no | List of service names this service needs before it can start. See Dependency Resolution |

---

## Dependency Resolution

`depends_on` entries are resolved in this order:

1. **Sibling service** — bare name matches a service in the same `nexus.yaml` → resolved directly
2. **Bound alias** — name matches a key in the `bind:` map declared at this repo's include site → resolves to the bound service
3. **Absolute reference** — name contains a `.` → treated as `<slug>.<service>` and looked up globally

If a name cannot be resolved, nexus fails at startup with a clear error identifying
the unresolved dependency and the include that declared it.

Circular dependencies cause a startup error.

### What depends_on does

- **Startup ordering**: dependency services are started before this service
- **Restart propagation**: when a dependency is redeployed (new commit lands), this
  service is also restarted after the dependency's new version is healthy
- **Crash handling**: if a dependency crashes and restarts (supervisor backoff), this
  service is *not* automatically restarted — it is expected to reconnect on its own

---

## Environment Injected into Build and Services

```sh
NEXUS_HOME=<path>
NEXUS_SLUG=<dot-joined include path, e.g. api or api.lib>
NEXUS_SHA=<full-commit-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_VOLUME_<NAME>=<absolute-path>    # one per declared volume, name uppercased
```

---

## Deployment Lifecycle

### Triggering

Nexus polls `git fetch origin <ref>` on a 30-second interval per repo.
When the fetched SHA differs from the last recorded SHA, a deployment is enqueued.

**Queuing rule**: at most one pending SHA per repo is held. If a new commit arrives
while a build is in progress, it replaces any previously queued SHA. Nexus always
converges to the latest commit without processing every intermediate one.

### Sequence

```
1. DETECT
   └── New SHA queued for repo

2. CHECKOUT
   └── git worktree add $NEXUS_HOME/repos/<slug>/worktrees/<sha> <sha>

3. BUILD  (inside the new worktree)
   ├── sh -c "<build command>"   (skipped if build is not declared)
   ├── stdout/stderr → $NEXUS_HOME/logs/<slug>/<sha>-build.log
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
   ├── Record new SHA as active in nexus.db
   └── Restart-propagation: enqueue a deployment for every service
       that declares depends_on pointing at a service in this repo

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

The root nexus.yaml (or a dedicated include) manages nexus itself:

```yaml
includes:
  - name: nexus-core
    url: https://github.com/rdkal/nexus
    ref: main
```

`nexus-core/nexus.yaml`:
```yaml
build: ./scripts/build.sh   # compiles/downloads new binary to $NEXUS_HOME/bin/nexus.next
services:
  - name: nexus-daemon
    run: $NEXUS_HOME/bin/nexus daemon
```

When a new nexus commit lands:

1. **BUILD**: new binary is written to `$NEXUS_HOME/bin/nexus.next`
   (old running binary is untouched — it's already exec'd into memory)
2. **SWAP**: build script atomically moves `nexus.next` → `nexus`
3. **SHUTDOWN**: nexus daemon receives SIGTERM on its own `nexus-daemon` service.
   It writes all pending state to `nexus.db` and exits cleanly.
4. The OS init system restarts `nexus-launcher`, which exec's the new binary.
5. **STARTUP**: new nexus reads `nexus.db`, reconstructs the full service tree,
   and resumes supervision of all other services.

**Key invariant**: all nexus state lives in `nexus.db` and the filesystem. The
new process reconstructs everything from disk with no handshake with the old one.
The gap between SIGTERM and the new process being ready is the only downtime —
other services continue running, unaware of the brief daemon restart.

---

## Cross-Service Coordination (Volumes)

Services that need to share runtime information (connection strings, sockets,
certificates) do so through **shared volumes** rather than through nexus itself.

Example: postgres writes a `.env` file to its volume; api reads it on startup.

```yaml
# nexus-community/postgres nexus.yaml
volumes:
  - name: data
services:
  - name: postgres
    run: |
      echo "DATABASE_URL=postgresql://localhost:5432/app" > $NEXUS_VOLUME_DATA/db.env
      postgres -D $NEXUS_VOLUME_DATA ...
```

```yaml
# api/nexus.yaml
services:
  - name: api-server
    run: |
      . $NEXUS_VOLUME_DATABASE/db.env
      uvicorn app:main --port 8080
    depends_on:
      - database
```

The `$NEXUS_VOLUME_DATABASE` variable is injected because the include site declares
a binding, and nexus injects the bound service's volume path. (Exact mechanism TBD
in implementation — for v1 the simpler path is an agreed absolute path convention.)

---

## Web UI

Nexus serves a minimal HTTP UI on a configurable port (default `7777`).
HTTP only, no authentication — intended for private network use.

### Pages

| Page | Content |
|---|---|
| `/` | All repos and services, current SHA, health at a glance |
| `/repos/<slug>` | Deployment history, build log per SHA |
| `/repos/<slug>/services/<name>` | Service status, restart count, live log tail |
| `/volumes` | All volumes, path, disk usage |

### API

```
GET  /api/repos
GET  /api/repos/<slug>
GET  /api/repos/<slug>/history
POST /api/repos/<slug>/redeploy     re-run build + restart services at current SHA
GET  /api/services
POST /api/services/<id>/restart
```

---

## v1 Scope

**In scope:**
- Install script (`curl … | sh`)
- `nexus.yaml` with `includes`, `build`, `volumes`, `services`, `depends_on`, `bind`
- Volume auto-namespacing by slug
- Git polling (30s), worktree-based deployments
- Build → SIGTERM old → start new lifecycle with rollback
- Dependency-ordered startup and shutdown
- Restart propagation on redeployment (not on crash)
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
- Cross-deployment shared volume injection (the `$NEXUS_VOLUME_DATABASE` example above)
- Multi-machine execution
- Windows support

---

## Open Questions

1. **Shared volume injection**: the cleanest way for services to share volume paths
   across repos is through bound env var injection at the include site. The `bind:`
   mechanism today only handles `depends_on` resolution. Should it also wire volume
   paths? Or is a conventions-based approach (agreed absolute paths) sufficient for v1?

2. **Grace period per service**: daemon-wide 30s for v1. Worth making per-service in v2.

3. **Root repo identity**: the root repo has slug `root`. Should this be configurable
   so the UI label is more meaningful?
