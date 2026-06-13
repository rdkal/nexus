# Nexus — Design Document

## Overview

Nexus is a git-native process manager and deployment system. It turns a collection
of git repositories into a self-managing, continuously-deployed system running
directly on the host — no containers, no cloud primitives. The only delivery
mechanism is git; the only configuration format is YAML.

A single `curl` invocation installs the daemon. From that point, everything —
including updates to nexus itself — is driven by commits.

---

## Core Concepts

| Concept | Description |
|---|---|
| **Repo** | A git repository with a `nexus.yaml` at its root |
| **Flow** | A named, triggerable sequence of tasks |
| **Task** | A single shell command inside a flow |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA |
| **Volume** | A directory that lives outside worktrees and persists across deployments |
| **Service** | A long-running process started by a startup task that nexus supervises |
| **Trigger** | A string that causes a flow to execute |

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --source https://github.com/myorg/my-system
```

The install script:

1. Installs the `nexus` binary to `/usr/local/bin/nexus`
2. Creates `/var/nexus/` for runtime state
3. Registers and starts a system service (`systemd` on Linux, `launchd` on macOS)
4. Clones the given `--source` repo as the **root repo**

The root repo's `nexus.yaml` is the entry point for the entire system.

After installation, nexus manages itself: updating the install script repo is all
that is needed to update nexus.

---

## Directory Layout (runtime)

```
/var/nexus/
  repos/
    <repo-slug>/
      .git/          ← bare clone
      worktrees/
        <sha>/       ← checked-out worktree per deployment
  volumes/
    <name>/          ← persistent, survives deployments
  current -> /var/nexus/repos/<repo-slug>/worktrees/<sha>  (per repo)
  nexus.db           ← sqlite: runs, services, state
  logs/
    <repo-slug>/
      <sha>.log
```

---

## nexus.yaml Specification

The format is shared by all repos — both the root repo and every managed repo.

```yaml
# nexus.yaml

name: my-system          # human label, used in the UI and logs

# Optional: pull in other repos. Each must have its own nexus.yaml.
repos:
  - url: https://github.com/myorg/api-service
    ref: main            # branch, tag, or SHA
  - url: https://github.com/myorg/worker
    ref: main

# Persistent directories, shared across deployments of this repo.
# Accessible inside tasks as $NEXUS_VOLUME_<NAME> (uppercased).
volumes:
  - name: postgres-data
    path: /var/nexus/volumes/postgres-data
  - name: config
    path: /var/nexus/volumes/config

# Flows: the unit of work in nexus.
flows:
  - name: build
    on: commit           # trigger — runs on every new commit
    tasks:
      - name: install-deps
        run: pip install -e .
      - name: run-tests
        run: pytest -x

  - name: startup
    on: build.success    # runs when the 'build' flow succeeds
    tasks:
      - name: migrate
        run: alembic upgrade head
      - name: serve
        run: uvicorn app:main --host 0.0.0.0 --port 8080
        mode: service    # nexus supervises this process

  - name: shutdown
    # no 'on' — nexus calls this explicitly before replacing a deployment
    tasks:
      - name: drain
        run: ./scripts/drain.sh
        timeout: 30s

  - name: nightly-cleanup
    on: "cron:0 2 * * *"
    tasks:
      - name: purge
        run: ./scripts/cleanup.sh
```

### Field Reference

#### `repos[]`

| Field | Required | Description |
|---|---|---|
| `url` | yes | Git-cloneable URL |
| `ref` | no | Branch, tag, or SHA. Defaults to `main` |

#### `volumes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Identifier. Exposed as `$NEXUS_VOLUME_<NAME>` in tasks |
| `path` | yes | Absolute path on host. Created if it does not exist |

#### `flows[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique within the repo |
| `on` | no | Trigger string. Absent means manually/explicitly invoked |
| `tasks` | yes | Ordered list of tasks |

