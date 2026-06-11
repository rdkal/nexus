"""Unit tests for nexus-web basic auth."""
import base64

import pytest
from fastapi.testclient import TestClient


def _basic(username: str, password: str) -> str:
    token = base64.b64encode(f"{username}:{password}".encode()).decode()
    return f"Basic {token}"


@pytest.fixture(autouse=True)
def _patch_env(monkeypatch):
    """Each test starts with auth disabled; individual tests override as needed."""
    monkeypatch.setenv("NEXUS_USER", "")
    monkeypatch.setenv("NEXUS_PASSWORD", "")


@pytest.fixture()
def client():
    import nexus.web as web_module
    return TestClient(web_module.app, raise_server_exceptions=True)


def _client_with_auth(monkeypatch, username: str, password: str):
    monkeypatch.setenv("NEXUS_USER", username)
    monkeypatch.setenv("NEXUS_PASSWORD", password)
    # Re-import to pick up the new env values (module-level constants).
    import importlib
    import nexus.web as web_module
    importlib.reload(web_module)
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
