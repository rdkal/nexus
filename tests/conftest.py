import os
import shutil
import signal
import socket
import subprocess
import textwrap
import time
from dataclasses import dataclass, field
from pathlib import Path

import httpx
import pytest

REPO_ROOT = Path(__file__).parent.parent
INSTALL_SH = REPO_ROOT / "install.sh"
FIXTURES = Path(__file__).parent / "fixtures"

# Git identity injected into every git subprocess so commits work without
# a global ~/.gitconfig in CI.
GIT_ENV = {
    **os.environ,
    "GIT_AUTHOR_NAME": "nexus-test",
    "GIT_AUTHOR_EMAIL": "test@nexus.local",
    "GIT_COMMITTER_NAME": "nexus-test",
    "GIT_COMMITTER_EMAIL": "test@nexus.local",
}


# ── git helpers ───────────────────────────────────────────────────────────────

def _git(cwd: Path, *args: str) -> None:
    subprocess.run(["git", "-C", str(cwd), *args],
                   check=True, env=GIT_ENV, capture_output=True)


def _git_out(cwd: Path, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", str(cwd), *args],
        check=True, env=GIT_ENV, capture_output=True, text=True,
    ).stdout.strip()


# ── LocalApp — a fake app repo with a local bare remote ──────────────────────

@dataclass
class LocalApp:
    """
    A local git app wired up so nexus can work with it:
      bare/    — the bare remote (acts as origin)
      active/  — nexus's working clone (NEXUS_HOME/apps/<name>)
      _scratch/ — a separate clone for making test commits
    """
    name: str
    bare: Path
    active: Path
    _scratch: Path

    def push_update(self, files: dict[str, str], message: str = "update") -> str:
        """Write files into scratch, commit, push to bare. Returns new SHA."""
        for rel, content in files.items():
            p = self._scratch / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(content)
        _git(self._scratch, "add", ".")
        _git(self._scratch, "commit", "-m", message)
        _git(self._scratch, "push")
        return _git_out(self._scratch, "rev-parse", "HEAD")

    def active_sha(self) -> str:
        return _git_out(self.active, "rev-parse", "HEAD")

    def remote_sha(self) -> str:
        return _git_out(self.bare, "rev-parse", "HEAD")


_DEFAULT_NEXUS_YAML = "flows:\n  hello: flows/hello.py:run\n"
_DEFAULT_FLOW_PY = (
    "from prefect import flow\n\n"
    "@flow\ndef run(): pass\n"
)
_MINIMAL_PYPROJECT = (
    '[project]\nname = "test-app"\nversion = "0.1.0"\n'
    'requires-python = ">=3.11"\n'
    'dependencies = ["prefect>=3.0"]\n'
)


@pytest.fixture
def nexus_home(tmp_path):
    """Function-scoped empty nexus home directory."""
    home = tmp_path / "nexus_home"
    (home / "apps").mkdir(parents=True)
    (home / "config.yaml").write_text("project: test\n")
    return home


@pytest.fixture
def make_app(tmp_path, nexus_home):
    """
    Factory fixture — call it once per app you need in a test.

    Usage::
        def test_something(make_app):
            app = make_app("myapp")
            app = make_app("myapp", nexus_yaml="processes:\\n  web: pc.yaml\\n")
    """
    def _make(name: str, nexus_yaml: str = _DEFAULT_NEXUS_YAML) -> LocalApp:
        bare = tmp_path / f"{name}.git"
        scratch = tmp_path / f"{name}-scratch"
        active = nexus_home / "apps" / name

        # Init bare repo with main as default branch
        subprocess.run(["git", "init", "--bare", str(bare)],
                       check=True, env=GIT_ENV, capture_output=True)
        _git(bare, "symbolic-ref", "HEAD", "refs/heads/main")

        # Seed via scratch clone
        subprocess.run(["git", "clone", str(bare), str(scratch)],
                       check=True, env=GIT_ENV, capture_output=True)

        (scratch / "nexus.yaml").write_text(nexus_yaml)
        (scratch / "flows").mkdir(exist_ok=True)
        (scratch / "flows" / "hello.py").write_text(_DEFAULT_FLOW_PY)
        (scratch / "pyproject.toml").write_text(_MINIMAL_PYPROJECT)

        _git(scratch, "add", ".")
        _git(scratch, "commit", "-m", "init")
        _git(scratch, "push", "--set-upstream", "origin", "main")

        # Active clone — mirrors what setup.py does
        subprocess.run(["git", "clone", str(bare), str(active)],
                       check=True, env=GIT_ENV, capture_output=True)

        return LocalApp(name=name, bare=bare, active=active, _scratch=scratch)

    return _make


