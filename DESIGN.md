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

## Deployment Identity

A deployment's identity is the **URL path to the directory containing its `nexus.yaml`**,
with the scheme stripped and `.git` suffix removed. This mirrors how Go resolved packages
before `go.mod`: the import path was the URL, the local path mirrored the URL under
`$GOPATH/src/`.

```
https://github.com/myorg/api      →  github.com/myorg/api
git@github.com:myorg/api.git      →  github.com/myorg/api  (normalised)
https://gitlab.com/myorg/api      →  gitlab.com/myorg/api
```

No `name` or `project` field anywhere. The identity comes from where the code lives.

**Short name**: the last path segment of the URL (`api` from `github.com/myorg/api`).
Used when addressing services in contexts where the short name is unambiguous.

Services are addressed as `<short-name>.<service-name>` or, where disambiguation is
needed, `<full-url-path>.<service-name>`:

```
github.com/nexus-community/postgres.postgres   full form
postgres.postgres                              short form (unambiguous in most trees)
```

---

## Directory Layout

The repos and volumes directories mirror the URL structure, the same way `$GOPATH/src`
mirrored import paths. Nesting depth in the include tree has no effect on the directory
structure — each deployment lives at its URL path regardless of who includes it.

```
$NEXUS_HOME/                               default: ~/.nexus
│
├── bin/
│   └── nexus                              nexus daemon binary (updated by deployments)
│
├── repos/
│   └── github.com/
│       ├── myorg/
│       │   ├── my-system/                 root deployment (pointed at by install --source)
│       │   │   ├── .git/                  bare clone
│       │   │   └── worktrees/
│       │   │       └── <sha>/             one checkout per deployment attempt
│       │   └── api/
│       │       ├── .git/
│       │       └── worktrees/
│       │           └── <sha>/
│       └── nexus-community/
│           └── postgres/
│               ├── .git/
│               └── worktrees/
│                   └── <sha>/
│
├── volumes/
│   └── github.com/
│       ├── myorg/
│       │   └── api/
│       │       └── uploads/               volume "uploads" declared in api's nexus.yaml
│       └── nexus-community/
│           └── postgres/
│               └── data/                  volume "data" declared in postgres's nexus.yaml
│
├── nexus.db                               sqlite: repos, deployments, services, run history
│
└── logs/
    └── github.com/
        └── myorg/
            └── api/
                ├── <sha>-build.log
                └── <sha>-<service-name>.log
```

**Volume namespacing**: volumes are stored at `$NEXUS_HOME/volumes/<url-path>/<name>`.
Two deployments at different URLs can both declare a volume named `data` without
any collision. The `name` in `nexus.yaml` is just a label; nexus resolves it to
the full namespaced path.

---

## nexus.yaml Specification

Every managed repo has a `nexus.yaml` at its root. The file has no name or identity
declaration — identity comes entirely from the URL of the directory containing it.

### Minimal example (aggregator only)

```yaml
# github.com/myorg/my-system — root nexus.yaml, wiring only

includes:
  - url: https://github.com/nexus-community/postgres
    ref: "@v15"
  - url: https://github.com/myorg/api
    ref: "@main"
    bind:
      database: postgres/services/postgres   # short-name/services/<name>, unambiguous here
```

### Full example (repo with services)

```yaml
# github.com/myorg/api — api/nexus.yaml

# Optional: pull in further repos as independently managed deployments.
includes:
  - url: https://github.com/myorg/shared-lib
    ref: "@main"

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
| `url` | yes | Git-cloneable URL. Scheme-stripped, `.git`-removed form is the deployment's identity |
| `ref` | no | Ref to track, prefixed with `@`. Defaults to `@main`. See ref syntax below |
| `bind` | no | Map of `alias: <address>` resolving dependency aliases declared in that repo's services. Address is `<short-name>/services/<service>` or the full `<url-path>/services/<service>` form |

**Ref syntax:**

| Value | Behaviour |
|---|---|
| `@main` | Track the tip of branch `main`. Redeploys on every new commit |
| `@v15` | Pin to tag `v15`. Redeploys only if the tag is moved (rare) |
| `@latest` | Track the highest semver tag. Nexus checks with `git ls-remote --tags --sort=-version:refname` and takes the top result. Redeploys when a new tag sorts higher |

#### `volumes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Identifier. Exposed as `$NEXUS_VOLUME_<NAME>` (uppercased). Stored at `$NEXUS_HOME/volumes/<url-path>/<name>` |

