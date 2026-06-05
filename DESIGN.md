# Nexus Design

Nexus is a self-hosted project orchestration layer that binds multiple git-based app repos
together using process management (process-compose) and workflow orchestration (Prefect 3).

Install with one command, point it at a `nexus.yaml`, and it clones your apps, starts their
processes, and tracks their Prefect flows — then keeps everything up-to-date by polling git.

---

## Architecture Overview

```
curl install.sh | bash -s -- <config-url>
        │
        ▼
~/.nexus/
├── config.yaml          ← fetched from user-provided URL
├── apps/
│   ├── api/             ← git clone (has nexus.yaml with flows + processes)
│   └── workers/         ← git clone (has nexus.yaml with flows only)
└── nexus/               ← this repo (the nexus source)
    ├── process-compose.yaml
    ├── pyproject.toml
    └── src/nexus/

process-compose up \
  -f nexus/process-compose.yaml \
  -f apps/api/api-compose.yaml \   ← collected from api/nexus.yaml
  -p 9080 -t=false
```

---

## nexus.yaml — Unified Config Format

There is one config format used everywhere: **`nexus.yaml`**.

The root file (pointed to at install time) can include other repos by name.
Each included repo has its own `nexus.yaml` at its root.
**Depth is limited to 1**: children cannot include further children.

### Root nexus.yaml

```yaml
project: my-project

includes:
  api:                               # name → namespace and base path /api
    repo: https://github.com/org/api
    branch: main                     # optional, default: main
    poll_interval: 30                # optional seconds, default: 60

  workers:                           # shorthand: just the repo URL
    repo: https://github.com/org/workers

# Root-level flows and processes are also valid:
flows:
  health: flows/health.py:health_check

processes:
  infra: infra-compose.yaml
```

### App nexus.yaml (inside an included repo)

```yaml
# No 'project', no 'includes' — depth-1 only

flows:
  ingest: src/flows/ingest.py:ingest_flow     # name → file:function entrypoint
  transform: src/flows/transform.py:transform

processes:
  web: process-compose.yaml                  # name → compose file path
  jobs: jobs-compose.yaml
```

Apps may have **only flows**, **only processes**, or **both**.
An app with no `nexus.yaml` may still have a bare `process-compose.yaml` at its root
(backward-compatible fallback).

---

## Components

### nexus-web (port 8080)
FastAPI process serving a static HTML page that links to the Prefect UI.
No auth, no HTTPS — apps own their own security. Entry point: `src/nexus/web.py`.

### prefect-server (port 4200)
A local Prefect 3 server. All flows from all apps are deployed here.

### prefect-worker
A Prefect worker running against the local server, pool name: `nexus-pool`.

### nexus-poller
Polls each included repo for git changes and drives the deploy pipeline.
Handles flows-only apps (skips process stop/start when no processes are declared).
Entry point: `src/nexus/poller.py`.

---

## Process Compose Integration

`nexus.start` builds the `process-compose up` command by:
1. Always including nexus's own `process-compose.yaml`
2. Adding each root-level `processes` entry (relative to nexus source)
3. Reading every app's `nexus.yaml` and adding its `processes` entries (relative to app dir)

```
process-compose up \
  -f ~/.nexus/nexus/process-compose.yaml \
  -f ~/.nexus/apps/api/process-compose.yaml \
  -p 9080 -t=false
```

The process-compose HTTP API runs on port 9080. The poller uses it to stop/start
app processes on deploy.

### Process Naming Convention
Processes inside an app's compose files **must be prefixed with `<app-name>-`** so
the poller can match them to the right app.

```yaml
# api/process-compose.yaml
processes:
  api-web:
    command: uv run uvicorn main:app --port 8001
    working_dir: ${NEXUS_APP_API_DIR}

  api-worker:
    command: uv run python worker.py
    working_dir: ${NEXUS_APP_API_DIR}
```

### Injected Environment Variables

| Variable | Value |
|---|---|
| `NEXUS_HOME` | `~/.nexus` |
| `NEXUS_SRC` | path to nexus source |
| `PREFECT_API_URL` | `http://localhost:4200/api` |
| `NEXUS_APP_<NAME>_DIR` | absolute path to that app's cloned repo |
| `NEXUS_BASE_PATH_<NAME>` | `/<name>` — app's base URL path |

---

## Git Polling & Deploy Flow

1. Poller reads `config.yaml` each cycle (picks up new includes without restart)
2. For each include: `git ls-remote origin` to check remote HEAD
3. If remote HEAD ≠ local HEAD, trigger the deploy pipeline:

### Deploy Pipeline

```
git fetch origin
git worktree add apps/<app>.next origin/<branch>
uv sync  (in staging dir)
          │
          ▼
  nexus_deploy.py exists?
  ┌───────yes──────────────────────────────────────┐
  │  Run as a Prefect flow (visible in UI)         │
  │  flow fails ──► remove worktree                │
  │                 keep current running   ◄───────┘
  │  flow passes ──► proceed to update
  └────────no──────► proceed to update (auto-deploy)

update:
  if app has processes:
    stop app's process-compose processes
  git reset --hard origin/<branch>  (in active dir)
  uv sync  (in active dir)
  if app has processes:
    start app's processes
  git worktree remove apps/<app>.next
  (log any declared flows for re-registration)
```

Apps with **only flows** (no `processes`) are updated without any process stop/start.

### App Deploy Hook Convention

Optional `nexus_deploy.py` in app root — a Prefect `@flow` that runs CI before deploy:

```python
# nexus_deploy.py
from prefect import flow, task
import subprocess

@task
def lint():
    subprocess.run(["uv", "run", "ruff", "check", "."], check=True)

@task
def test():
    subprocess.run(["uv", "run", "pytest"], check=True)

@flow(name="nexus-deploy")
def deploy():
    lint()
    test()
    # returning normally → deploy proceeds; raising → deploy aborted
```

---

## Prefect Flows

Apps declare flows in their `nexus.yaml`. Nexus loads and tracks them.
Since all apps share one Prefect server, flows can reference each other by name
using the namespaced path `<app-name>/<flow-name>`.

All processes inherit `PREFECT_API_URL=http://localhost:4200/api`.

**Planned**: nexus will auto-register declared flows as Prefect deployments on
startup and on each app update (using the `file:function` entrypoints).

---

## Directory Structure

```
~/.nexus/
├── config.yaml
├── config/              ← if config came from a git repo
└── apps/
    └── <app-name>/      ← one dir per app

nexus repo:
├── DESIGN.md
├── CLAUDE.md
├── install.sh
├── process-compose.yaml
├── pyproject.toml
└── src/
    └── nexus/
        ├── config.py    ← nexus.yaml parsing (root + app format)
        ├── web.py       ← FastAPI portal page (port 8080)
        ├── poller.py    ← git polling, deploy pipeline
        ├── setup.py     ← initial app cloning
        └── start.py     ← builds + execs process-compose command
```

---

## What's Not Covered Yet

- Startup on boot (systemd unit / launchd plist)
- Prefect flow auto-registration (entrypoints from nexus.yaml → Prefect deployments)
- Config hot-reload for new includes (poller already re-reads config each cycle)
