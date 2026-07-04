"""
Pytest fixtures for nexus end-to-end tests.

Stack under test:
  nexus-pm (process, owns nexus-pm.sock)
    └─ nexus daemon (supervised process, owns nexus.sock)
         └─ user services (supervised via nexus-pm)

Approach:
  1. Build binaries once (session scope).
  2. Per test: fresh NEXUS_HOME, git repo, sqlite project entry, start nexus-pm.
  3. NexusClient talks HTTP over nexus.sock (Unix domain socket).
  4. Projects are added directly via sqlite3 so file:// spec paths work without
     hitting the CLI argument parser's single-colon split limitation.
"""

import http.client
import json
import os
import shutil
import socket
import sqlite3
import subprocess
import time
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).parent.parent

# ---------------------------------------------------------------------------
# HTTP over Unix socket
# ---------------------------------------------------------------------------


class _UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, socket_path: str):
        super().__init__("localhost")
        self._socket_path = socket_path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(self._socket_path)


class NexusClient:
    """Thin HTTP client that talks to the nexus daemon over its Unix socket."""

    def __init__(self, socket_path: str):
        self._socket = socket_path

    def _request(self, method: str, path: str, body=None):
        conn = _UnixHTTPConnection(self._socket)
        headers: dict = {}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        conn.request(method, path, body=data, headers=headers)
        resp = conn.getresponse()
        raw = resp.read()
        try:
            return resp.status, json.loads(raw)
        except json.JSONDecodeError:
            return resp.status, raw.decode()

    def list_projects(self):
        _, body = self._request("GET", "/projects")
        return body

    def get_project(self, name: str):
        _, body = self._request("GET", f"/projects/{name}")
        return body

    def list_services(self, name: str):
        _, body = self._request("GET", f"/projects/{name}/services")
        return body

    def get_history(self, name: str):
        _, body = self._request("GET", f"/projects/{name}/history")
        return body

    def redeploy(self, name: str):
        status, body = self._request("POST", f"/projects/{name}/redeploy")
        return status, body


# ---------------------------------------------------------------------------
# Session fixture: build binaries once
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def built_binaries(tmp_path_factory):
    """Build nexus and nexus-pm once for the entire test session."""
    out = tmp_path_factory.mktemp("nexus-bins")
    for cmd in ("nexus", "nexus-pm"):
        subprocess.run(
            ["go", "build", "-o", str(out / cmd), f"./cmd/{cmd}"],
            cwd=str(REPO_ROOT),
            check=True,
        )
    return out


# ---------------------------------------------------------------------------
# Git repo helper
# ---------------------------------------------------------------------------


class GitRepo:
    """
    Pair of bare + working-clone git repos for testing.

    spec_path uses file:// so it is valid as a git URL and does not get absorbed
    by filepath.Join when used as a key inside NEXUS_HOME/repos/.
    """

    def __init__(self, base: Path):
        self.bare = base / "repo.git"
        self.work = base / "work"
        self.spec_path = f"file://{self.bare}"

        subprocess.run(
            ["git", "init", "--bare", "--initial-branch=main", str(self.bare)],
            check=True, capture_output=True,
        )
        subprocess.run(
            ["git", "clone", str(self.bare), str(self.work)],
            check=True, capture_output=True,
        )
        for key, value in [("user.email", "test@nexus"), ("user.name", "Nexus Test")]:
            subprocess.run(
                ["git", "-C", str(self.work), "config", key, value],
                check=True, capture_output=True,
            )
        # Ensure the working clone is on `main` (git may default to `master`).
        subprocess.run(
            ["git", "-C", str(self.work), "checkout", "-B", "main"],
            check=True, capture_output=True,
        )

    def commit(self, files: dict, message: str = "test commit") -> str:
        """Write files dict {filename: content}, commit, and push. Returns SHA."""
        for filename, content in files.items():
            path = self.work / filename
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
            subprocess.run(
                ["git", "-C", str(self.work), "add", filename],
                check=True, capture_output=True,
            )
        subprocess.run(
            ["git", "-C", str(self.work), "commit", "-m", message],
            check=True, capture_output=True,
        )
        subprocess.run(
            ["git", "-C", str(self.work), "push"],
            check=True, capture_output=True,
        )
        result = subprocess.run(
            ["git", "-C", str(self.work), "rev-parse", "HEAD"],
            check=True, capture_output=True, text=True,
        )
        return result.stdout.strip()


@pytest.fixture
def git_repo(tmp_path):
    return GitRepo(tmp_path / "git")


# ---------------------------------------------------------------------------
# Nexus fixture: manages a full nexus-pm + nexus-runtime stack
# ---------------------------------------------------------------------------


_NEXUS_SCHEMA = """
CREATE TABLE IF NOT EXISTS projects (
    name        TEXT PRIMARY KEY,
    spec_path   TEXT NOT NULL,
    ref         TEXT NOT NULL DEFAULT '@main',
    current_sha TEXT
);
CREATE TABLE IF NOT EXISTS deployments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    address     TEXT NOT NULL,
    sha         TEXT NOT NULL,
    status      TEXT NOT NULL,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE TABLE IF NOT EXISTS services (
    address         TEXT PRIMARY KEY,
    status          TEXT NOT NULL,
    pid             INTEGER,
    restart_count   INTEGER NOT NULL DEFAULT 0,
    last_exit_code  INTEGER,
    last_exit_at    INTEGER
);
"""


