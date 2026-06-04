# Nexus Design

Nexus is a self-hosted project orchestration layer that binds multiple git-based app repos
together using process management (process-compose) and workflow orchestration (Prefect 3).

Install with one command, point it at a config file, and it clones your apps, starts their
processes, and registers their Prefect flows — then keeps everything up-to-date by polling git.

---

## Architecture Overview

```
curl install.sh | bash -s -- <config-url>
        │
        ▼
~/.nexus/
├── config.yaml          ← fetched from user-provided URL
├── apps/
│   ├── app1/            ← git clone of app repo
│   └── app2/
└── nexus/               ← this repo (the nexus source)
    ├── process-compose.yaml   ← nexus services
    ├── pyproject.toml
    └── src/nexus/

process-compose -f nexus/process-compose.yaml \
                -f apps/app1/process-compose.yaml \
                -f apps/app2/process-compose.yaml
```

---

## Config File Format

The single user-provided input. Can be a raw YAML URL or a git repo containing `nexus.yaml`.

```yaml
project: my-project

apps:
  - name: app1
    repo: https://github.com/org/app1
    branch: main

  - name: app2
    repo: https://github.com/org/app2
    branch: main
    poll_interval: 30   # optional, seconds (default: 60)
```

---

## Components

### nexus-web (port 8080)
A small Python/FastAPI process serving a static HTML page that links to the Prefect UI.
No auth, no HTTPS. Entry point: `src/nexus/web.py`.

### prefect-server (port 4200)
A local Prefect 3 server. Flows from all apps are deployed here.
Started by process-compose as part of the nexus service group.

### prefect-worker
A Prefect worker connected to the local server, executing flows from all apps.
Pool name: `nexus-pool`.

### nexus-poller
A Python process that polls each app's git repo for changes.
On change: stop the app's processes → `git pull` → restart.
Entry point: `src/nexus/poller.py`.

---

## Process Compose Integration

Nexus itself is defined in `process-compose.yaml` at the repo root. Each app repo
is expected to have a `process-compose.yaml` in its root as well.

At startup, `nexus.start` builds a single `process-compose up` command that merges
all files via repeated `-f` flags. Process-compose merges them into one process graph.

```
process-compose up \
  -f ~/.nexus/nexus/process-compose.yaml \
  -f ~/.nexus/apps/app1/process-compose.yaml \
  -f ~/.nexus/apps/app2/process-compose.yaml \
  -p 9080 --tui=false
```

The process-compose HTTP API runs on port 9080. The nexus-poller uses it to
stop/start app processes when a git change is detected.

### App Naming Convention
App process names **must** be prefixed with `<app-name>-` so the poller can identify
which processes belong to which app.

```yaml
# app1/process-compose.yaml
processes:
  app1-web:
    command: uv run uvicorn main:app --port 8001
    working_dir: ${NEXUS_APP_APP1_DIR}

  app1-worker:
    command: uv run python worker.py
    working_dir: ${NEXUS_APP_APP1_DIR}
```

### Injected Environment Variables
Every process inherits these from the environment nexus starts with:

| Variable | Value |
|---|---|
| `NEXUS_HOME` | `~/.nexus` |
| `NEXUS_SRC` | path to nexus source |
| `PREFECT_API_URL` | `http://localhost:4200/api` |
| `NEXUS_APP_<NAME>_DIR` | path to that app's cloned repo |

---

## Git Polling (Update Flow)

1. Poller reads `config.yaml` each cycle (picks up config changes without restart)
2. For each app: `git ls-remote origin` to get current remote HEAD
3. If remote HEAD ≠ local HEAD:
   a. Query process-compose API for processes prefixed `<app-name>-`
   b. Stop each one via `POST /process/stop`
   c. `git pull origin <branch>` in the app dir
   d. Start each one via `POST /process/start`
4. Sleep for `poll_interval` seconds

---

## Prefect Flows

Apps define Prefect flows normally in their Python code. To register them with the
nexus Prefect server, apps should include a deployment step in their process-compose
(e.g. a one-shot `app1-deploy` process that runs `prefect deploy`).

Since all apps share one Prefect server, flows from different apps can reference each
other by deployment name. The `PREFECT_API_URL` env var points every app at the same server.

---

## Directory Structure

```
~/.nexus/
├── config.yaml
├── config/              ← if config came from a git repo
└── apps/
    └── <app-name>/      ← one dir per app, bare git clone

nexus repo:
├── DESIGN.md
├── install.sh
├── process-compose.yaml
├── pyproject.toml
└── src/
    └── nexus/
        ├── __init__.py
        ├── config.py    ← config YAML parsing
        ├── web.py       ← FastAPI static page
        ├── poller.py    ← git polling + process restart
        ├── setup.py     ← initial app cloning
        └── start.py     ← builds + execs process-compose command
```

---

## What's Not Covered Yet

- Startup on boot (systemd unit / launchd plist)
- HTTPS / auth
- Prefect flow auto-registration on deploy
- Cross-app flow references (works naturally via shared Prefect server)
- Config file hot-reload (poller already re-reads config each cycle)
