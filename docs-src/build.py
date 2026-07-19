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
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \\
  --project github.com/myorg/my-system
"""

MANAGE = """\
nexus project add github.com/myorg/my-system
nexus project add github.com/myorg/monorepo/services/api --ref @api-v*
nexus project remove my-system
"""

NEXUS_YAML = """\
# Build step (optional) — runs once per deploy, in this directory.
build: go build -o server ./cmd/server

# Named persistent directories (optional). Each is created on first use
# and exposed to services as $NEXUS_VOLUME_<NAME>.
volumes:
  data: {}

# Long-running processes nexus supervises (and restarts on crash).
services:
  web:
    run: ./server --port 8080

# Compose other projects (optional).
projects:
  # External sub-project: its own repo + ref, deployed independently.
  db:
    src: github.com/nexus-community/postgres
    ref: "@v15"

  # Inline sub-project: shares this repo, deployed together with it.
  metrics:
    services:
      exporter:
        run: ./exporter
"""

REFS = """\
@main       track a branch — deploys on every push
@v1.2.3     pin an exact tag
@latest     the newest semver tag
@web-v*     newest tag matching a glob — one app in a monorepo
"""

WEB_UI = """\
nexus project add github.com/rdkal/nexus/web
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
                    " on your PATH — no root. It downloads prebuilt binaries; ",
                    h.code["go"],
                    " is only needed to build from source.",
                ],
                code(INSTALL),
                h.p["Add or remove projects any time after install:"],
                code(MANAGE),
                h.h2["The nexus.yaml file"],
                h.p[
                    "A repo becomes deployable by adding a ",
                    h.code["nexus.yaml"],
                    " at its root. Every field is optional.",
                ],
                code(NEXUS_YAML),
                h.h3["Ref syntax"],
                h.p[
                    "The tracked ref (set with ",
                    h.code["--ref"],
                    ", or per sub-project via ",
                    h.code["ref:"],
                    ") always starts with ",
                    h.code["@"],
                    ":",
                ],
                code(REFS),
                h.h2["Web UI (optional)"],
                h.p[
                    "There's a small dashboard, and it's just another nexus project. "
                    "It lives in the nexus repo under ",
                    h.code["web/"],
                    ", so you add it by that subdirectory path — nexus finds the repo, "
                    "reads ",
                    h.code["web/nexus.yaml"],
                    ", and runs it on port 7777 against the daemon socket:",
                ],
                code(WEB_UI),
                h.p[
                    "It shows your project tree, each deployment's history and current SHA, "
                    "and per-service status with live logs — plus one-click redeploy and restart.",
                ],
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