def _add_project_to_db(db_path: Path, name: str, spec_path: str, ref: str = "@main"):
    """Bootstrap the nexus schema (if needed) and insert a project record."""
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(db_path))
    conn.executescript(_NEXUS_SCHEMA)
    conn.execute(
        "INSERT INTO projects (name, spec_path, ref) VALUES (?, ?, ?)",
        (name, spec_path, ref),
    )
    conn.commit()
    conn.close()


class NexusFixture:
    def __init__(self, home: Path, bin_dir: Path, env: dict, source_bin_dir: Path):
        self._home = home
        self._bin_dir = bin_dir
        self._env = dict(env)
        self._pm_proc = None
        self.client = NexusClient(str(home / "nexus.sock"))
        # Pristine session-built binaries (never swapped at runtime). Useful as a
        # stand-in "new" nexus binary for self-update tests.
        self.nexus_source = source_bin_dir / "nexus"

    @property
    def db_path(self) -> Path:
        return self._home / "nexus.db"

    def add_project(self, spec_path: str, name: str, ref: str = "@main"):
        """Register a project in the DB before the daemon starts."""
        _add_project_to_db(self.db_path, name, spec_path, ref)

    def start(self, poll_interval: str = "2s", extra_env: dict | None = None):
        """Start nexus-pm, which auto-starts the nexus daemon."""
        env = dict(self._env)
        env["NEXUS_POLL_INTERVAL"] = poll_interval
        if extra_env:
            env.update(extra_env)
        self._pm_proc = subprocess.Popen(
            [str(self._bin_dir / "nexus-pm")],
            env=env,
        )

    def stop(self):
        if self._pm_proc is not None and self._pm_proc.poll() is None:
            self._pm_proc.terminate()
            try:
                self._pm_proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self._pm_proc.kill()
                self._pm_proc.wait()

    def wait_for_socket(self, timeout: float = 15.0):
        """Block until nexus.sock exists and accepts connections."""
        sock_path = str(self._home / "nexus.sock")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if (self._home / "nexus.sock").exists():
                try:
                    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                    s.settimeout(1)
                    s.connect(sock_path)
                    s.close()
                    return
                except OSError:
                    pass
            time.sleep(0.1)
        raise TimeoutError(f"nexus.sock not ready after {timeout}s")

    def wait_for_sha(self, project: str, timeout: float = 45.0) -> str:
        """Block until the project has a non-empty current_sha. Returns it."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                p = self.client.get_project(project)
                if isinstance(p, dict) and p.get("current_sha"):
                    return p["current_sha"]
            except Exception:
                pass
            time.sleep(1)
        raise TimeoutError(f"project {project!r} not deployed within {timeout}s")

    def list_summary(self, address: str) -> dict | None:
        """Return the /projects list entry for a given address, or None.

        Works for nested addresses (e.g. "root/db") which cannot be fetched via
        the parametric /projects/{name} route.
        """
        projects = self.client.list_projects()
        if not isinstance(projects, list):
            return None
        for p in projects:
            if p.get("name") == address:
                return p
        return None

    def wait_for_list_entry(
        self, address: str, timeout: float = 45.0, healthy: bool = False
    ) -> dict:
        """Block until address appears in /projects (optionally healthy with a SHA)."""
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            try:
                last = self.list_summary(address)
                if last is not None:
                    if not healthy:
                        return last
                    if last.get("current_sha") and last.get("health") == "healthy":
                        return last
            except Exception:
                pass
            time.sleep(1)
        raise TimeoutError(
            f"address {address!r} not present/healthy within {timeout}s (last={last})"
        )

    def wait_for_list_gone(self, address: str, timeout: float = 45.0):
        """Block until address is no longer present in /projects."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if self.list_summary(address) is None:
                    return
            except Exception:
                pass
            time.sleep(1)
        raise TimeoutError(f"address {address!r} still present after {timeout}s")

    def wait_for_project_sha(self, project: str, sha: str, timeout: float = 60.0):
        """Block until the project's current_sha equals sha.

        Tolerates transient socket failures — the daemon may be mid-restart.
        """
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                p = self.client.get_project(project)
                if isinstance(p, dict) and p.get("current_sha") == sha:
                    return
            except Exception:
                pass
            time.sleep(0.5)
        raise TimeoutError(
            f"project {project!r} did not reach sha={sha} within {timeout}s"
        )

    def service_pid(self, project: str, service: str) -> str | None:
        """Return the PID string of a running service, or None if not found."""
        services = self.client.list_services(project)
        if not isinstance(services, list):
            return None
        for s in services:
            if s.get("name") == service:
                return s.get("pid")
        return None

    def wait_for_history_status(
        self, project: str, status: str, timeout: float = 45.0
    ) -> list:
        """Block until history contains at least one entry with the given status."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                entries = self.client.get_history(project)
                if isinstance(entries, list) and any(
                    e.get("status") == status for e in entries
                ):
                    return entries
            except Exception:
                pass
            time.sleep(1)
        raise TimeoutError(
            f"project {project!r} history did not reach status={status!r} within {timeout}s"
        )


@pytest.fixture
def nexus(tmp_path, built_binaries):
    """
    Fresh NexusFixture per test: own NEXUS_HOME, copied binaries, stopped on teardown.
    """
    home = tmp_path / "nexus_home"
    home.mkdir()
    bin_dir = home / "bin"
    bin_dir.mkdir()

    for name in ("nexus", "nexus-pm"):
        dst = bin_dir / name
        shutil.copy(built_binaries / name, dst)
        dst.chmod(0o755)

    env = {
        k: v
        for k, v in os.environ.items()
        if k in ("PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "GIT_EXEC_PATH")
    }
    env["NEXUS_HOME"] = str(home)

    fix = NexusFixture(home, bin_dir, env, built_binaries)
    yield fix
    fix.stop()