#### `tasks[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Label for UI and logs |
| `run` | yes | Shell command. Runs with `sh -c` in the worktree directory |
| `mode` | no | `task` (default, exit-to-complete) or `service` (supervised, restarts on crash) |
| `timeout` | no | Max duration before the task is killed. e.g. `30s`, `5m` |
| `env` | no | Map of additional environment variables |

---

## Trigger Types

Triggers are plain strings on the `on` field. Nexus parses them by prefix:

| Trigger | Meaning |
|---|---|
| `commit` | New commit detected on the watched ref |
| `cron:<expr>` | Standard 5-field cron expression |
| `<flow-name>.success` | Another flow in this repo completed successfully |
| `<flow-name>.failure` | Another flow in this repo failed |
| `event:<name>` | An external event posted to the nexus HTTP API |
| *(absent)* | Flow is only run by explicit invocation (UI or API) |

---

## Deployment Lifecycle

This is the sequence nexus follows when a new commit is detected on a watched repo:

```
1. DETECT
   ├── Poll `git fetch origin <ref>` on an interval (default: 30s)
   └── Compare fetched SHA against last-known SHA

2. CHECKOUT
   └── git worktree add /var/nexus/repos/<slug>/worktrees/<new-sha> <new-sha>

3. BUILD  (run inside the new worktree)
   ├── Execute tasks in the flow triggered by `commit`
   ├── On failure → remove the failed worktree, keep current deployment running
   └── On success → proceed to SWAP

4. SWAP
   ├── 4a. SHUTDOWN  (run against the OLD worktree / current deployment)
   │       Execute tasks in the flow named `shutdown` (if defined)
   │       Send SIGTERM to all supervised services from the old deployment
   │       Wait for graceful exit (timeout configurable, default 30s), then SIGKILL
   │
   ├── 4b. STARTUP   (run inside the new worktree)
   │       Execute tasks in the flow triggered by `build.success`
   │       `mode: service` tasks are registered as supervised processes
   │
   └── 4c. COMMIT
           Update `current` symlink to new worktree
           Record new SHA as the active deployment in nexus.db

5. CLEANUP
   └── Remove the old worktree: git worktree remove <old-sha>
```

### Rollback

If the STARTUP step fails (any task exits non-zero, or a service crashes immediately):

- Nexus re-runs the `startup` flow of the **previous** SHA (worktree is still present)
- Updates `current` back to the old SHA
- Marks the new SHA as `failed` in nexus.db
- The failed worktree is kept until the next successful deployment (for log inspection)

---

## Volume Environment Injection

Inside every task execution, nexus injects:

```sh
NEXUS_VOLUME_<NAME>=<path>    # one per declared volume
NEXUS_REPO=<name>
NEXUS_SHA=<full-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_CURRENT=<absolute-path-to-current-worktree>
```

---

## Process Supervision

Tasks with `mode: service` are long-running processes that nexus owns:

- Nexus spawns them and tracks the PID
- On crash, nexus restarts with exponential backoff (1s, 2s, 4s … cap 60s)
- During SHUTDOWN, nexus sends `SIGTERM` then `SIGKILL` after timeout
- A service that crashes more than 5 times in 60 seconds is marked `failing` and
  is not restarted automatically; an alert appears in the UI

Port binding is not managed by nexus directly — services bind their own ports.
The UI displays service process metadata (PID, uptime, restart count) but does
not proxy traffic.

---

## Execution Backend (Prefect)

Nexus currently delegates flow execution to **Prefect**. The mapping is direct:

| Nexus | Prefect |
|---|---|
| Flow | `@flow` decorated function |
| Task | `@task` decorated function |
| Flow run | Prefect flow run |
| Flow run log | Prefect task logs |

Nexus generates Prefect flows dynamically from `nexus.yaml` at startup and when
configs change. The Prefect server runs embedded within the nexus process (no
separate deployment required).

The execution backend is behind an interface (`nexus.executor.Executor`) so it
can be replaced in the future without changing the YAML schema or lifecycle logic.

