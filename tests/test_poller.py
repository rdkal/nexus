"""Tests for the poller's change detection and deploy pipeline."""
import textwrap
from pathlib import Path

import pytest

from nexus.config import IncludeConfig
from nexus.poller import local_head, remote_head, update_app


# ── change detection ──────────────────────────────────────────────────────────

def test_no_change_detected(make_app):
    app = make_app("myapp")
    rhead = remote_head(app.active, "main")
    lhead = local_head(app.active)
    assert rhead == lhead


def test_change_detected_after_push(make_app):
    app = make_app("myapp")
    sha_before = remote_head(app.active, "main")

    app.push_update({"README.md": "v2"})

    sha_after = remote_head(app.active, "main")
    assert sha_after != sha_before
    assert local_head(app.active) == sha_before  # active not updated yet


# ── update_app — flows-only app (no processes) ────────────────────────────────

def test_update_flows_only_app(make_app, nexus_home, fake_process_compose):
    """Flows-only app deploys without touching process-compose."""
    nexus_yaml = "flows:\n  hello: flows/hello.py:run\n"
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    app.push_update({"flows/hello.py": "from prefect import flow\n\n@flow\ndef run(): print('v2')\n"})

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is True
    assert fake_process_compose.stopped == []
    assert fake_process_compose.started == []
    assert app.active_sha() == app.remote_sha()


# ── update_app — app with processes ──────────────────────────────────────────

def test_update_stops_and_starts_processes(make_app, nexus_home, fake_process_compose):
    """App with processes: poller stops them, updates, restarts them."""
    nexus_yaml = "processes:\n  web: process-compose.yaml\n"
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    fake_process_compose.processes = ["myapp-web", "myapp-worker"]

    app.push_update({"nexus.yaml": nexus_yaml, "v": "2"})

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is True
    assert set(fake_process_compose.stopped) == {"myapp-web", "myapp-worker"}
    assert set(fake_process_compose.started) == {"myapp-web", "myapp-worker"}
    assert app.active_sha() == app.remote_sha()


def test_update_active_dir_missing(nexus_home, fake_process_compose):
    """Returns False without error when active dir doesn't exist."""
    inc = IncludeConfig(name="ghost", repo="/nonexistent/repo.git")
    result = update_app(inc, nexus_home=nexus_home)
    assert result is False


def test_no_nexus_yaml_aborts(make_app, nexus_home, fake_process_compose, tmp_path):
    """App repo with no nexus.yaml is skipped."""
    import subprocess
    from tests.conftest import GIT_ENV
    app = make_app("myapp")
    subprocess.run(["git", "-C", str(app._scratch), "rm", "nexus.yaml"],
                   check=True, capture_output=True, env=GIT_ENV)
    subprocess.run(["git", "-C", str(app._scratch), "commit", "-m", "remove nexus.yaml"],
                   check=True, capture_output=True, env=GIT_ENV)
    subprocess.run(["git", "-C", str(app._scratch), "push"],
                   check=True, capture_output=True, env=GIT_ENV)

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)
    assert result is False


# ── deploy gates ──────────────────────────────────────────────────────────────

def test_root_gate_pass_allows_deploy(make_app, nexus_home, fake_process_compose):
    """Root deploy gate that succeeds lets deploy proceed."""
    nexus_yaml = textwrap.dedent("""\
        deploy:
          - check

        flows:
          hello: flows/hello.py:run
          check: gates/check.py:run_check
    """)
    app = make_app("myapp", nexus_yaml=nexus_yaml)

    # Push an update that includes a passing gate
    app.push_update({
        "nexus.yaml": nexus_yaml,
        "gates/check.py": "from prefect import flow\n\n@flow\ndef run_check(): pass\n",
    })

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is True


