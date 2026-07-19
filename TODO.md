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
| Install & self-update without host Go — CI (`release.yml`) builds release binaries; `install.sh` and self-update download them (prebuilt only, no source fallback) | ✅ | ✅ | ✅ |
| Install registers no projects — `nexus project add` is a separate step afterwards | ✅ | ✅ | ✅ |
| Installer adds `$NEXUS_HOME/bin` to `PATH` (idempotent `~/.profile`/`~/.bashrc`/`~/.zshrc`) | ✅ | ✅ | |
| `project add`/`remove` reconcile the running daemon live (`POST /projects`) — no restart | ✅ | ✅ | ✅ |
| **Configuration** |
| `nexus.yaml` parser (external projects, inline projects, recursive `projects:`) | ✅ | ✅ | ✅ |
| Project name inference from spec path (final segment default) | ✅ | ✅ | ✅ |
| Custom project name via `spec-path:name` syntax | ✅ | ✅ | ✅ |
| `nexus project add <spec-path[:name]>` CLI command | ✅ | ✅ | |
| `nexus project remove <name>` CLI command | ✅ | ✅ | |
| `projects:` string shorthand — `<spec>@<ref>` (or bare `<spec>`) as an alternative to the `{src, ref}` map | ✅ | ✅ | ✅ |
| Drop the mandatory `@` ref prefix — bare refs (`main`, `v15`, `latest`, `web-v*`); `@` only as the `spec@ref` separator | ✅ | ✅ | ✅ |
| **Git layer** |
| Bare clone at spec path under `repos/` | ✅ | ✅ | ✅ |
| Git transport resolution — try spec as-is (honours `insteadOf`), then HTTPS, then SSH; store the working clone URL | ✅ | ✅ | ✅ |
| Reject an unresolvable spec at `nexus project add` instead of silently storing it | ✅ | ✅ | ✅ |
| 30-second polling loop via `git ls-remote` | ✅ | ✅ | ✅ |
| `@<branch>` ref resolution (branch tip SHA) | ✅ | ✅ | ✅ |
| `@<tag>` ref resolution (exact tag SHA) | ✅ | ✅ | ✅ |
| `@latest` semver tag resolution (`--sort=-version:refname`) | ✅ | ✅ | ✅ |
| `@<glob>` wildcard tag ref resolution (highest match) | ✅ | ✅ | ✅ |
| Repo-root walk-up discovery for subdirectory spec paths (`git ls-remote`) | ✅ | ✅ | ✅ |
| Commit queuing (latest-wins, one pending SHA per deployment) | ✅ | ✅ | ✅ |
| **Monorepo support** (many apps in one repo, deployed independently) |
| Wildcard tag ref `@<glob>` — highest semver tag matching the pattern (any scheme) | ✅ | ✅ | ✅ |
| Per-app ref isolation — a non-matching (other-app) tag must not redeploy | ✅ | ✅ | ✅ |
| Subdirectory spec path via walk-up repo discovery — `nexus.yaml` under a repo subpath | ✅ | ✅ | ✅ |
| External sub-project `src` may point at a subdirectory (walk-up in daemon) | ✅ | ✅ | ✅ |
| `projects.subdir` column + migration for existing DBs | ✅ | ✅ | ✅ |
| Path-scoped change detection for branch refs — redeploy only when the app's subtree changed | | | |
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
| Full state recovery from `nexus.db` on daemon restart (incl. inline services) | ✅ | ✅ | |
| Concurrency-safe DB (WAL, busy_timeout, single writer) | ✅ | ✅ | ✅ |
| Idempotent worktree checkout (survives interrupted deploys) | ✅ | ✅ | ✅ |
| **Daemon socket** |
| Unix socket server at `$NEXUS_HOME/nexus.sock` | ✅ | ✅ | ✅ |
| `GET /projects` — list all projects and health summary | ✅ | ✅ | ✅ |
| `GET /projects/<address>` — deployment detail and current SHA | ✅ | ✅ | ✅ |
| `GET /projects/<address>/history` — deployment history | ✅ | ✅ | ✅ |
| `POST /projects/<address>/redeploy` — re-run build + restart at current SHA | ✅ | ✅ | ✅ |
| `GET /projects/<address>/services` — list services and status | ✅ | ✅ | ✅ |
| `GET /projects/<address>/services/<name>/log` — stream service log | ✅ | ✅ | ✅ |
| `GET /projects/<address>/builds/<sha>/log` — build log for a deployment | ✅ | ✅ | ✅ |
| `POST /projects/<address>/services/<name>/restart` — manual restart | ✅ | ✅ | ✅ |
| Nested-address routing (slashed addresses/inline service names) | ✅ | ✅ | ✅ |
| **Self-update** |
| Build script: compile Go binary, atomic swap to `$NEXUS_HOME/bin/nexus` | ✅ | ✅ | ✅ |
| `nexus.yaml` self-tracking config (build-only, no services) | ✅ | ✅ | ✅ |
| After self-build deploy, call `POST /runtime/restart` on nexus-pm.sock | ✅ | ✅ | ✅ |
| Self-identification via spec path (NEXUS_SELF_SPEC override) | ✅ | ✅ | ✅ |
| Self-identification excludes subdir projects (web UI at `…/nexus/web` ≠ self) | ✅ | ✅ | ✅ |
| **Web UI (Python / iris)** |
| `nexus-web` lives in-repo at `web/`; added via `nexus project add github.com/rdkal/nexus/web` | ✅ | ✅ | ✅ |
| Unix socket HTTP client (httpx over UDS) wrapping the 7 endpoints | ✅ | ✅ | ✅ |
| Address-tree build + project-vs-service path resolution | ✅ | ✅ | ✅ |
| Overview page `/` — project tree, current SHA, health | ✅ | ✅ | ✅ |
| Project detail page `/<address>` — deployment history + services | ✅ | ✅ | ✅ |
| Service detail page + log (auto-polling tail) | ✅ | ✅ | ✅ |
| Redeploy / restart actions (fixi POST → banner) | ✅ | ✅ | ✅ |
| Build-log route `GET /projects/<address>/builds/<sha>/log` + web build-log page | ✅ | ✅ | ✅ |
| **Docs site** (GitHub Pages, iris — `docs-src/build.py`) |
| Static page with install + `nexus.yaml` syntax, served from `/docs` | ✅ | ✅ | ✅ |
| Code blocks full-width with horizontal overflow scroll (`overflow-x: auto`, no wrap) | ✅ | ✅ | ✅ |
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
| Nested project detail + history over socket | ✅ | ✅ | ✅ |
| Inline service log + restart over socket | ✅ | ✅ | ✅ |
| Web UI renders project tree + detail against a live socket | ✅ | ✅ | ✅ |
| Web UI redeploy + restart actions against a live socket | ✅ | ✅ | ✅ |
| Dogfood: nexus deploys `nexus-web` itself and it serves on port 7777 | ✅ | ✅ | ✅ |
| `nexus project add` and `nexus project remove` round-trip | ✅ | | |