```
nexus.executor.Executor  (abstract)
  ├── nexus.executor.prefect.PrefectExecutor   ← current
  └── nexus.executor.local.LocalExecutor       ← planned fallback (no Prefect dep)
```

The `LocalExecutor` runs tasks as subprocesses directly and is used for
development and lightweight installs where Prefect is not desired.

---

## Web UI

Nexus serves a minimal HTTP UI (no auth, no TLS) on a configurable port
(default `7777`).

### Pages

| Page | Content |
|---|---|
| `/` | Dashboard: active repos, current SHAs, service health |
| `/repos/<slug>` | Deployments history, flow runs, logs per SHA |
| `/flows` | All flows across all repos, trigger status |
| `/runs/<id>` | Live-streamed log output for a specific flow run |
| `/volumes` | Declared volumes and disk usage |
| `/services` | All supervised services, PID, uptime, restart count |

### API

The UI is backed by a REST API that is also the integration point for external
tooling:

```
GET  /api/repos
GET  /api/repos/<slug>/deployments
POST /api/repos/<slug>/deploy          ← force a re-deploy of current SHA
POST /api/flows/<repo>/<flow>/trigger  ← manually trigger any flow
POST /api/events/<name>                ← fire an `event:` trigger
GET  /api/services
POST /api/services/<id>/restart
```

---

## Recursive / Composable Systems

Nexus is designed to manage a whole system, not a single service. The root
`nexus.yaml` (provided at install) defines the system boundary. Any repo listed
under `repos:` is pulled in and managed independently. Those repos can themselves
declare `repos:`, creating a tree of managed repositories.

```
root nexus.yaml
  ├── repos/api-service      → api-service/nexus.yaml
  │     └── (no child repos)
  ├── repos/worker           → worker/nexus.yaml
  │     └── repos/shared-lib → shared-lib/nexus.yaml
  └── repos/infra            → infra/nexus.yaml
        └── (volumes, no flows)
```

Each node is independent — it watches its own ref, runs its own flows, and manages
its own worktrees. The tree is for human organisation; there is no cross-repo
dependency ordering at this time.

---

## Security Posture (v1)

- Web UI is HTTP only, no authentication
- Intended to run on a private network or behind a firewall
- Tasks run as the same user as the nexus daemon
- No privilege escalation is performed

These constraints are explicit v1 decisions. Auth and TLS are deferred.

---

## Open Questions

1. **Poll vs webhook**: Polling is simple but has latency. Should nexus support
   an inbound webhook endpoint so GitHub/GitLab can push notifications?

2. **Prefect as a hard dependency**: Prefect adds significant startup weight.
   Should `LocalExecutor` be the default and Prefect opt-in?

3. **Cross-repo triggers**: Can a flow in repo A trigger a flow in repo B?
   Not in v1, but useful for fan-out deployments.

4. **Service port registry**: Should nexus maintain a declared port map so the
   UI can surface service URLs? (`ports: [8080]` on a task?)

5. **Secret management**: Tasks often need secrets (DB passwords, API keys).
   What is the story here? Mount from a volume? Environment injection at the
   daemon level?

6. **Concurrent deployments**: If two commits arrive quickly, does nexus queue
   them, or cancel the in-flight build in favour of the newer SHA?
   (Proposal: queue, with a max depth of 1 — newer commit supersedes in-flight build.)

7. **Nexus self-update**: The root repo can define a `startup` flow that restarts
   the nexus process itself. This needs care to avoid a restart loop.

---

## v1 Scope Summary

In scope:
- Install script (curl)
- Root `nexus.yaml` with `repos`, `volumes`, `flows`
- Git polling, worktree-based deployments
- Build / shutdown / startup lifecycle with rollback
- `mode: service` process supervision
- Prefect executor + local executor fallback
- Minimal web UI (read-only + manual trigger)
- REST API

Out of scope for v1:
- TLS / authentication
- Webhooks (inbound git events)
- Cross-repo triggers
- Secret management
- Multi-machine / distributed execution
- Windows support
