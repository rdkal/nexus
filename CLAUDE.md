# Nexus

Self-hosted project orchestration: git polling, process management (process-compose), and workflow orchestration (Prefect 3).

## Commands

```bash
uv sync                  # install deps
uv run pytest tests/ -v  # run integration tests
./install.sh --home /tmp/nexus-dev tests/fixtures/nexus.yaml  # dev run
```

## Architecture

See `DESIGN.md` for the full design. Short version:

- `install.sh` — one-command setup; auto-installs uv and process-compose if missing; `--home <dir>` overrides `NEXUS_HOME`
- `process-compose.yaml` — nexus services: `prefect-server`, `prefect-worker`, `nexus-web` (port 8080), `nexus-poller`
- `src/nexus/config.py` — parses `nexus.yaml` (root + app format, see below)
- `src/nexus/setup.py` — clones included app repos into `$NEXUS_HOME/apps/`
- `src/nexus/start.py` — builds and execs the `process-compose up` command, collecting compose files from all app `nexus.yaml`s
- `src/nexus/web.py` — FastAPI app on port 8080; static HTML page linking to Prefect UI (port 4200)
- `src/nexus/poller.py` — polls each included repo; on change prepares a `git worktree` staging dir (`<app>.next`), runs any deploy gates declared in the app's `nexus.yaml`, then resets the active dir and restarts processes (process steps skipped for flows-only apps)

## nexus.yaml Format

One config format used everywhere — root file and app repo files share the same schema.

**Root** (`config.yaml` / `nexus.yaml`):
```yaml
project: my-project
includes:
  api:                               # name → namespace + base path /api
    repo: https://github.com/org/api
    branch: main                     # default: main
    poll_interval: 30                # default: 60s
  workers: https://github.com/org/workers   # shorthand
```

**App** (inside app repo root as `nexus.yaml`):
```yaml
# No 'project', no 'includes' — depth-1 only
deploy:                                       # root gates: run before anything deploys
  - run-tests

flows:
  ingest: src/flows/ingest.py:ingest_flow   # shorthand: name → entrypoint
  heavy-job:                                 # with per-flow gate
    entrypoint: src/flows/heavy.py:heavy_job
    deploy:
      - integration-tests
  run-tests: src/tests/run.py:run_all        # gate flows live here too

processes:
  web: process-compose.yaml                 # shorthand: name → compose file
  jobs:                                      # with per-process gate
    file: jobs-compose.yaml
    deploy:
      - run-tests
```

Apps may have only `flows`, only `processes`, or both. Apps must have a `nexus.yaml`.

## Key Conventions

**App process names** must be prefixed `<app-name>-` so the poller can identify which processes belong to which app.

**Deploy gates** — any flow in the `flows` dict can be listed in a `deploy:` list (root, per-flow, or per-process). All gates run in a staging worktree before any processes are stopped or flows re-registered. A non-zero exit aborts the deploy and keeps the current version running.

**process-compose API** (port 9080):
- `GET /processes` → `{"data": [...]}`
- `PATCH /process/stop/{name}`
- `POST /process/start/{name}`

## Testing

- `tests/test_config.py` — unit tests for nexus.yaml parsing; no filesystem or subprocesses
- `tests/test_setup.py` — integration tests for `clone_or_update` using local bare git repos
- `tests/test_poller.py` — integration tests for change detection and the deploy pipeline

`tests/conftest.py` provides:
- `make_app(name, nexus_yaml)` — factory that wires up a bare remote + scratch clone + active clone locally so git push/fetch work without a network
- `nexus_home` — a temporary `$NEXUS_HOME` directory
- `fake_process_compose` — monkeypatches `app_processes`, `stop_process`, `start_process` in `poller` with in-memory fakes

The `running_nexus` session fixture in `conftest.py` runs `install.sh` against `tests/fixtures/nexus.yaml` and waits for port 8080 — use it for E2E tests. `nexus-web` has no `depends_on` so it starts immediately (~5s).

## TODO.md

`TODO.md` tracks every feature with three status columns: **Designed**, **Implemented**, **Tested**. Keep it up to date whenever you implement or test something — it is the source of truth for what's done.
