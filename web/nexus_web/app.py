"""FastAPI application wiring the daemon socket client to iris views.

The URL scheme mirrors the resource-name tree (the design's page spec):

    /                         overview — all projects
    /<address>                project detail
    /<address>/<service>      service status + log

Because addresses and inline service names contain slashes, project and service
pages share a single catch-all route that resolves the path against the live
project list (see tree.resolve).
"""

from __future__ import annotations

from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse
from iris import is_fx
from iris.integrations.fastapi import IrisResponse

from . import tree, views
from .client import NexusClient, NexusError


def create_app(client: NexusClient) -> FastAPI:
    app = FastAPI(title="nexus-web")

    @app.get("/healthz")
    def healthz() -> PlainTextResponse:
        return PlainTextResponse("ok")

    @app.get("/")
    def overview() -> IrisResponse:
        try:
            projects = client.list_projects()
        except NexusError as e:
            return IrisResponse(views.not_found_page(f"daemon unreachable: {e}"), status_code=502)
        return IrisResponse(views.overview_page(projects))

    @app.get("/{path:path}")
    def detail(path: str, request: Request) -> IrisResponse:
        try:
            projects = client.list_projects()
        except NexusError as e:
            return IrisResponse(views.not_found_page(f"daemon unreachable: {e}"), status_code=502)

        target = tree.resolve(path, {p["name"] for p in projects})
        if target is None:
            return IrisResponse(views.not_found_page(f"No project or service at /{path}"), status_code=404)

        if target[0] == "project":
            address = target[1]
            try:
                project = client.get_project(address)
                history = client.get_history(address)
                services = client.list_services(address)
            except NexusError as e:
                return IrisResponse(views.not_found_page(str(e)), status_code=502)
            return IrisResponse(views.project_page(address, project, history, services))

        # service
        _, address, service = target
        try:
            services = client.list_services(address)
        except NexusError as e:
            return IrisResponse(views.not_found_page(str(e)), status_code=502)
        row = next((s for s in services if s["name"] == service), None)
        if row is None:
            return IrisResponse(views.not_found_page(f"No service /{path}"), status_code=404)

        log = client.get_log(address, service)
        # fixi request → return just the log block for an in-place swap.
        if is_fx(request.headers):
            return IrisResponse(views.log_fragment(log))
        return IrisResponse(views.service_page(address, service, row, log))

    @app.post("/{path:path}")
    def action(path: str) -> IrisResponse:
        # POST on a page URL performs its action: redeploy a project, restart a
        # service. The response is a banner fragment fixi swaps into #banner.
        try:
            projects = client.list_projects()
        except NexusError as e:
            return IrisResponse(views.action_banner(f"daemon unreachable: {e}", ok=False), status_code=502)

        target = tree.resolve(path, {p["name"] for p in projects})
        if target is None:
            return IrisResponse(views.action_banner(f"No project or service at /{path}", ok=False), status_code=404)

        try:
            if target[0] == "project":
                res = client.redeploy(target[1])
                return IrisResponse(views.action_banner(f"Redeploy queued: {_short(res.get('queued'))}"))
            _, address, service = target
            services = client.list_services(address)
            if service not in {s["name"] for s in services}:
                return IrisResponse(views.action_banner(f"No service /{path}", ok=False), status_code=404)
            res = client.restart(address, service)
            return IrisResponse(views.action_banner(f"Restarted {res.get('restarted', service)}"))
        except NexusError as e:
            return IrisResponse(views.action_banner(str(e), ok=False), status_code=502)

    return app


def _short(sha) -> str:
    return sha[:12] if sha else "?"
