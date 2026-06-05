# TODO

Status legend: ✅ Done · ⚠️ Partial · ❌ Not done

| Feature | Designed | Implemented | Tested |
|---|---|---|---|
| **install.sh — one-command setup** | ✅ | ✅ | ⚠️ |
| `--home` flag | ✅ | ✅ | ✅ |
| Auto-install uv if missing | ✅ | ✅ | ❌ (pre-installed in test env) |
| Auto-install process-compose if missing | ✅ | ✅ | ❌ (pre-installed in test env) |
| Local file path for config | ✅ | ✅ | ✅ |
| Remote YAML URL for config | ✅ | ✅ | ❌ |
| Git repo URL for config | ✅ | ✅ | ❌ |
| **nexus.yaml parsing** | ✅ | ✅ | ⚠️ |
| Root config (project + includes) | ✅ | ✅ | ⚠️ (only empty/no-includes case) |
| IncludeConfig shorthand (string URL) | ✅ | ✅ | ❌ |
| IncludeConfig full form (repo/branch/poll_interval) | ✅ | ✅ | ❌ |
| FlowConfig shorthand (string entrypoint) | ✅ | ✅ | ❌ |
| FlowConfig full form (entrypoint + deploy gates) | ✅ | ✅ | ❌ |
| ProcessConfig shorthand (string file) | ✅ | ✅ | ❌ |
| ProcessConfig full form (file + deploy gates) | ✅ | ✅ | ❌ |
| Root-level deploy gates list | ✅ | ✅ | ❌ |
| **App repo cloning (setup.py)** | ✅ | ✅ | ❌ |
| Initial clone | ✅ | ✅ | ❌ |
| Update existing clone | ✅ | ✅ | ❌ |
| **process-compose launch (start.py)** | ✅ | ✅ | ⚠️ |
| Nexus-own services compose | ✅ | ✅ | ✅ (implicitly — web starts) |
| App compose files collected from nexus.yaml | ✅ | ✅ | ❌ |
| Per-app env vars injected (NEXUS_APP_*_DIR, NEXUS_BASE_PATH_*) | ✅ | ✅ | ❌ |
| **nexus-web portal (port 8080)** | ✅ | ✅ | ✅ |
| Serves HTTP 200 | ✅ | ✅ | ✅ |
| Links to Prefect UI at port 4200 | ✅ | ✅ | ✅ |
| **Prefect server + worker** | ✅ | ✅ | ❌ |
| Server starts on port 4200 | ✅ | ✅ | ❌ (port never checked in tests) |
| Worker connects to nexus-pool | ✅ | ✅ | ❌ |
| **Git poller** | ✅ | ✅ | ❌ |
| Detects remote HEAD change | ✅ | ✅ | ❌ |
| Re-reads config.yaml each cycle | ✅ | ✅ | ❌ |
| Per-app poll_interval | ✅ | ✅ | ❌ |
| **Deploy pipeline** | ✅ | ✅ | ❌ |
| Staging worktree (app.next) | ✅ | ✅ | ❌ |
| uv sync in staging | ✅ | ✅ | ❌ |
| Root deploy gates | ✅ | ✅ | ❌ |
| Per-process deploy gates | ✅ | ✅ | ❌ |
| Per-flow deploy gates | ✅ | ✅ | ❌ |
| Gate failure aborts deploy, keeps current running | ✅ | ✅ | ❌ |
| Process stop → git reset → uv sync → process start | ✅ | ✅ | ❌ |
| Flows-only app (skip process stop/start) | ✅ | ✅ | ❌ |
| Staging worktree cleanup on success and failure | ✅ | ✅ | ❌ |
| **Prefect flow auto-registration** | ✅ | ❌ | ❌ |
| Register declared flows as deployments on startup | ✅ | ❌ | ❌ |
| Re-register flows after app update | ✅ | ❌ | ❌ |
| **Startup on boot** | ❌ | ❌ | ❌ |
| systemd unit / launchd plist | ❌ | ❌ | ❌ |
