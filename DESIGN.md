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
  -f apps/api/process-compose.yaml \   ← path from api/nexus.yaml processes.web.file
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

  workers: https://github.com/org/workers   # shorthand: plain string URL

# Root-level flows and processes are also valid:
flows:
  health: flows/health.py:health_check

processes:
  infra: infra-compose.yaml
```

### App nexus.yaml (inside an included repo)

```yaml
# No 'project', no 'includes' — depth-1 only

# Root deploy: gates that run once before anything in this app is deployed
deploy:
  - run-tests

flows:
  ingest: src/flows/ingest.py:ingest_flow     # shorthand: name → entrypoint
  heavy-job:                                   # with extra per-flow gate
    entrypoint: src/flows/heavy.py:heavy_job
    deploy:
      - integration-tests
  run-tests: src/tests/run.py:run_all          # this flow is used as a gate
  integration-tests: src/tests/integ.py:run

processes:
  web: process-compose.yaml                    # shorthand: name → compose file
  jobs:                                         # with extra per-process gate
    file: jobs-compose.yaml
    deploy:
      - smoke-test
  smoke-test: src/tests/smoke.py:smoke         # another gate flow (flows-only entry)
```

Apps may have **only flows**, **only processes**, or **both**.
Apps must have a `nexus.yaml` — there is no bare `process-compose.yaml` fallback.

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
git fetch + worktree staging
uv sync in staging
load staging/nexus.yaml
          │
          ▼
  run root deploy gates       ← app.deploy: [flow-names]
  run per-process gates        ← processes.<name>.deploy: [flow-names]
  run per-flow gates           ← flows.<name>.deploy: [flow-names]
          │
    any fail? ──► remove staging, keep current running
          │
          ▼
  stop process-compose processes  (skipped if app has no processes)
  git reset --hard + uv sync in active dir
  start process-compose processes
  re-register Prefect deployments (register.py)
  remove staging worktree
```

Gates run in the **staging worktree** against the new code, before anything is touched.
Apps with only flows skip the process stop/start entirely.

### Gate Flows

Any flow declared in `flows` can be used as a gate by referencing its name in
a `deploy` list. The poller executes gates via their `entrypoint` using `uv run python`.
Since `PREFECT_API_URL` is injected, runs appear in the Prefect UI.

```python
# src/tests/run.py
from prefect import flow, task
import subprocess

@task
def lint():
    subprocess.run(["uv", "run", "ruff", "check", "."], check=True)

@task
def test():
    subprocess.run(["uv", "run", "pytest"], check=True)

@flow
def run_all():
    lint()
    test()
    # normal return → gate passes; exception → gate fails, deploy aborted
```

---

## Prefect Flows

Apps declare flows in their `nexus.yaml`. On startup and after every successful
deploy, `nexus.register` upserts each declared flow as a Prefect deployment on
the shared server via the Prefect REST API.

All processes inherit `PREFECT_API_URL=http://localhost:4200/api`.

### Deployment naming

Prefect rejects slashes in names, so the Prefect deployment name is
`{app-name}-{flow-name}` (hyphen-joined). The underlying Prefect flow record
uses the Python function name from the entrypoint.

Example: `flows/ingest.py:ingest_flow` in app `api` with nexus flow name
`ingest` → Prefect flow `ingest_flow`, deployment name `api-ingest`.

### Behavior during live updates

Re-registration upserts the deployment record (updating `path` and `entrypoint`
to the newly-deployed active dir). It does **not** affect in-flight runs:

| Run state at re-registration | Behaviour |
|---|---|
| Already executing in worker | Finishes on old code — unaffected |
| Queued (Scheduled, not yet picked up) | Worker loads new code at pickup |
| New triggers / scheduled after deploy | Always use new code |

This is the right default for a single-machine setup: running work is never
interrupted, and new work always gets the latest version.

---

## Directory Structure

```
~/.nexus/
├── config.yaml
├── config/              ← if config came from a git repo
├── nexus/               ← this repo (the nexus source)
└── apps/
    ├── <app-name>/      ← active clone
    └── <app-name>.next/ ← staging worktree (exists only during deploy)

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
        ├── register.py  ← upserts Prefect deployments via REST API
        ├── setup.py     ← initial app cloning
        └── start.py     ← builds + execs process-compose command
```

---

## What's Not Covered Yet

- Startup on boot (systemd unit / launchd plist)
- Config hot-reload for new includes (poller already re-reads config each cycle)
