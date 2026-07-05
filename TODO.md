# Nexus — TODO

| Task | Designed | Implemented | Tested |
|------|:--------:|:-----------:|:------:|
| **Foundation** |
| Install script (`curl \| sh`, sets up NEXUS_HOME, registers user service) | ✅ | ✅ | ✅ |
| `nexus-pm` process manager binary (`cmd/nexus-pm`) | ✅ | ✅ | |
| `nexus-pm.sock` HTTP API: spawn / stop / status / runtime-restart | ✅ | ✅ | |
| `RemoteSupervisor` client in nexus runtime (talks to nexus-pm.sock) | ✅ | ✅ | |
| `PMSocket` path added to `home.Paths` | ✅ | ✅ | ✅ |
| NEXUS_HOME directory structure creation | ✅ | ✅ | ✅ |
| systemd user service registration (Linux) — points to `nexus-pm` | ✅ | ✅ | |
| launchctl plist registration (macOS) — points to `nexus-pm` | ✅ | ✅ | |
| **Configuration** |
| `nexus.yaml` parser (external projects, inline projects, recursive `projects:`) | ✅ | ✅ | ✅ |
| Project name inference from spec path (final segment default) | ✅ | ✅ | ✅ |
| Custom project name via `spec-path:name` syntax | ✅ | ✅ | ✅ |
| `nexus project add <spec-path[:name]>` CLI command | ✅ | ✅ | |
| `nexus project remove <name>` CLI command | ✅ | ✅ | |
| **Git layer** |
| Bare clone at spec path under `repos/` | ✅ | ✅ | ✅ |
| Git transport resolution from git CLI config (SSH/HTTPS/local) | ✅ | ✅ | ✅ |
| 30-second polling loop via `git ls-remote` | ✅ | ✅ | ✅ |
| `@<branch>` ref resolution (branch tip SHA) | ✅ | ✅ | ✅ |
| `@<tag>` ref resolution (exact tag SHA) | ✅ | ✅ | ✅ |
| `@latest` semver tag resolution (`--sort=-version:refname`) | ✅ | ✅ | ✅ |
| Commit queuing (latest-wins, one pending SHA per deployment) | ✅ | ✅ | ✅ |
| **Deployment lifecycle** |
| CHECKOUT: `git worktree add` at project alias path under root spec-path | ✅ | ✅ | ✅ |
| BUILD: `sh -c` in nexus.yaml directory, log to `logs/<address>/<sha>-build.log` | ✅ | ✅ | ✅ |
| Failed build: remove worktree, mark SHA failed, keep current services | ✅ | ✅ | ✅ |
| SHUTDOWN: SIGTERM all services, 30s grace, SIGKILL survivors | ✅ | ✅ | ✅ |
| STARTUP: spawn services from new worktree | ✅ | ✅ | ✅ |
| VERIFY: 5-second window, any exit triggers rollback | ✅ | ✅ | ✅ |
| PROMOTE: record new SHA as active in `nexus.db` | ✅ | ✅ | ✅ |
| CLEANUP: `git worktree remove` old worktree | ✅ | ✅ | ✅ |
| ROLLBACK: restart previous worktree's services on VERIFY failure | ✅ | ✅ | ✅ |
| **Nested projects** |
| External sub-project discovery from parent `projects:` (`src:`) | ✅ | ✅ | ✅ |
| External sub-project independent polling / own-ref deploy | ✅ | ✅ | ✅ |
| Sub-project worktree under root spec path, addressed `<root>/<alias>` | ✅ | ✅ | ✅ |
| Sub-project SHA tracking via deployments table (not projects table) | ✅ | ✅ | ✅ |
| Sub-project teardown when removed from parent config | ✅ | ✅ | ✅ |
| Recursive recovery of sub-projects on daemon restart | ✅ | ✅ | |
| Inline sub-project deploy (shares parent worktree, atomic with parent) | ✅ | ✅ | ✅ |
| Config flatten-to-units (inline subtree + external refs) | ✅ | ✅ | ✅ |
| External sub-projects nested inside inline projects | ✅ | ✅ | |
| **Process supervision** |
| Service spawning with `sh -c`, working dir = nexus.yaml directory | ✅ | ✅ | ✅ |
| Environment injection (`NEXUS_PROJECT`, `NEXUS_SHA`, `NEXUS_REF`, `NEXUS_WORKTREE`) | ✅ | ✅ | |
| `NEXUS_VOLUME_<NAME>` env injection per declared volume | ✅ | ✅ | |
| Restart on unexpected exit with exponential backoff (1s → 2s → 4s … cap 60s) | ✅ | ✅ | ✅ |
| Degraded state: >5 crashes in 60s → stop restarting, alert | ✅ | ✅ | ✅ |
| Service log capture to `logs/<address>/<service>/current.log` | ✅ | ✅ | ✅ |
| **Volumes** |
| Volume directory creation at `volumes/<address>/` on first use | ✅ | ✅ | |
| **State persistence** |
| `nexus.db` SQLite schema (projects, deployments, service state) | ✅ | ✅ | ✅ |
| Full state recovery from `nexus.db` on daemon restart | ✅ | ✅ | |
| Concurrency-safe DB (WAL, busy_timeout, single writer) | ✅ | ✅ | ✅ |
| Idempotent worktree checkout (survives interrupted deploys) | ✅ | ✅ | ✅ |
| **Daemon socket** |
| Unix socket server at `$NEXUS_HOME/nexus.sock` | ✅ | ✅ | ✅ |
| `GET /projects` — list all projects and health summary | ✅ | ✅ | ✅ |
| `GET /projects/<address>` — deployment detail and current SHA | ✅ | ✅ | ✅ |
| `GET /projects/<address>/history` — deployment history | ✅ | ✅ | ✅ |
| `POST /projects/<address>/redeploy` — re-run build + restart at current SHA | ✅ | ✅ | ✅ |
| `GET /projects/<address>/services` — list services and status | ✅ | ✅ | ✅ |
| `GET /projects/<address>/services/<name>/log` — stream service log | ✅ | ✅ | |
| `POST /projects/<address>/services/<name>/restart` — manual restart | ✅ | ✅ | ✅ |
| **Self-update** |
| Build script: compile Go binary, atomic swap to `$NEXUS_HOME/bin/nexus` | ✅ | ✅ | ✅ |
| `nexus.yaml` self-tracking config (build-only, no services) | ✅ | ✅ | ✅ |
| After self-build deploy, call `POST /runtime/restart` on nexus-pm.sock | ✅ | ✅ | ✅ |
| Self-identification via spec path (NEXUS_SELF_SPEC override) | ✅ | ✅ | ✅ |
| **Web UI (Python / iris)** |
| Unix socket HTTP client transport | ✅ | | |
| Project tree page (`/`) | ✅ | | |
| Project detail page (`/<project-name>`) | ✅ | | |
| Nested project / service / volume detail pages | ✅ | | |
| Live log tail | ✅ | | |
| Public REST API (proxied from daemon socket) | ✅ | | |
| **Go unit tests** |
| Ref parsing (`@branch`, `@tag`, `@latest`) from `git ls-remote` output | ✅ | ✅ | ✅ |
| Commit queuing logic (latest-wins, replace pending) | ✅ | ✅ | ✅ |
| Deployment lifecycle state machine transitions | ✅ | ✅ | ✅ |
| Process supervision: backoff timing, degraded detection | ✅ | ✅ | ✅ |
| Socket API handlers | ✅ | ✅ | ✅ |
| Volume and log path derivation from resource addresses | ✅ | ✅ | ✅ |
| Project tree loading: external, inline, nested | ✅ | ✅ | ✅ |
| **pytest e2e tests** |
| Test fixtures: daemon subprocess, local bare git repos, socket client | ✅ | ✅ | ✅ |
| Service starts after first commit | ✅ | ✅ | ✅ |
| Deployment recorded in history (active status) | ✅ | ✅ | ✅ |
| Failed build does not promote SHA | ✅ | ✅ | ✅ |
| New commit triggers automatic redeploy | ✅ | ✅ | ✅ |
| Redeploy same SHA reuses worktree, keeps service running | ✅ | ✅ | ✅ |
| Self-update: nexus restarts itself, user services keep running (same PID) | ✅ | ✅ | ✅ |
| Service restarts on crash, reaches degraded after threshold | ✅ | | |
| Rollback on failed build (previous services kept running) | ✅ | | |
| New commit replaces queued SHA during active build | ✅ | | |
| External nested project deploys independently on its own ref change | ✅ | ✅ | ✅ |
| External sub-project torn down when removed from parent config | ✅ | ✅ | ✅ |
| Inline project deploys together with parent | ✅ | ✅ | ✅ |
| Inline project redeploys with parent (new worktree, new PIDs) | ✅ | ✅ | ✅ |
| `nexus project add` and `nexus project remove` round-trip | ✅ | | |
