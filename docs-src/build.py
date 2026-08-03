#!/usr/bin/env python3
"""Static docs-site generator for nexus, built with iris.

Renders a single self-contained index.html (inline CSS, no external assets) into
the output directory (default: ../docs), which GitHub Pages serves.

    python build.py [output_dir]
"""

import dataclasses
import sys
from pathlib import Path

from iris import LIGHT, Container, Panel, Stack, h, raw, render
from iris import Page

REPO = "https://github.com/rdkal/nexus"

# A touch wider than the default measure so code blocks aren't cramped.
THEME = dataclasses.replace(LIGHT, measure="54rem")

# iris ships no <pre> styling, so long lines would overflow the page. Keep code
# blocks full-width and scroll them horizontally instead of wrapping.
EXTRA_CSS = """
pre.code {
  margin: 0;
  white-space: pre;
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  tab-size: 2;
}
"""

INSTALL = """\
curl -fsSL https://github.com/rdkal/nexus/raw/main/install.sh | sh
"""

NEXUS_YAML = """\
# Build step (optional) — runs once per deploy, in this directory.
build: go build -o server ./cmd/server

# Named persistent directories (optional). Each is created on first use
# and exposed to services as $NEXUS_VOLUME_<NAME>.
volumes:
  data: {}

# Environment variables (optional), docker-compose style. A .env next to
# this file is loaded; host secrets go in ~/.nexus/env/<project>.env.
environment:
  LOG_LEVEL: info

# Long-running processes nexus supervises (and restarts on crash).
services:
  web:
    run: ./server --port 8080

# Compose other projects (optional).
projects:
  # External sub-project (<spec>@<ref>), deployed independently.
  db: github.com/nexus-community/postgres@v15

  # Inline sub-project: shares this repo, deployed together with it.
  metrics:
    services:
      exporter:
        run: ./exporter
"""

REFS = """\
main       track a branch — deploys on every push
v1.2.3     pin an exact tag
latest     the newest semver tag
web-v*     newest tag matching a glob — one app in a monorepo
"""

TASKS_YAML = """\
tasks:
  # A one-shot command. Trigger it on a schedule, after another task, or by hand.
  backup:
    schedule: "@daily"        # cron, "@every 15m", "@hourly"/"@daily" (UTC)
    run: ./backup.sh

  notify:
    after: backup             # runs when backup succeeds; a failure stops the chain
    run: ./notify.sh

  report:
    after: [backup, notify]   # fan-in join: runs once BOTH have succeeded
    run: ./report.sh

  migrate:
    run: ./migrate.sh         # no trigger -> manual only (nexus task run app/migrate)
"""

RUN_EXAMPLE = """\
# Start a long-running one-shot operation on the host — supervised and logged.
nexus run backfill-2024 -- ./scripts/backfill.sh --year 2024

nexus run list            # name, project, status
nexus run logs backfill-2024
nexus run stop backfill-2024   # signal it to stop
nexus run rm backfill-2024     # forget a finished run
"""

WEB_UI = """\
nexus project add github.com/rdkal/nexus/web
"""

ENV_YAML = """\
# Project-wide — applies to the build and every service.
environment:
  LOG_LEVEL: info

services:
  api:
    environment:              # per-service; wins over project-wide
      PORT: ${PORT:-8080}     # ${VAR:-default}: optional, with a fallback
      TOKEN: ${CF_API_TOKEN}  # undefined -> deploy fails (never a silent empty)
    run: ./api

projects:
  authelia:
    src: github.com/org/infra/authelia
    environment:              # the composer configuring a nested sub-project
      TRAEFIK_DIR: ${NEXUS_RETU_TRAEFIK_DYNAMIC}   # point at another project's volume
"""

PRECEDENCE = """\
essentials < repo .env < project env < service env
  < parent projects: env < ~/.nexus/env/<project>.env < NEXUS_*
"""

