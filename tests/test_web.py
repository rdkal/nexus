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
import urllib.error
import urllib.request
from pathlib import Path

import pytest

from conftest import GitRepo

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


def _http_post(url: str, timeout: float = 5.0):
    req = urllib.request.Request(url, method="POST", headers={"FX-Request": "true"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
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


def test_web_actions_restart_and_redeploy(nexus, git_repo, web_server):
    git_repo.commit({"nexus.yaml": APP_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start()
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    def exporter():
        for s in nexus.client.list_services("app"):
            if s["name"] == "metrics/exporter":
                return s
        return None

    deadline = time.monotonic() + 20
    while time.monotonic() < deadline and not (exporter() and exporter().get("pid")):
        time.sleep(0.5)
    pid1 = exporter()["pid"]
    assert pid1, "inline service has no PID"

    # Restart the inline service through the web action (POST on its page URL).
    status, body = _http_post(web_server + "/app/metrics/exporter")
    assert status == 200, body
    assert "Restarted" in body

    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        cur = exporter()
        if cur and cur.get("pid") and cur["pid"] != pid1:
            break
        time.sleep(0.5)
    else:
        raise AssertionError(f"service PID did not change after web restart (stayed {pid1})")

    # Redeploy the project through the web action.
    status, body = _http_post(web_server + "/app")
    assert status == 200, body
    assert "Redeploy queued" in body


def test_web_build_log_page(nexus, git_repo, web_server):
    git_repo.commit(
        {"nexus.yaml": "build: echo BUILD_MARKER_XYZ\nservices:\n  api:\n    run: sleep 3600\n"}
    )
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start()
    nexus.wait_for_socket()
    sha = nexus.wait_for_sha("app")

    # The project page's history links each SHA to its build log.
    status, body = _http_get(web_server + "/app")
    assert status == 200, body
    assert f"/app/builds/{sha}" in body

    # The build-log page shows the captured build output.
    status, body = _http_get(web_server + f"/app/builds/{sha}")
    assert status == 200, body
    assert "Build log" in body and "BUILD_MARKER_XYZ" in body


def _web_app_files() -> dict:
    """The nexus-web package source, as a {path: content} dict for a GitRepo."""
    files = {}
    for p in sorted((WEB_DIR / "nexus_web").glob("*.py")):
        files[f"nexus_web/{p.name}"] = p.read_text()
    files["requirements.txt"] = (WEB_DIR / "requirements.txt").read_text()
    return files


def test_dogfood_nexus_deploys_web(nexus, tmp_path, web_python):
    """nexus builds AND runs nexus-web as a normal project — the design's claim.

    Depends on web_python so the build is skipped when the venv can't be built
    (no network) and so the iris wheel is already in pip's cache, keeping the
    in-nexus `pip install` fast.
    """
    port = _free_port()
    repo = GitRepo(tmp_path / "webrepo")
    files = _web_app_files()
    # Install deps into ./libs and run with PYTHONPATH. (The production
    # web/nexus.yaml uses a plain venv; here the test's file:// spec path puts a
    # ':' in the worktree path, which `python -m venv` refuses — real
    # github.com/... paths have no colon — so --target sidesteps that artifact
    # while still proving nexus builds and runs the app.)
    files["nexus.yaml"] = (
        "build: python3 -m pip install -q --target ./libs -r requirements.txt\n"
        "services:\n"
        "  web:\n"
        f"    run: PYTHONPATH=libs python3 -m nexus_web --socket $NEXUS_HOME/nexus.sock --port {port}\n"
    )
    repo.commit(files)

    nexus.add_project(repo.spec_path, "web")
    nexus.start()
    nexus.wait_for_socket()

    # The build creates a venv and pip-installs iris from git — allow generous time.
    try:
        nexus.wait_for_sha("web", timeout=300)
    except TimeoutError:
        logs = sorted((nexus.home / "logs" / "web").glob("*-build.log"))
        tail = logs[-1].read_text()[-2000:] if logs else "(no build log found)"
        raise AssertionError(f"web project did not deploy; build log tail:\n{tail}")

    # nexus-web, now supervised by nexus, serves its own dashboard — which lists
    # itself as the "web" project.
    base = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + 60
    status, body = 0, ""
    while time.monotonic() < deadline:
        try:
            status, body = _http_get(base + "/", timeout=2.0)
            if status == 200 and "web" in body:
                break
        except Exception:
            pass
        time.sleep(1)
    assert status == 200, f"web UI did not serve on {base}"
    assert "Projects" in body and "web" in body
