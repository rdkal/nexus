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
- `src/nexus/config.py` — parses `config.yaml` (project + list of app repos)
- `src/nexus/setup.py` — clones app repos into `$NEXUS_HOME/apps/`
- `src/nexus/start.py` — builds and execs the `process-compose up` command, merging nexus + app compose files
- `src/nexus/web.py` — FastAPI app on port 8080; static HTML page linking to Prefect UI (port 4200)
- `src/nexus/poller.py` — polls each app's git repo; on change runs `nexus_deploy.py` (Prefect flow) in a `git worktree` staging dir, then resets the active dir and restarts processes

## Key Conventions

**App process names** must be prefixed `<app-name>-` so the poller can identify which processes belong to which app.

**App deploy flow** — optional `nexus_deploy.py` in app root with a `@flow(name="nexus-deploy")`. If it exits non-zero, deploy is aborted and the running version is kept.

**process-compose API** (port 9080):
- `GET /processes` → `{"data": [...]}`
- `PATCH /process/stop/{name}`
- `POST /process/start/{name}`

## Testing

Tests in `tests/test_install.py` are integration tests. The session fixture in `conftest.py` runs `install.sh` against `tests/fixtures/nexus.yaml` (no apps, no git cloning) and waits for port 8080. `nexus-web` has no `depends_on` so it starts immediately — tests finish in ~5s without waiting for Prefect.
