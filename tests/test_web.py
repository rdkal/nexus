"""Unit tests for nexus-web: auth and API endpoints."""
import base64
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from fastapi.testclient import TestClient


def _basic(username: str, password: str) -> str:
    token = base64.b64encode(f"{username}:{password}".encode()).decode()
    return f"Basic {token}"


@pytest.fixture(autouse=True)
def _patch_env(monkeypatch):
    """Each test starts with auth disabled; patches both env and the module attribute."""
    import nexus.web as web_module
    monkeypatch.setenv("NEXUS_USER", "")
    monkeypatch.setenv("NEXUS_PASSWORD", "")
    monkeypatch.setattr(web_module, "NEXUS_USER", "")
    monkeypatch.setattr(web_module, "NEXUS_PASSWORD", "")


@pytest.fixture()
def client():
    import nexus.web as web_module
    return TestClient(web_module.app, raise_server_exceptions=True)


def _client_with_auth(monkeypatch, username: str, password: str):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_USER", username)
    monkeypatch.setattr(web_module, "NEXUS_PASSWORD", password)
    return TestClient(web_module.app, raise_server_exceptions=True)


# ── auth disabled ─────────────────────────────────────────────────────────────

def test_no_auth_required_returns_200(client):
    resp = client.get("/")
    assert resp.status_code == 200


def test_no_auth_required_with_credentials_still_200(client):
    resp = client.get("/", headers={"Authorization": _basic("user", "pass")})
    assert resp.status_code == 200


# ── auth enabled ──────────────────────────────────────────────────────────────

def test_auth_missing_credentials_returns_401(monkeypatch):
    c = _client_with_auth(monkeypatch, "admin", "secret")
    resp = c.get("/", auth=None)
    assert resp.status_code == 401


def test_auth_missing_credentials_has_www_authenticate_header(monkeypatch):
    c = _client_with_auth(monkeypatch, "admin", "secret")
    resp = c.get("/")
    assert "WWW-Authenticate" in resp.headers
    assert resp.headers["WWW-Authenticate"] == 'Basic realm="Nexus"'


def test_auth_wrong_password_returns_401(monkeypatch):
    c = _client_with_auth(monkeypatch, "admin", "secret")
    resp = c.get("/", auth=("admin", "wrong"))
    assert resp.status_code == 401


def test_auth_wrong_username_returns_401(monkeypatch):
    c = _client_with_auth(monkeypatch, "admin", "secret")
    resp = c.get("/", auth=("nobody", "secret"))
    assert resp.status_code == 401


def test_auth_correct_credentials_returns_200(monkeypatch):
    c = _client_with_auth(monkeypatch, "admin", "secret")
    resp = c.get("/", auth=("admin", "secret"))
    assert resp.status_code == 200


def test_auth_correct_credentials_returns_html(monkeypatch):
    c = _client_with_auth(monkeypatch, "admin", "secret")
    resp = c.get("/", auth=("admin", "secret"))
    assert "text/html" in resp.headers["content-type"]
    assert "Nexus" in resp.text


# ── /api/meta ─────────────────────────────────────────────────────────────────

def test_api_meta_returns_prefect_ui_url(client):
    resp = client.get("/api/meta")
    assert resp.status_code == 200
    assert "prefect_ui_url" in resp.json()


# ── /api/config ───────────────────────────────────────────────────────────────

def test_api_config_returns_project_and_empty_includes(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text("project: test-project\n")
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/config")
    assert resp.status_code == 200
    data = resp.json()
    assert data["project"] == "test-project"
    assert data["env_keys"] == []
    assert data["includes"] == []


def test_api_config_returns_env_keys_without_values(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text("project: p\nenv:\n  SECRET: hunter2\n  HOST: localhost\n")
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/config")
    assert resp.status_code == 200
    assert set(resp.json()["env_keys"]) == {"SECRET", "HOST"}


def test_api_config_returns_include_details(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text(
        "project: p\nincludes:\n  api: github.com/org/api@v1\n"
    )
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/config")
    assert resp.status_code == 200
    incs = resp.json()["includes"]
    assert len(incs) == 1
    assert incs[0]["name"] == "api"
    assert incs[0]["repo"] == "github.com/org/api"
    assert incs[0]["ref"] == "v1"


def test_api_config_returns_500_when_config_missing(tmp_path, monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/config")
    assert resp.status_code == 500
    assert "error" in resp.json()


# ── /api/apps ─────────────────────────────────────────────────────────────────

def test_api_apps_empty_when_no_includes(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text("project: p\n")
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/apps")
    assert resp.status_code == 200
    assert resp.json() == []


def test_api_apps_reports_missing_clone(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text(
        "project: p\nincludes:\n  api: github.com/org/api\n"
    )
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/apps")
    assert resp.status_code == 200
    data = resp.json()
    assert len(data) == 1
    assert data[0]["name"] == "api"
    assert data[0]["exists"] is False
    assert data[0]["sha"] == ""


def test_api_apps_reports_sha_when_cloned(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text(
        "project: p\nincludes:\n  api: github.com/org/api\n"
    )
    app_dir = tmp_path / "apps" / "api"
    app_dir.mkdir(parents=True)
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    fake_run = MagicMock()
    fake_run.return_value.stdout = "abc1234\n"
    monkeypatch.setattr(web_module.subprocess, "run", fake_run)
    resp = TestClient(web_module.app).get("/api/apps")
    assert resp.status_code == 200
    data = resp.json()
    assert data[0]["exists"] is True
    assert data[0]["sha"] == "abc1234"


def test_api_apps_returns_empty_list_when_config_missing(tmp_path, monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "NEXUS_HOME", tmp_path)
    resp = TestClient(web_module.app).get("/api/apps")
    assert resp.status_code == 200
    assert resp.json() == []


# ── /api/processes proxy ──────────────────────────────────────────────────────

def test_api_processes_returns_503_when_pc_unavailable(monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "PC_BASE", "http://localhost:19999")
    resp = TestClient(web_module.app, raise_server_exceptions=False).get("/api/processes")
    assert resp.status_code == 503
    assert "error" in resp.json()


def test_api_start_returns_503_when_pc_unavailable(monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "PC_BASE", "http://localhost:19999")
    resp = TestClient(web_module.app, raise_server_exceptions=False).post("/api/process/start/foo")
    assert resp.status_code == 503


def test_api_stop_returns_503_when_pc_unavailable(monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "PC_BASE", "http://localhost:19999")
    resp = TestClient(web_module.app, raise_server_exceptions=False).patch("/api/process/stop/foo")
    assert resp.status_code == 503


def test_api_restart_returns_503_when_pc_unavailable(monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "PC_BASE", "http://localhost:19999")
    resp = TestClient(web_module.app, raise_server_exceptions=False).post("/api/process/restart/foo")
    assert resp.status_code == 503


def test_api_logs_returns_503_when_pc_unavailable(monkeypatch):
    import nexus.web as web_module
    monkeypatch.setattr(web_module, "PC_BASE", "http://localhost:19999")
    resp = TestClient(web_module.app, raise_server_exceptions=False).get("/api/process/logs/foo")
    assert resp.status_code == 503
