"""iris view builders for nexus-web.

Each function returns an iris node. Page/service builders return a full page
wrapped in the shared shell; ``log_fragment`` returns just the log block so it
can be swapped in via fixi without a full reload.
"""

from __future__ import annotations

import datetime

from iris import (
    Badge,
    Banner,
    Breadcrumbs,
    Container,
    Empty,
    Header,
    Panel,
    Page,
    Row,
    Stack,
    Stat,
    Table,
    h,
    raw,
)

from .tree import TreeNode, build_tree

TITLE = "nexus"

_HEALTH_VARIANT = {
    "healthy": "success",
    "degraded": "danger",
    "no_services": "muted",
    "not_deployed": "warning",
}

_STATUS_VARIANT = {
    "active": "success",
    "failed": "danger",
    "rolled_back": "warning",
    "building": "muted",
}


def _shell(view):
    return Page(title=TITLE, fixi=True)[
        Header(title=h.a(href="/", class_="brand")[TITLE]),
        Container[view],
    ]


def _short(sha: str) -> str:
    return sha[:12] if sha else "—"


def _fmt_time(unix) -> str:
    if not unix:
        return "—"
    return datetime.datetime.fromtimestamp(unix).strftime("%Y-%m-%d %H:%M:%S")


def _health_badge(health: str):
    return Badge(f".{_HEALTH_VARIANT.get(health, 'muted')}")[health or "—"]


def _status_badge(status: str):
    return Badge(f".{_STATUS_VARIANT.get(status, 'muted')}")[status or "—"]


def _service_badge(s: dict):
    if s.get("degraded"):
        return Badge(".danger")["degraded"]
    if s.get("running"):
        return Badge(".success")["running"]
    return Badge(".muted")["stopped"]


def _crumbs(address: str):
    parts = address.split("/")
    items: list = [("nexus", "/")]
    acc = ""
    for i, part in enumerate(parts):
        acc = part if i == 0 else acc + "/" + part
        if i < len(parts) - 1:
            items.append((part, "/" + acc))
        else:
            items.append(part)  # current page — no link
    return Breadcrumbs(items=items)


# --- overview -------------------------------------------------------------

def overview_page(projects: list[dict]):
    roots = build_tree(projects)
    if not roots:
        return _shell(Stack[h.h1["Projects"], Empty(title="No projects yet")])
    return _shell(Stack[h.h1["Projects"], _tree_table(roots)])


def _tree_table(roots: list[TreeNode]):
    rows: list = []

    def walk(node: TreeNode, depth: int):
        indent = "  " * depth
        p = node.project
        if p is not None:
            name = h.a(href="/" + node.address)[indent + node.label]
            rows.append(
                [
                    name,
                    _health_badge(p.get("health", "")),
                    h.code[_short(p.get("current_sha", ""))],
                    h.span(class_="muted")[p.get("ref", "") or "—"],
                ]
            )
        else:
            rows.append([h.span[indent + node.label], "", "", ""])
        for c in node.children:
            walk(c, depth + 1)

    for r in roots:
        walk(r, 0)
    return Table(headers=["Project", "Health", "SHA", "Ref"], rows=rows)


# --- project detail -------------------------------------------------------

def project_page(address: str, project: dict, history: list[dict], services: list[dict]):
    meta = Row[
        Stat(label="Health", value=_health_badge(project.get("health", ""))),
        Stat(label="Current SHA", value=h.code[_short(project.get("current_sha", ""))]),
        Stat(label="Ref", value=project.get("ref", "") or "—"),
    ]
    # Redeploy re-runs the build + swap at the current SHA. POST to the project's
    # own URL; fixi swaps the returned banner into #banner.
    redeploy = _action_button("Redeploy", "/" + address)
    return _shell(
        Stack[
            _crumbs(address),
            Row[h.h1[address], redeploy],
            h.div("#banner"),
            meta,
            h.h2["Services"],
            _services_table(address, services),
            h.h2["History"],
            _history_table(address, history),
        ]
    )


def _services_table(address: str, services: list[dict]):
    if not services:
        return Empty(title="No services")
    rows = []
    for s in services:
        name = s["name"]
        rows.append(
            [
                h.a(href="/" + address + "/" + name)[name],
                _service_badge(s),
                h.code[s.get("pid", "") or "—"],
                str(s.get("restarts", 0)),
            ]
        )
    return Table(headers=["Service", "Status", "PID", "Restarts"], rows=rows)


def _history_table(address: str, history: list[dict]):
    if not history:
        return Empty(title="No deployments")
    rows = []
    for d in history:
        sha = d.get("sha", "")
        sha_cell = h.a(href=f"/{address}/builds/{sha}")[h.code[_short(sha)]] if sha else "—"
        rows.append(
            [
                sha_cell,
                _status_badge(d.get("status", "")),
                _fmt_time(d.get("started_at")),
            ]
        )
    return Table(headers=["SHA", "Status", "Started"], rows=rows)


# --- service detail -------------------------------------------------------

def service_page(address: str, service: str, row: dict | None, log: str):
    full = address + "/" + service
    meta = Row[
        Stat(label="Status", value=_service_badge(row) if row else "—"),
        Stat(label="PID", value=h.code[(row or {}).get("pid", "") or "—"]),
        Stat(label="Restarts", value=str((row or {}).get("restarts", 0))),
    ]
    restart = _action_button("Restart", "/" + full)
    return _shell(
        Stack[
            _crumbs(full),
            Row[h.h1[full], restart],
            h.div("#banner"),
            meta,
            h.h2["Log"],
            Panel[h.div("#log")[log_fragment(log)]],
            _log_poller("/" + full),
        ]
    )


def log_fragment(log: str):
    return h.pre(class_="log")[log if log else "(no output yet)"]


def _log_poller(url: str):
    # fixi has no interval trigger, so poll the log fragment with a tiny script.
    # The FX-Request header makes the app return just the <pre>, swapped into #log.
    script = (
        "(function(){var u=%r;setInterval(function(){"
        "fetch(u,{headers:{'FX-Request':'true'}})"
        ".then(function(r){return r.ok?r.text():null;})"
        ".then(function(t){var e=document.getElementById('log');"
        "if(t!==null&&e){e.innerHTML=t;}});},3000);})();"
    ) % url
    return h.script[raw(script)]


def _action_button(label: str, url: str):
    return h.button(
        class_="action",
        fx_action=url,
        fx_method="post",
        fx_trigger="click",
        fx_target="#banner",
        fx_swap="innerHTML",
    )[label]


def action_banner(message: str, ok: bool = True):
    return Banner(".success" if ok else ".danger")[message]


def build_log_page(address: str, sha: str, log: str):
    # Breadcrumbs: every part of the project address is a link, then this build.
    parts = address.split("/")
    items: list = [("nexus", "/")]
    acc = ""
    for i, part in enumerate(parts):
        acc = part if i == 0 else acc + "/" + part
        items.append((part, "/" + acc))
    items.append("build " + _short(sha))

    return _shell(
        Stack[
            Breadcrumbs(items=items),
            h.h1["Build log"],
            Row[h.code[_short(sha)], h.span(class_="muted")[address]],
            Panel[h.pre(class_="log")[log if log else "(no build log for this deployment)"]],
        ]
    )


def not_found_page(message: str = "Not found"):
    return _shell(Empty(title=message))