CLI = """\
nexus project add <spec>[:name] [--ref <ref>]   register a project, deploy it now
nexus project list                              projects with their SHA and status
nexus project stop <name|address>               pause: stop services, stay tracked
                                                 (a root name or a nested address, e.g. retu/ingest)
nexus project start <name|address>              resume from the last deployed SHA
nexus project set-src <name> <new-spec>         repoint a moved repo (keeps name + history)
nexus project set-ref <name> <ref>              track a different branch/tag/glob, live
nexus project remove <name>                     forget a root project (stops it too)
nexus service stop <address> <service>          pause one service inside a project
nexus service start <address> <service>         resume a paused service
nexus task run <project>/<task>                 trigger a task now
nexus run <name> -- <command>                   start a durable, run-once host operation
nexus run list | logs <name> | stop <name> | rm <name>
nexus version                                   print the installed version
"""


def code(text: str):
    return Panel[h.pre(class_="code")[text.rstrip("\n")]]


def page():
    return Page(title="nexus — git-driven deployments", theme=THEME)[
        h.style[raw(EXTRA_CSS)],
        Container[
            Stack(gap=1)[
                h.h1["nexus"],
                h.p(class_="lede")[
                    "Git-driven deployments for your own servers. "
                    "Point nexus at a repo and every push to your tracked ref deploys — "
                    "no CI, no registry, no pipeline YAML."
                ],
                h.p[
                    "Source & full design: ",
                    h.a(href=REPO)[REPO.replace("https://", "")],
                    ".",
                ],
                h.h2["Install"],
                h.p[
                    "One line. Needs ",
                    h.code["git"],
                    " and ",
                    h.code["curl"],
                    " on your PATH — no root, no Go toolchain. It downloads prebuilt binaries.",
                ],
                code(INSTALL),
                h.p[
                    "The installer adds ",
                    h.code["~/.nexus/bin"],
                    " to your PATH — open a new terminal (or re-source your shell) so ",
                    h.code["nexus"],
                    " is found. Then add a project; the web dashboard is a good first one — "
                    "it's just another nexus project, in the nexus repo under ",
                    h.code["web/"],
                    ", so you add it by that subdirectory path:",
                ],
                code(WEB_UI),
                h.p[
                    "Adding a project deploys it right away, and nexus then polls its ref "
                    "(~30 s) and redeploys on new commits. The dashboard runs on port 7777 "
                    "and shows your project tree, deployment history, and per-service status "
                    "with live logs — plus one-click redeploy and restart.",
                ],
                h.h2["The nexus.yaml file"],
                h.p[
                    "A repo becomes deployable by adding a ",
                    h.code["nexus.yaml"],
                    " at its root. Every field is optional.",
                ],
                code(NEXUS_YAML),
                h.h3["Ref syntax"],
                h.p[
                    "A ref is a branch, tag, or tag glob — set it with ",
                    h.code["--ref"],
                    ", or inline as ",
                    h.code["<spec>@<ref>"],
                    ".",
                ],
                code(REFS),
                h.h2["Tasks"],
                h.p[
                    "Alongside long-running services, a project can define ",
                    h.strong["tasks"],
                    " — one-shot commands that run in the current deployment, with the same "
                    "environment and volumes. A task is triggered on a ",
                    h.code["schedule:"],
                    " (cron or ",
                    h.code["@every"],
                    "/",
                    h.code["@daily"],
                    "), ",
                    h.code["after:"],
                    " another task succeeds (a failure stops the chain), or manually with ",
                    h.code["nexus task run <project>/<task>"],
                    ". Give ",
                    h.code["after:"],
                    " a list — ",
                    h.code["after: [a, b]"],
                    " — for a fan-in join that runs once all the named tasks have succeeded.",
                ],
                code(TASKS_YAML),
                h.p[
                    "Every run is recorded with its outcome. The dashboard lists a project's "
                    "tasks with each one's trigger and last-run status, and a ",
                    h.strong["Run"],
                    " button — which reads ",
                    h.strong["Retry"],
                    " on a task whose last run failed, so you can re-run it with one click.",
                ],
                h.h2["Managed runs"],
                h.p[
                    "Tasks and services come from git. For a one-off long operation you want to "
                    "kick off ",
                    h.em["from the host"],
                    " — a data backfill, a migration, a bulk import — use a ",
                    h.strong["managed run"],
                    ". It runs in the background, supervised and logged, and its outcome is "
                    "recorded; it survives a nexus runtime self-update (it runs under the process "
                    "manager, not the runtime).",
                ],
                code(RUN_EXAMPLE),
                h.p[
                    "The owning project is inferred from the directory you launch it in — the one "
                    "whose ",
                    h.code["nexus.yaml"],
                    " lives there — so the run inherits that project's environment and volumes, "
                    "and its log lands under that project. A run is ",
                    h.strong["run-once"],
                    ": it is never restarted. Stopping it records it as ",
                    h.em["cancelled"],
                    "; if it is interrupted (a host reboot) it is marked failed — never silently "
                    "re-run.",
                ],
                h.h2["Environment variables"],
                h.p[
                    "Set variables at three levels — project-wide (build and every service), "
                    "per service (most specific wins), and on a ",
                    h.code["projects:"],
                    " entry (the composer configuring a nested sub-project). Docker-compose "
                    "syntax: a map, or a ",
                    h.code["- KEY=value"],
                    " list.",
                ],
                code(ENV_YAML),
                h.p[
                    "Two ",
                    h.code[".env"],
                    " files load automatically: one next to the ",
                    h.code["nexus.yaml"],
                    " (committed defaults) and ",
                    h.code["~/.nexus/env/<project>.env"],
                    " (host secrets, not in git, overrides the repo). Interpolate with ",
                    h.code["${VAR}"],
                    "; a reference to an undefined variable fails the deploy instead of "
                    "expanding to empty — use ",
                    h.code["${VAR:-default}"],
                    " for optionals.",
                ],
                h.p[
                    "Every project's volumes are published to all projects as ",
                    h.code["NEXUS_<PROJECT>_<VOLUME>"],
                    " (e.g. ",
                    h.code["NEXUS_TRAEFIK_DYNAMIC"],
                    "), so one project can reference another's volume path without hardcoding "
                    "it. Processes also get ",
                    h.code["NEXUS_PROJECT"],
                    ", ",
                    h.code["NEXUS_SHA"],
                    ", ",
                    h.code["NEXUS_REF"],
                    ", ",
                    h.code["NEXUS_WORKTREE"],
                    ", and ",
                    h.code["NEXUS_VOLUME_<NAME>"],
                    " for their own volumes — but nothing else from the daemon's environment, "
                    "so one project's secrets stay invisible to another. Precedence, low to high:",
                ],
                code(PRECEDENCE),
                h.h2["CLI commands"],
                h.p[
                    "The daemon runs as a background service; these are the commands you use. ",
                    h.code["add"],
                    ", ",
                    h.code["remove"],
                    ", ",
                    h.code["stop"],
                    ", and ",
                    h.code["start"],
                    " take effect immediately — the CLI updates the database and nudges the "
                    "running daemon. Add ",
                    h.code["--home"],
                    " to target a non-default ",
                    h.code["NEXUS_HOME"],
                    ".",
                ],
                code(CLI),
            ]
        ]
    ]


def main():
    out_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parent.parent / "docs"
    out_dir.mkdir(parents=True, exist_ok=True)

    html = render(page())
    (out_dir / "index.html").write_text(html, encoding="utf-8")
    # Disable Jekyll so GitHub Pages serves the file as-is.
    (out_dir / ".nojekyll").write_text("", encoding="utf-8")
    print(f"wrote {out_dir/'index.html'} ({len(html)} bytes)")


if __name__ == "__main__":
    main()
