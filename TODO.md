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
| `nexus version` / `--version` — release tag injected at build time (`-X main.version`), VCS-info fallback | ✅ | ✅ | ✅ |
| Clear error when `NEXUS_HOME` makes a socket path exceed the OS Unix-socket limit (not a bare `invalid argument`) | ✅ | ✅ | ✅ |
| `environment:` on projects and services (docker-compose map/list forms) with `${VAR}` interpolation + `.env` file | ✅ | ✅ | ✅ |
| Global `NEXUS_<PROJECT>_<VOLUME>` env var — reference another project's volume path without hardcoding | ✅ | ✅ | ✅ |
| Env isolation — processes inherit only allowlisted essentials + declared env, not the daemon's full env; daemon vars forwarded only when named | ✅ | ✅ | ✅ |
| Operator `.env` at `$NEXUS_HOME/env/<project>.env` — host-specific config/secrets outside git, overrides repo values | ✅ | ✅ | ✅ |
| Undefined `${VAR}` reference fails the deploy (before stopping old services) instead of expanding to empty | ✅ | ✅ | ✅ |
| `${VAR:-default}` / `${VAR-default}` opt-out — supply a fallback instead of erroring | ✅ | ✅ | ✅ |
| `environment:` on a `projects:` entry (composer override) honored for external sub-projects, not just inline | ✅ | ✅ | ✅ |
| Changing a nested project's parent-supplied `environment:`/`ref`/`src` rebuilds the child (reconcileChildren diffs content, not just alias presence) | ✅ | ✅ | ✅ |
| **Configuration** |
| `nexus.yaml` parser (external projects, inline projects, recursive `projects:`) | ✅ | ✅ | ✅ |
| Project name inference from spec path (final segment default) | ✅ | ✅ | ✅ |
| Custom project name via `spec-path:name` syntax | ✅ | ✅ | ✅ |
| `nexus project add <spec-path[:name]>` CLI command | ✅ | ✅ | |
| `nexus project remove <name>` CLI command | ✅ | ✅ | |
| `nexus project set-src <name> <new-spec>` — repoint a moved repo; resolves the new spec, keeps name/ref/SHA history, daemon rebuilds live (reconcile diffs spec_path/subdir) | ✅ | ✅ | ✅ |
| `nexus project stop`/`start <name>` — pause/resume a project tree for maintenance; persisted (`stopped` column), survives daemon restart | ✅ | ✅ | ✅ |
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
| Retry a failed deploy with capped exponential backoff (self-heal transient failures / self-update release race) — not only on a new commit | ✅ | ✅ | ✅ |
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
| Recovery re-spawns services skipped due to cross-project env not-yet-available (deterministic alias order + retry pass); surfaces still-unresolved loudly | ✅ | ✅ | ✅ |
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
| **Tasks (scheduled & triggered)** — one-shot commands; the trigger is the primitive, pipelines emerge from `after:` edges (see DESIGN "Tasks") |
| `tasks:` config field — one-shot `run`, in the deployed worktree with service env/volumes | ✅ | ✅ | ✅ |
| `schedule:` trigger — cron expression + `@every`/`@daily` shorthand, evaluated in UTC; missed fires skipped (no backfill) | ✅ | ✅ | ✅ |
| `after: <task>` trigger — run when the named sibling task exits 0; chain stops on failure | ✅ | ✅ | ✅ |
| Manual trigger — `nexus task run <project>/<task>` + `nexus task list`, cascades to `after:` dependents | ✅ | ✅ | ✅ |
| One-shot execution **under nexus-pm** (non-blocking `POST/GET/DELETE /run/{id}`) — survives a runtime restart; output to `logs/<address>/tasks/<task>/current.log` | ✅ | ✅ | ✅ |
| Poll-and-recover across a runtime restart — startup re-polls `running` rows, finalises them, and fires their `after:` cascade (lost run → `failed`) | ✅ | ✅ | ✅ |
| No self-overlap — hard DB guarantee via a partial unique index (`task_runs(address,task) WHERE status='running'`); a second concurrent claim is rejected by the DB, holds across restarts / scheduler swaps / overlapping processes | ✅ | ✅ | ✅ |
| `task_runs` table — per-run trigger reason (`schedule`/`after:X`/`manual`), start/finish, exit; socket `GET /projects/<addr>/tasks` | ✅ | ✅ | ✅ |
| Validate `after:` graph at deploy time — unknown task or cycle is a deploy error | ✅ | ✅ | ✅ |
| Fan-in / joins — `after: [a, b]` runs when all parents succeed; edge-triggered barrier against the join's own last run (no run-correlation table) | ✅ | ✅ | ✅ |
| **Tasks — designed soon** (specced later) |
| Failure-mode design — retry-with-policy on a failed task, `on_failure:` triggering a different task, `on: always` edges | | | |
| Cross-project task triggers (a task in one project triggering one in another) | | | |
| Web UI for tasks — project detail lists each task with trigger + last-run status, and a manual **Run** / **Retry** (on a failed task) button | ✅ | ✅ | ✅ |
| Web UI for tasks — task graph view, per-task run history, next-fire time | | | |
| **Managed runs (host-registered operations)** — imperative, run-once, durable long ops via `nexus run` (see DESIGN "Managed runs") |
| `runs` table — name, owning address, command, workdir, status, exit, start/finish | ✅ | ✅ | ✅ |
| `nexus run <name> -- <cmd>` — register + start; reuses the poll-and-recover run executor (child of nexus-pm, survives a runtime restart) | ✅ | ✅ | ✅ |
| Project association by working directory — cwd inside a project's current worktree → that address scopes the run (deepest match); else unattached | ✅ | ✅ | ✅ |
| Run-once contract — recorded outcome, never restarted on exit; interrupted (nexus-pm gone / reboot) → `failed`, surfaced for manual re-launch | ✅ | ✅ | ✅ |
| Output capture to `logs/<address>/runs/<name>/current.log`; `nexus run logs <name>` | ✅ | ✅ | ✅ |
| `nexus run list` / `rm`; socket endpoints on `nexus.sock` (`POST/GET /runs`, `GET /runs/<name>/log`, `DELETE /runs/<name>`) | ✅ | ✅ | ✅ |
| No-overlap — a live run `name` is unique (partial unique index on `running`), same hard DB guarantee as tasks | ✅ | ✅ | ✅ |
| `nexus run stop <name>` — SIGTERM→SIGKILL a running run via a new nexus-pm `POST /run/<id>/stop`; recorded as cancelled | ✅ | ✅ | ✅ |
| Web docs — a "Managed runs" section on the docs site (docs-src/build.py), like the Tasks section | ✅ | ✅ | ✅ |
| Distinct `cancelled` run status — an operator-stopped run records as `cancelled` (durable `stop_requested` flag drives the finaliser, incl. across a restart); an interrupted run stays `failed`. Widened `runs.status` CHECK via a table-rebuild migration | ✅ | ✅ | ✅ |
| `--project <address>` override for the cross-directory case | ✅ | | |
| `nexus run logs -f` follow | | | |
| Web UI — a Runs section on the project detail page (later) | | | |
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
| Self-identification is transport-independent (resolved clone URL vs bare spec) — else the runtime never restarts | ✅ | ✅ | ✅ |
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