#### `services[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique within this deployment. Addressed externally as `<url-path>/services/<name>` |
| `run` | yes | Shell command. Spawned with `sh -c` from the worktree root |
| `depends_on` | no | List of service names this service needs before it can start. See Dependency Resolution |

---

## Resource Naming Rules

Resource names (service names, volume names, and future flow names) must not be any
of the reserved type namespace segments: `services`, `volumes`, `flows`.

This guarantees that any path under a deployment URL is unambiguously parseable:

```
github.com/rdkal/my-project/services          → the services namespace
github.com/rdkal/my-project/services/api      → service named "api"
github.com/rdkal/my-project/volumes/data      → volume named "data"
github.com/rdkal/my-project/services/volumes  → INVALID — "volumes" is reserved
github.com/rdkal/my-project/volumes/flows     → INVALID — "flows" is reserved
```

Nexus validates resource names at startup and rejects any `nexus.yaml` that uses a
reserved name. Adding a new resource type in the future simply adds a new reserved word.

---

## Dependency Resolution

Full service addresses follow the resource path convention:

```
github.com/nexus-community/postgres/services/postgres   full form, always unambiguous
postgres/services/postgres                               short form (last URL segment)
```

`depends_on` entries are resolved in this order:

1. **Sibling service** — bare name with no `/` matches a service in the same `nexus.yaml` → resolved directly
2. **Bound alias** — name matches a key in the `bind:` map provided by the parent that includes this repo → resolves to the bound full address
3. **Short-name address** — `<repo-short-name>/services/<service>` where the short name is the last URL segment of any deployment in the tree → resolved if unambiguous
4. **Full address** — `<url-path>/services/<service>` → always unambiguous

If resolution is ambiguous (two deployments share the same last URL segment), nexus
fails at startup with a clear error and requires the full URL-path form.

If a name cannot be resolved at all, nexus fails at startup with a clear error identifying
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
NEXUS_URL=<url-path identity, e.g. github.com/myorg/api>
NEXUS_SHA=<full-commit-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_VOLUME_<NAME>=<absolute-path>    # one per declared volume; resolves to $NEXUS_HOME/volumes/<url-path>/<name>
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
   └── git worktree add $NEXUS_HOME/repos/<url-path>/worktrees/<sha> <sha>

3. BUILD  (inside the new worktree)
   ├── sh -c "<build command>"   (skipped if build is not declared)
   ├── stdout/stderr → $NEXUS_HOME/logs/<url-path>/<sha>-build.log
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

The root nexus.yaml (or a dedicated include) manages nexus itself:

```yaml
includes:
  - url: https://github.com/rdkal/nexus
    ref: "@main"
```

`github.com/rdkal/nexus` — nexus.yaml:
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
certificates) do so through volumes — each service reads from or writes to its
own volume, and a consumer service uses a well-known convention to find the path.

Example: postgres writes a connection env file into its data volume. The api reads
it by sourcing the file at the path nexus injects via `$NEXUS_VOLUME_DATA`.

```yaml
# github.com/nexus-community/postgres — nexus.yaml
volumes:
  - name: data
services:
  - name: postgres
    run: |
      echo "DATABASE_URL=postgresql://localhost:5432/app" > $NEXUS_VOLUME_DATA/db.env
      postgres -D $NEXUS_VOLUME_DATA
```

```yaml
# github.com/myorg/api — nexus.yaml
services:
  - name: api-server
    run: |
      . /path/to/postgres/data/db.env   # absolute path agreed by convention
      uvicorn app:main --port 8080
    depends_on:
      - database
```

**Cross-volume path sharing is unresolved in v1.** The consumer needs the absolute
path to the postgres volume (`$NEXUS_HOME/volumes/github.com/nexus-community/postgres/data`),
but nexus does not currently inject another deployment's volume paths. Options for v2:
extend `bind:` to also wire volume paths, or add an explicit volume-export/import mechanism.
For now, services can agree on a path via environment variables set in the host environment.

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
- Cross-deployment shared volume injection (the `$NEXUS_VOLUME_DATABASE` example above)
- Multi-machine execution
- Windows support

---

## Open Questions

1. **Shared volume injection**: the cleanest way for services to share volume paths
   across repos is through bound env var injection at the include site. The `bind:`
   mechanism today only handles `depends_on` resolution. Should it also wire volume
   paths? Or is a conventions-based approach (agreed absolute paths) sufficient for v1?

2. **`@latest` tie-breaking**: when two tags sort equally under `version:refname`
   (e.g. non-semver tag names), which wins? Proposal: fall back to tag creation date.