# ── FakeProcessCompose — intercepts poller's stop/start calls ─────────────────

@dataclass
class FakeProcessCompose:
    processes: list[str] = field(default_factory=list)
    stopped: list[str] = field(default_factory=list)
    started: list[str] = field(default_factory=list)


# ── shared Prefect server — started once per session ─────────────────────────

@dataclass
class PrefectServer:
    api_url: str


@pytest.fixture(scope="session")
def prefect_server(tmp_path_factory):
    """
    Start one Prefect server for the whole test session so gate flows connect
    over HTTP rather than each spawning their own ephemeral server (~20 s each).
    """
    prefect_home = tmp_path_factory.mktemp("prefect")

    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]

    api_url = f"http://127.0.0.1:{port}/api"

    proc = subprocess.Popen(
        ["uv", "run", "prefect", "server", "start",
         "--host", "127.0.0.1", "--port", str(port)],
        env={**os.environ, "PREFECT_HOME": str(prefect_home)},
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )

    deadline = time.monotonic() + 90
    ready = False
    while time.monotonic() < deadline:
        try:
            if httpx.get(f"{api_url}/health", timeout=1).status_code == 200:
                ready = True
                break
        except Exception:
            pass
        time.sleep(0.5)

    if not ready:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        except Exception:
            pass
        pytest.fail("Prefect server did not start within 90 s")

    # Create the nexus-pool work pool that deployments reference
    try:
        httpx.post(
            f"{api_url}/work_pools",
            json={"name": "nexus-pool", "type": "process"},
            timeout=10,
            follow_redirects=True,
        )
    except Exception:
        pass  # may already exist on re-use

    yield PrefectServer(api_url=api_url)

    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        proc.wait(timeout=10)
    except Exception:
        pass


@pytest.fixture
def fake_process_compose(monkeypatch, tmp_path, prefect_server):
    """
    Replaces the three process-compose API functions in poller with in-memory
    fakes. Prepopulate `fake.processes` with names the poller should see.

    Points PREFECT_API_URL at the session-scoped Prefect server so gate flows
    connect over HTTP rather than spinning up an ephemeral server each time.
    """
    import nexus.poller as poller_mod

    fake = FakeProcessCompose()

    monkeypatch.setattr(
        poller_mod, "app_processes",
        lambda name: [p for p in fake.processes if p.startswith(f"{name}-")],
    )
    monkeypatch.setattr(
        poller_mod, "stop_process",
        lambda name: fake.stopped.append(name),
    )
    monkeypatch.setattr(
        poller_mod, "start_process",
        lambda name: fake.started.append(name),
    )
    monkeypatch.setattr(poller_mod, "PREFECT_API_URL", prefect_server.api_url)
    monkeypatch.setenv("PREFECT_HOME", str(tmp_path / "prefect"))
    return fake


# ── real_process_compose — live process-compose server for integration tests ──

@dataclass
class RealProcessCompose:
    port: int


