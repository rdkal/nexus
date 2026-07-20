# nexus-web

The optional web UI for [nexus](../). It is a **normal nexus project** — not
bundled or special-cased by the runtime — that connects to the daemon's Unix
socket (`$NEXUS_HOME/nexus.sock`) and serves the public HTTP interface on port
7777. The daemon itself never binds a public port; this process is the sole
HTTP interface. HTTP only, no auth — intended for a private network.

## Deploy

```
nexus project add github.com/rdkal/nexus-web --ref @latest
```

Its `nexus.yaml` builds with [uv](https://docs.astral.sh/uv/) — a single static
binary that needs no system Python or pip. If uv isn't already on the host, the
build step installs it (Astral's one-line installer) and lets it provision a
Python, so the dashboard deploys on a box with only `git` and `curl`. It then
runs straight from the venv uv built (`.venv/bin/python -m nexus_web`).

> This lives under `web/` inside the nexus repo for development; it is
> self-contained and can be lifted into its own repo unchanged.

## Run locally

```
python -m nexus_web --socket $NEXUS_HOME/nexus.sock --port 7777
```

## Pages

| URL | Content |
|---|---|
| `/` | All projects as a tree, current SHA + health |
| `/<address>` | Project detail: health, SHA, deployment history, services |
| `/<address>/<service>` | Service status + log (with a fixi Refresh) |

Addresses and inline service names contain slashes (`my-system/db`,
`metrics/exporter`); project and service pages share a catch-all route that
resolves the path against the live project list.

## Layout

- `nexus_web/client.py` — httpx client over the Unix socket (the 7 endpoints)
- `nexus_web/tree.py` — address-tree build + project-vs-service resolution (pure)
- `nexus_web/views.py` — iris view builders
- `nexus_web/app.py` — FastAPI routes
- `nexus_web/__main__.py` — CLI entry point