def test_root_gate_fail_aborts_deploy(make_app, nexus_home, fake_process_compose):
    """Root deploy gate that raises keeps current version running."""
    nexus_yaml = textwrap.dedent("""\
        deploy:
          - check

        flows:
          hello: flows/hello.py:run
          check: gates/check.py:run_check
    """)
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    fake_process_compose.processes = ["myapp-web"]

    sha_before = app.active_sha()

    app.push_update({
        "nexus.yaml": nexus_yaml,
        "gates/check.py": "from prefect import flow\n\n@flow\ndef run_check(): raise RuntimeError('tests failed')\n",
    })

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is False
    assert fake_process_compose.stopped == []   # nothing was touched
    assert app.active_sha() == sha_before       # active dir unchanged


def test_per_process_gate_fail_aborts(make_app, nexus_home, fake_process_compose):
    """Per-process gate failure prevents process restart."""
    nexus_yaml = textwrap.dedent("""\
        flows:
          check: gates/check.py:run_check

        processes:
          web:
            file: process-compose.yaml
            deploy:
              - check
    """)
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    fake_process_compose.processes = ["myapp-web"]

    app.push_update({
        "nexus.yaml": nexus_yaml,
        "gates/check.py": "from prefect import flow\n\n@flow\ndef run_check(): raise RuntimeError('smoke failed')\n",
    })

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is False
    assert fake_process_compose.stopped == []


def test_unknown_gate_aborts(make_app, nexus_home, fake_process_compose):
    """Referencing a gate flow name that isn't in flows dict aborts deploy."""
    nexus_yaml = "deploy:\n  - nonexistent-flow\n"
    app = make_app("myapp", nexus_yaml=nexus_yaml)

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)
    assert result is False


# ── per-flow deploy gates ─────────────────────────────────────────────────────

def test_per_flow_gate_pass_allows_deploy(make_app, nexus_home, fake_process_compose):
    """Per-flow gate that passes lets the deploy proceed."""
    nexus_yaml = textwrap.dedent("""\
        flows:
          ingest:
            entrypoint: flows/ingest.py:run
            deploy:
              - check
          check: gates/check.py:run_check
    """)
    app = make_app("myapp", nexus_yaml=nexus_yaml)

    app.push_update({
        "nexus.yaml": nexus_yaml,
        "gates/check.py": "from prefect import flow\n\n@flow\ndef run_check(): pass\n",
        "flows/ingest.py": "from prefect import flow\n\n@flow\ndef run(): pass\n",
    })

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is True
    assert app.active_sha() == app.remote_sha()


def test_per_flow_gate_fail_aborts_deploy(make_app, nexus_home, fake_process_compose):
    """Per-flow gate failure aborts deploy and keeps current version."""
    nexus_yaml = textwrap.dedent("""\
        flows:
          ingest:
            entrypoint: flows/ingest.py:run
            deploy:
              - check
          check: gates/check.py:run_check
    """)
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    sha_before = app.active_sha()

    app.push_update({
        "nexus.yaml": nexus_yaml,
        "gates/check.py": "from prefect import flow\n\n@flow\ndef run_check(): raise RuntimeError('flow gate failed')\n",
        "flows/ingest.py": "from prefect import flow\n\n@flow\ndef run(): pass\n",
    })

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is False
    assert app.active_sha() == sha_before


def test_per_flow_gate_runs_before_process_changes(make_app, nexus_home, fake_process_compose):
    """Per-flow gate failure prevents process stop/start even with processes declared."""
    nexus_yaml = textwrap.dedent("""\
        flows:
          ingest:
            entrypoint: flows/ingest.py:run
            deploy:
              - check
          check: gates/check.py:run_check

        processes:
          web: process-compose.yaml
    """)
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    fake_process_compose.processes = ["myapp-web"]

    app.push_update({
        "nexus.yaml": nexus_yaml,
        "gates/check.py": "from prefect import flow\n\n@flow\ndef run_check(): raise RuntimeError('flow gate failed')\n",
        "flows/ingest.py": "from prefect import flow\n\n@flow\ndef run(): pass\n",
    })

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is False
    assert fake_process_compose.stopped == []
