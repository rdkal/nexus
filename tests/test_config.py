"""Unit tests for nexus.yaml parsing — no filesystem or subprocesses needed."""
import textwrap
from pathlib import Path

import pytest

from nexus.config import FlowConfig, NexusConfig, ProcessConfig, load_config


@pytest.fixture
def cfg(tmp_path):
    """Write a nexus.yaml string and return the parsed NexusConfig."""
    def _load(text: str) -> NexusConfig:
        p = tmp_path / "nexus.yaml"
        p.write_text(textwrap.dedent(text))
        return load_config(p)
    return _load


# ── root config ───────────────────────────────────────────────────────────────

def test_root_minimal(cfg):
    c = cfg("project: my-project\n")
    assert c.project == "my-project"
    assert c.includes == []
    assert c.flows == {}
    assert c.processes == {}
    assert c.deploy == []


def test_include_shorthand(cfg):
    c = cfg("""
        project: p
        includes:
          api: https://github.com/org/api
    """)
    assert len(c.includes) == 1
    inc = c.includes[0]
    assert inc.name == "api"
    assert inc.repo == "https://github.com/org/api"
    assert inc.branch == "main"
    assert inc.poll_interval == 60


def test_include_full_form(cfg):
    c = cfg("""
        project: p
        includes:
          api:
            repo: https://github.com/org/api
            branch: develop
            poll_interval: 30
    """)
    inc = c.includes[0]
    assert inc.branch == "develop"
    assert inc.poll_interval == 30


def test_multiple_includes(cfg):
    c = cfg("""
        project: p
        includes:
          api: https://github.com/org/api
          workers:
            repo: https://github.com/org/workers
            branch: staging
    """)
    names = [i.name for i in c.includes]
    assert names == ["api", "workers"]


# ── flows ─────────────────────────────────────────────────────────────────────

def test_flow_shorthand(cfg):
    c = cfg("flows:\n  ingest: flows/ingest.py:run\n")
    assert "ingest" in c.flows
    f = c.flows["ingest"]
    assert isinstance(f, FlowConfig)
    assert f.entrypoint == "flows/ingest.py:run"
    assert f.deploy == []


def test_flow_full_form(cfg):
    c = cfg("""
        flows:
          ingest:
            entrypoint: flows/ingest.py:run
            deploy:
              - run-tests
    """)
    f = c.flows["ingest"]
    assert f.entrypoint == "flows/ingest.py:run"
    assert f.deploy == ["run-tests"]


# ── processes ─────────────────────────────────────────────────────────────────

def test_process_shorthand(cfg):
    c = cfg("processes:\n  web: process-compose.yaml\n")
    assert "web" in c.processes
    p = c.processes["web"]
    assert isinstance(p, ProcessConfig)
    assert p.file == "process-compose.yaml"
    assert p.deploy == []


def test_process_full_form(cfg):
    c = cfg("""
        processes:
          jobs:
            file: jobs-compose.yaml
            deploy:
              - run-tests
              - smoke-test
    """)
    p = c.processes["jobs"]
    assert p.file == "jobs-compose.yaml"
    assert p.deploy == ["run-tests", "smoke-test"]


# ── deploy gates ──────────────────────────────────────────────────────────────

def test_root_deploy_gates(cfg):
    c = cfg("""
        deploy:
          - run-tests
          - lint
    """)
    assert c.deploy == ["run-tests", "lint"]


def test_deploy_absent_is_empty(cfg):
    c = cfg("project: p\n")
    assert c.deploy == []


def test_deploy_null_is_empty(cfg):
    c = cfg("deploy:\n")
    assert c.deploy == []


def test_empty_file_returns_empty_config(cfg):
    c = cfg("")
    assert c.project is None
    assert c.includes == []
    assert c.flows == {}
    assert c.processes == {}
    assert c.deploy == []


# ── app nexus.yaml (no project, no includes) ──────────────────────────────────

def test_app_config_no_project(cfg):
    c = cfg("""
        flows:
          hello: flows/hello.py:run
        processes:
          web: process-compose.yaml
    """)
    assert c.project is None
    assert c.includes == []


def test_full_app_config(cfg):
    c = cfg("""
        deploy:
          - run-tests

        flows:
          ingest: flows/ingest.py:ingest
          run-tests: tests/run.py:run_all

        processes:
          web: process-compose.yaml
          jobs:
            file: jobs-compose.yaml
            deploy:
              - run-tests
    """)
    assert c.deploy == ["run-tests"]
    assert c.flows["ingest"].entrypoint == "flows/ingest.py:ingest"
    assert c.flows["run-tests"].deploy == []
    assert c.processes["web"].file == "process-compose.yaml"
    assert c.processes["jobs"].deploy == ["run-tests"]
