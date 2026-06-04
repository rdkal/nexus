import os
import signal
import socket
import subprocess
import time
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).parent.parent
INSTALL_SH = REPO_ROOT / "install.sh"
FIXTURES = Path(__file__).parent / "fixtures"


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
def nexus_home(tmp_path_factory):
    return tmp_path_factory.mktemp("nexus_home")


@pytest.fixture(scope="session")
def running_nexus(nexus_home):
    config_file = FIXTURES / "nexus.yaml"
    log_file = nexus_home / "install.log"

    with open(log_file, "w") as log:
        proc = subprocess.Popen(
            ["bash", str(INSTALL_SH), "--home", str(nexus_home), str(config_file)],
            start_new_session=True,
            stdout=log,
            stderr=subprocess.STDOUT,
        )

    # nexus-web has no depends_on so it starts right away; allow 60s
    deadline = time.monotonic() + 60
    if not _wait_for_port(8080, deadline):
        output = log_file.read_text(errors="replace")
        _kill_group(proc)
        pytest.fail(f"Nexus web server did not start within 60s.\n\n{output}")

    yield nexus_home

    _kill_group(proc)
