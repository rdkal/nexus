"""Render smoke tests for the iris views (needs iris installed)."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from iris import render  # noqa: E402

from nexus_web import views  # noqa: E402


def test_overview_renders_tree_with_links_and_health():
    projects = [
        {"name": "my-system", "ref": "@main", "current_sha": "abc123def456", "health": "no_services"},
        {"name": "my-system/db", "ref": "@v15", "current_sha": "deadbeefcafe", "health": "healthy"},
        {"name": "app", "ref": "@main", "current_sha": "1234567890ab", "health": "degraded"},
    ]
    out = render(views.overview_page(projects))
    assert "my-system" in out and "db" in out
    assert "healthy" in out and "degraded" in out
    assert 'href="/my-system/db"' in out


def test_overview_empty():
    out = render(views.overview_page([]))
    assert "No projects" in out


def test_project_page_lists_services_and_history():
    project = {"name": "app", "ref": "@main", "current_sha": "1234567890ab", "health": "degraded"}
    history = [
        {"id": 2, "sha": "1234567890ab", "status": "active", "started_at": 1700000000},
        {"id": 1, "sha": "0000oldsha00", "status": "rolled_back", "started_at": 1699990000},
    ]
    services = [
        {"name": "api", "running": True, "degraded": False, "restarts": 0, "pid": "111"},
        {"name": "metrics/exporter", "running": False, "degraded": True, "restarts": 6, "pid": ""},
    ]
    out = render(views.project_page("app", project, history, services))
    assert "api" in out and "metrics/exporter" in out
    assert "active" in out and "rolled_back" in out
    assert 'href="/app/metrics/exporter"' in out  # link to inline service


def test_service_page_shows_log_and_refresh():
    row = {"name": "metrics/exporter", "running": True, "degraded": False, "restarts": 0, "pid": "222"}
    out = render(views.service_page("app", "metrics/exporter", row, "l1\nEXPORTER_STARTED\n"))
    assert "EXPORTER_STARTED" in out
    assert 'id="log"' in out
    assert 'fx-action="/app/metrics/exporter"' in out  # fixi refresh target


def test_log_fragment_is_bare_pre():
    out = render(views.log_fragment("hello"))
    assert out.strip().startswith("<pre") and "hello" in out
    assert "<html" not in out  # fragment, not a full page
