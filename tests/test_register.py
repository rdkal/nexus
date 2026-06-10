"""Tests for Prefect flow auto-registration."""
import httpx
import pytest

from nexus.config import FlowConfig, IncludeConfig, NexusConfig
from nexus.register import WORK_POOL, register_app_flows


def _config(flows=None) -> NexusConfig:
    return NexusConfig(
        project=None,
        includes=[],
        flows=flows or {},
        processes={},
        deploy=[],
    )


def _get_deployments(api_url: str, name: str) -> list[dict]:
    r = httpx.post(
        f"{api_url}/deployments/filter",
        json={"deployments": {"name": {"any_": [name]}}},
        timeout=10,
        follow_redirects=True,
    )
    r.raise_for_status()
    return r.json()


# ── no-op ─────────────────────────────────────────────────────────────────────

def test_no_flows_creates_no_deployments(tmp_path, prefect_server):
    register_app_flows("noop-app", tmp_path, _config(), prefect_server.api_url)
    assert _get_deployments(prefect_server.api_url, "noop-app-hello") == []


# ── single flow ───────────────────────────────────────────────────────────────

def test_register_creates_deployment(tmp_path, prefect_server):
    (tmp_path / "flows").mkdir()
    (tmp_path / "flows" / "hello.py").write_text(
        "from prefect import flow\n\n@flow\ndef run(): pass\n"
    )
    config = _config(flows={"hello": FlowConfig(entrypoint="flows/hello.py:run")})

    register_app_flows("reg-app", tmp_path, config, prefect_server.api_url)

    deps = _get_deployments(prefect_server.api_url, "reg-app-hello")
    assert len(deps) == 1
    d = deps[0]
    assert d["name"] == "reg-app-hello"
    assert d["work_pool_name"] == WORK_POOL
    assert d["entrypoint"] == "flows/hello.py:run"
    assert d["path"] == str(tmp_path)


def test_register_is_idempotent(tmp_path, prefect_server):
    (tmp_path / "flows").mkdir()
    (tmp_path / "flows" / "hello.py").write_text(
        "from prefect import flow\n\n@flow\ndef run(): pass\n"
    )
    config = _config(flows={"hello": FlowConfig(entrypoint="flows/hello.py:run")})

    register_app_flows("idem-app", tmp_path, config, prefect_server.api_url)
    register_app_flows("idem-app", tmp_path, config, prefect_server.api_url)

    assert len(_get_deployments(prefect_server.api_url, "idem-app-hello")) == 1


# ── multiple flows ────────────────────────────────────────────────────────────

def test_register_multiple_flows(tmp_path, prefect_server):
    flows_dir = tmp_path / "flows"
    flows_dir.mkdir()
    for fn in ("ingest", "export"):
        (flows_dir / f"{fn}.py").write_text(
            f"from prefect import flow\n\n@flow\ndef {fn}(): pass\n"
        )
    config = _config(flows={
        "ingest": FlowConfig(entrypoint="flows/ingest.py:ingest"),
        "export": FlowConfig(entrypoint="flows/export.py:export"),
    })

    register_app_flows("multi-app", tmp_path, config, prefect_server.api_url)

    for name in ("ingest", "export"):
        deps = _get_deployments(prefect_server.api_url, f"multi-app-{name}")
        assert len(deps) == 1, f"missing deployment for {name}"
        assert deps[0]["work_pool_name"] == WORK_POOL


# ── update_app triggers re-registration ───────────────────────────────────────

def test_update_app_registers_flows(make_app, nexus_home, fake_process_compose, prefect_server):
    """After a successful update, flows are registered as Prefect deployments."""
    from nexus.config import IncludeConfig
    from nexus.poller import update_app

    app = make_app("flow-reg-app")
    inc = IncludeConfig(name="flow-reg-app", repo=str(app.bare))

    # Push a new commit so update_app has something to do
    app.push_update({"README.md": "v2"})

    update_app(inc, nexus_home)

    deps = _get_deployments(prefect_server.api_url, "flow-reg-app-hello")
    assert len(deps) == 1
    assert deps[0]["entrypoint"] == "flows/hello.py:run"
