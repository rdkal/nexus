"""End-to-end test for nexus-web against a live daemon socket.

Builds (once per session) the web app's venv from web/requirements.txt, launches
`python -m nexus_web` pointed at a real nexus socket, deploys a project, and
asserts the rendered pages reflect it. This exercises the whole stack:
iris → httpx → nexus.sock.
"""

import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

import pytest

WEB_DIR = Path(__file__).resolve().parents[1] / "web"

APP_YAML = """\
services:
  api:
    run: sleep 3600
projects:
  metrics:
    services:
      exporter:
        run: sh -c 'echo EXPORTER_UP; exec sleep 3600'
"""


def _free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _http_get(url: str, timeout: float = 5.0):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


@pytest.fixture(scope="session")
def web_python():
    """Path to a venv python with the web app's deps; skips if it can't be built."""
    venv = WEB_DIR / ".venv"
    py = venv / "bin" / "python"

    def works() -> bool:
        if not py.exists():
            return False
        r = subprocess.run(
            [str(py), "-c", "import iris, httpx, uvicorn, fastapi"],
            capture_output=True,
        )
        return r.returncode == 0

    if not works():
        subprocess.run([sys.executable, "-m", "venv", str(venv)], check=True)
        r = subprocess.run(
            [str(py), "-m", "pip", "install", "-q", "-r", str(WEB_DIR / "requirements.txt")],
            capture_output=True,
            text=True,
        )
        if r.returncode != 0 or not works():
            pytest.skip(f"could not build web venv: {r.stderr[-800:]}")
    return str(py)


@pytest.fixture
def web_server(nexus, web_python, tmp_path):
    """Launch nexus-web against the nexus fixture's socket; yield its base URL."""
    port = _free_port()
    log = open(tmp_path / "web.log", "w")
    proc = subprocess.Popen(
        [
            web_python, "-m", "nexus_web",
            "--socket", str(nexus.socket_path),
            "--host", "127.0.0.1",
            "--port", str(port),
        ],
        cwd=str(WEB_DIR),
        stdout=log,
        stderr=subprocess.STDOUT,
    )
    base = f"http://127.0.0.1:{port}"
    try:
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise RuntimeError(f"web server exited early; see {tmp_path/'web.log'}")
            try:
                status, body = _http_get(base + "/healthz", timeout=1.0)
                if status == 200 and body.strip() == "ok":
                    break
            except Exception:
                pass
            time.sleep(0.3)
        else:
            raise TimeoutError("web /healthz not ready")
        yield base
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        log.close()


def test_web_renders_project_tree_and_details(nexus, git_repo, web_server):
    git_repo.commit({"nexus.yaml": APP_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start()
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # Overview lists the project.
    status, body = _http_get(web_server + "/")
    assert status == 200, body
    assert "app" in body and "Projects" in body

    # Project detail lists both the top-level and inline services.
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        status, body = _http_get(web_server + "/app")
        if status == 200 and "api" in body and "metrics/exporter" in body:
            break
        time.sleep(1)
    assert status == 200, body
    assert "api" in body and "metrics/exporter" in body
    assert "History" in body

    # Service page shows the live log (captured stdout marker).
    deadline = time.monotonic() + 20
    body = ""
    while time.monotonic() < deadline:
        status, body = _http_get(web_server + "/app/metrics/exporter")
        if status == 200 and "EXPORTER_UP" in body:
            break
        time.sleep(1)
    assert status == 200, body
    assert "EXPORTER_UP" in body
    assert 'id="log"' in body

    # Unknown path → 404 page.
    status, _ = _http_get(web_server + "/does/not/exist")
    assert status == 404
