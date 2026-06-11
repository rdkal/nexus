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
| **nexus.yaml parsing** | ✅ | ✅ | ✅ |
| Root config (project + includes) | ✅ | ✅ | ✅ |
| IncludeConfig shorthand (`github.com/org/repo@ref`) | ✅ | ✅ | ✅ |
| IncludeConfig full form (repo + poll_interval + env) | ✅ | ✅ | ✅ |
| Schema-less URL normalised, resolved at clone time | ✅ | ✅ | ✅ |
| `@ref` suffix for branch or tag pinning | ✅ | ✅ | ✅ |
| Root-level `env:` visible to all apps | ✅ | ✅ | ✅ |
| Per-include `env:` overrides root | ✅ | ✅ | ✅ |
| Multiple includes | ✅ | ✅ | ✅ |
| FlowConfig shorthand (string entrypoint) | ✅ | ✅ | ✅ |
| FlowConfig full form (entrypoint + deploy gates) | ✅ | ✅ | ✅ |
| ProcessConfig shorthand (string file) | ✅ | ✅ | ✅ |
| ProcessConfig full form (file + deploy gates) | ✅ | ✅ | ✅ |
| Root-level deploy gates list | ✅ | ✅ | ✅ |
| deploy: null / absent treated as [] | ✅ | ✅ | ✅ |
| App config (no project, no includes) | ✅ | ✅ | ✅ |
| **App repo cloning (setup.py)** | ✅ | ✅ | ✅ |
| Initial clone | ✅ | ✅ | ✅ |
| Update (fast-forward) existing clone | ✅ | ✅ | ✅ |
| Clone respects ref (branch or tag) | ✅ | ✅ | ✅ |
| **process-compose launch (start.py)** | ✅ | ✅ | ✅ |
| Nexus-own services compose | ✅ | ✅ | ✅ (implicitly — web starts) |
| App compose files collected from nexus.yaml | ✅ | ✅ | ✅ |
| Per-app env vars injected (NEXUS_APP_*_DIR, NEXUS_BASE_PATH_*) | ✅ | ✅ | ✅ |
| Per-include custom env vars injected | ✅ | ✅ | ✅ |
| **nexus-web portal (port 8080)** | ✅ | ✅ | ⚠️ |
| Serves HTTP 200 | ✅ | ✅ | ✅ |
| Links to Prefect UI at port 4200 | ✅ | ✅ | ✅ |
| Services section — live status from process-compose API | ✅ | ❌ | ❌ |
| Apps section — name, repo, current SHA, clone exists | ✅ | ❌ | ❌ |
| Config section — project, env keys, includes | ✅ | ❌ | ❌ |
| Graceful error when config.yaml missing or invalid | ✅ | ❌ | ❌ |
| **Prefect server + worker** | ✅ | ✅ | ❌ |
| Server starts on port 4200 | ✅ | ✅ | ❌ (port never checked in tests) |
| Worker connects to nexus-pool | ✅ | ✅ | ❌ |
| **Git poller — change detection** | ✅ | ✅ | ✅ |
| Detects remote HEAD change | ✅ | ✅ | ✅ |
| No-op when HEAD unchanged | ✅ | ✅ | ✅ |
| Returns False when active dir missing | ✅ | ✅ | ✅ |
| Re-reads config.yaml each cycle | ✅ | ✅ | ❌ |
| Per-app poll_interval | ✅ | ✅ | ❌ |
| **Deploy pipeline** | ✅ | ✅ | ✅ |
| Staging worktree (app.next) | ✅ | ✅ | ✅ |
| uv sync in staging | ✅ | ✅ | ✅ |
| No nexus.yaml in repo aborts deploy | ✅ | ✅ | ✅ |
| Root deploy gates pass → deploy proceeds | ✅ | ✅ | ✅ |
| Root deploy gate fails → deploy aborted, current version kept | ✅ | ✅ | ✅ |
| Per-process deploy gates | ✅ | ✅ | ✅ |
| Per-flow deploy gates | ✅ | ✅ | ✅ |
| Unknown gate name aborts deploy | ✅ | ✅ | ✅ |
| process stop → git reset → uv sync → process start | ✅ | ✅ | ✅ (real PC) |
| Flows-only app (skip process stop/start) | ✅ | ✅ | ✅ |
| Staging worktree cleanup on success and failure | ✅ | ✅ | ✅ |
| **Prefect flow auto-registration** | ✅ | ✅ | ✅ |
| Register declared flows as deployments on startup | ✅ | ✅ | ✅ |
| Re-register flows after app update | ✅ | ✅ | ✅ |
| Deployment naming: `{app-name}-{flow-name}` (Prefect rejects slashes) | ✅ | ✅ | ✅ |
| In-flight runs finish on old code; queued/new runs get new code | ✅ | ✅ | ❌ |
| **Startup on boot** | ✅ | ✅ | ⚠️ |
| systemd unit / launchd plist | ✅ | ✅ | ⚠️ (no automated test; requires real systemd/launchd session) |
