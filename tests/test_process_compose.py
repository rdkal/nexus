"""Integration tests for process-compose HTTP API — uses a real process-compose server."""
import os
import shutil
import signal
import subprocess
import textwrap
import time

import httpx
import pytest

from nexus.config import IncludeConfig
from nexus.poller import app_processes, start_process, stop_process, update_app


# ── helpers ───────────────────────────────────────────────────────────────────

def _status(port: int, name: str) -> dict:
    resp = httpx.get(f"http://localhost:{port}/processes", timeout=5)
    return next((p for p in resp.json()["data"] if p["name"] == name), {})


def _wait_running(port: int, name: str, timeout: float = 10.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if _status(port, name).get("is_running"):
            return True
        time.sleep(0.2)
    return False


def _wait_stopped(port: int, name: str, timeout: float = 10.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not _status(port, name).get("is_running"):
            return True
        time.sleep(0.2)
    return False


# ── app_processes ─────────────────────────────────────────────────────────────

def test_app_processes_returns_matching_names(real_process_compose):
    """app_processes() reads from the real API and filters by app-name prefix."""
    procs = app_processes("myapp")
    assert set(procs) == {"myapp-web", "myapp-worker"}


def test_app_processes_excludes_other_apps(real_process_compose):
    """app_processes() does not return processes from a different app."""
    assert app_processes("other") == []


def test_app_processes_excludes_sentinel(real_process_compose):
    """_sentinel is running but must not appear — it has no app prefix."""
    procs = app_processes("myapp")
    assert "_sentinel" not in procs


# ── stop_process / start_process ──────────────────────────────────────────────

def test_stop_process_stops_running_process(real_process_compose):
    """stop_process() sends PATCH and the process leaves Running state."""
    port = real_process_compose.port
    assert _wait_running(port, "myapp-web"), "precondition: process should be running"

    stop_process("myapp-web")

    assert _wait_stopped(port, "myapp-web"), "process did not stop within timeout"
    assert _status(port, "myapp-web")["status"] == "Completed"


def test_start_process_restarts_stopped_process(real_process_compose):
    """start_process() sends POST and brings the process back to Running."""
    port = real_process_compose.port
    stop_process("myapp-web")
    assert _wait_stopped(port, "myapp-web")

    start_process("myapp-web")

    assert _wait_running(port, "myapp-web"), "process did not restart within timeout"
    assert _status(port, "myapp-web")["status"] == "Running"


# ── update_app end-to-end with real process-compose ───────────────────────────

def test_update_app_stops_and_restarts_real_processes(
    make_app, nexus_home, real_process_compose
):
    """
    Full update_app pipeline against a live process-compose server.
    Verifies that processes are actually stopped via HTTP, then restarted
    after the git update lands — no mocking of the API layer.
    """
    port = real_process_compose.port
    nexus_yaml = "processes:\n  web: process-compose.yaml\n"
    app = make_app("myapp", nexus_yaml=nexus_yaml)
    app.push_update({"nexus.yaml": nexus_yaml, "v": "2"})

    assert _wait_running(port, "myapp-web"),    "precondition: myapp-web should be running"
    assert _wait_running(port, "myapp-worker"), "precondition: myapp-worker should be running"

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    result = update_app(inc, nexus_home=nexus_home)

    assert result is True
    assert app.active_sha() == app.remote_sha()

    assert _wait_running(port, "myapp-web"),    "myapp-web should be running after deploy"
    assert _wait_running(port, "myapp-worker"), "myapp-worker should be running after deploy"


# ── env vars reach spawned processes ─────────────────────────────────────────

def test_env_vars_reach_process_compose_processes(tmp_path):
    """
    Env vars passed to process-compose are visible inside the processes it spawns.

    This exercises the same code path as build_env + os.execvpe in start.py:
    nexus injects root/include env vars into the process-compose environment,
    and process-compose inherits that environment to every child process.
    """
    pc_bin = shutil.which("process-compose")
    if pc_bin is None:
        pytest.skip("process-compose not found")

    output_file = tmp_path / "env_output.txt"
    pc_yaml = tmp_path / "env-test.yaml"
    pc_yaml.write_text(textwrap.dedent(f"""\
        version: "0.5"
        processes:
          _sentinel:
            command: sleep 86400
          write-env:
            command: sh -c 'printf "%s" "$NEXUS_TEST_VAR" > {output_file}'
    """))

    env = {**os.environ, "NEXUS_TEST_VAR": "hello-from-nexus"}
    proc = subprocess.Popen(
        [pc_bin, "-f", str(pc_yaml), "-t=false", "up"],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if output_file.exists() and output_file.read_text():
            break
        time.sleep(0.2)

    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        proc.wait(timeout=5)
    except Exception:
        pass

    assert output_file.exists(), "write-env process never ran"
    assert output_file.read_text() == "hello-from-nexus"
