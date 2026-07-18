"""App-level tests using FastAPI's TestClient against a stub NexusClient.

Exercises routing, the fixi log fragment, and the redeploy/restart actions
without a real daemon socket.
"""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from fastapi.testclient import TestClient  # noqa: E402

from nexus_web.app import create_app  # noqa: E402


class StubClient:
    def __init__(self):
        self.redeployed = []
        self.restarted = []

    def list_projects(self):
        return [
            {"name": "app", "ref": "@main", "current_sha": "abc123def456", "health": "degraded"},
            {"name": "app/db", "ref": "@v15", "current_sha": "deadbeefcafe", "health": "healthy"},
        ]

    def get_project(self, address):
        return {"name": address, "ref": "@main", "current_sha": "abc123def456", "health": "degraded"}

    def get_history(self, address):
        return [{"id": 1, "sha": "abc123def456", "status": "active", "started_at": 1700000000}]

    def list_services(self, address):
        return [
            {"name": "api", "running": True, "degraded": False, "restarts": 0, "pid": "111"},
            {"name": "metrics/exporter", "running": False, "degraded": True, "restarts": 6, "pid": ""},
        ]

    def get_log(self, address, service):
        return f"log for {address}/{service}\nMARKER_LINE\n"

    def redeploy(self, address):
        self.redeployed.append(address)
        return {"queued": "abc123def456"}

    def restart(self, address, service):
        self.restarted.append((address, service))
        return {"restarted": f"{address}/{service}"}


def _client():
    stub = StubClient()
    return TestClient(create_app(stub)), stub


def test_healthz():
    c, _ = _client()
    r = c.get("/healthz")
    assert r.status_code == 200 and r.text == "ok"


def test_overview_lists_projects():
    c, _ = _client()
    r = c.get("/")
    assert r.status_code == 200
    assert "app" in r.text and "db" in r.text and "Projects" in r.text


def test_project_page_has_services_and_redeploy():
    c, _ = _client()
    r = c.get("/app")
    assert r.status_code == 200
    assert "metrics/exporter" in r.text
    assert 'fx-action="/app"' in r.text and 'fx-method="post"' in r.text  # redeploy button


def test_service_page_and_fx_fragment():
    c, _ = _client()
    full = c.get("/app/metrics/exporter")
    assert full.status_code == 200
    assert "MARKER_LINE" in full.text and 'id="log"' in full.text
    assert "setInterval" in full.text  # auto-poll script

    frag = c.get("/app/metrics/exporter", headers={"FX-Request": "true"})
    assert frag.status_code == 200
    assert frag.text.strip().startswith("<pre") and "<html" not in frag.text


def test_redeploy_action():
    c, stub = _client()
    r = c.post("/app")
    assert r.status_code == 200
    assert "Redeploy queued" in r.text and "banner" in r.text
    assert stub.redeployed == ["app"]


def test_restart_action_on_inline_service():
    c, stub = _client()
    r = c.post("/app/metrics/exporter")
    assert r.status_code == 200
    assert "Restarted" in r.text
    assert stub.restarted == [("app", "metrics/exporter")]


def test_unknown_paths_404():
    c, _ = _client()
    assert c.get("/nope").status_code == 404
    assert c.post("/nope").status_code == 404
    assert c.post("/app/no-such-service").status_code == 404