@pytest.fixture
def real_process_compose(tmp_path, monkeypatch):
    """
    Start a real process-compose server with test processes and point the
    poller at it.  Skipped if the binary isn't on PATH.

    Processes exposed:
        myapp-web, myapp-worker  (sleep 3600, stoppable/restartable)
        _sentinel                (sleep 86400, keeps PC alive while others stop)

    After stop: status == "Completed", is_running == False
    After start: status == "Running",  is_running == True
    """
    pc_bin = shutil.which("process-compose")
    if pc_bin is None:
        pytest.skip("process-compose not found")

    with socket.socket() as s:
        s.bind(("", 0))
        port = s.getsockname()[1]

    pc_yaml = tmp_path / "processes.yaml"
    pc_yaml.write_text(textwrap.dedent("""\
        version: "0.5"
        processes:
          _sentinel:
            command: sleep 86400
          myapp-web:
            command: sleep 3600
          myapp-worker:
            command: sleep 3600
    """))

    proc = subprocess.Popen(
        [pc_bin, "-f", str(pc_yaml), "-p", str(port), "-t=false", "up"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )

    deadline = time.monotonic() + 30
    ready = False
    while time.monotonic() < deadline:
        try:
            if httpx.get(f"http://localhost:{port}/processes", timeout=1).status_code == 200:
                ready = True
                break
        except Exception:
            pass
        time.sleep(0.25)

    if not ready:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        except Exception:
            pass
        pytest.fail("process-compose did not start within 30s")

    import nexus.poller as poller_mod
    monkeypatch.setattr(poller_mod, "PC_PORT", port)
    monkeypatch.setattr(poller_mod, "PC_BASE", f"http://localhost:{port}")

    yield RealProcessCompose(port=port)

    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        proc.wait(timeout=5)
    except Exception:
        pass


# ── E2E fixture — runs the full install.sh and waits for port 8080 ────────────

def _port_open(port: int, timeout: float = 1.0) -> bool:
    try:
        with socket.create_connection(("localhost", port), timeout=timeout):
            return True
    except OSError:
        return False


def _wait_for_port(port: int, deadline: float) -> bool:
    while time.monotonic() < deadline:
        if _port_open(port):
            return True
        time.sleep(0.5)
    return False


def _kill_group(proc: subprocess.Popen) -> None:
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        proc.wait(timeout=8)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass


@pytest.fixture(scope="session")
def running_nexus(tmp_path_factory):
    """
    Session fixture: runs install.sh with an empty config (no apps) and waits
    for nexus-web on port 8080. process-compose is auto-installed by install.sh
    if not already present.
    """
    home = tmp_path_factory.mktemp("e2e")
    config_file = FIXTURES / "nexus.yaml"
    log_file = home / "install.log"

    with open(log_file, "w") as log:
        proc = subprocess.Popen(
            ["bash", str(INSTALL_SH), "--home", str(home), str(config_file)],
            start_new_session=True,
            stdout=log,
            stderr=subprocess.STDOUT,
        )

    deadline = time.monotonic() + 60
    if not _wait_for_port(8080, deadline):
        output = log_file.read_text(errors="replace")
        _kill_group(proc)
        pytest.fail(f"Nexus web server did not start within 60s.\n\n{output}")

    yield home

    _kill_group(proc)


# ── subprocess call timing ────────────────────────────────────────────────────

_subprocess_calls: list[tuple[str, str, float]] = []  # (nodeid, label, secs)


@pytest.fixture(autouse=True)
def _time_subprocesses(monkeypatch, request):
    original = subprocess.run

    def _timed(*args, **kwargs):
        cmd = args[0] if args else kwargs.get("args", [])
        if isinstance(cmd, (list, tuple)):
            parts = [str(x) for x in cmd]
            label = " ".join(parts[:4]) + (" …" if len(parts) > 4 else "")
        else:
            label = str(cmd)[:60]
        t0 = time.perf_counter()
        result = original(*args, **kwargs)
        elapsed = time.perf_counter() - t0
        if elapsed > 0.3:
            _subprocess_calls.append((request.node.nodeid, label, elapsed))
        return result

    monkeypatch.setattr(subprocess, "run", _timed)
    yield


def pytest_terminal_summary(terminalreporter, exitstatus, config):
    if not _subprocess_calls:
        return
    terminalreporter.write_sep("=", "slowest subprocess calls (> 0.3 s)")
    for nodeid, label, secs in sorted(_subprocess_calls, key=lambda x: -x[2])[:20]:
        short = nodeid.split("::")[-1]
        terminalreporter.write_line(f"  {secs:5.2f}s  {label}  [{short}]")
