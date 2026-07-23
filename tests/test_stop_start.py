"""
End-to-end tests for `nexus project stop` / `start`.

Stopping a project takes its services (and nested sub-projects) down for a
maintenance window while keeping its registration and current SHA in the DB, so
it stays down across a daemon restart until started — distinct from `remove`,
which forgets the project entirely.
"""

import time

YAML = "services:\n  web:\n    run: sleep 3600\n"


def _wait_gone(nexus, name, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if nexus.list_summary(name) is None:
            return True
        time.sleep(1)
    return nexus.list_summary(name) is None


def _wait_live(nexus, name, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if nexus.list_summary(name) is not None:
            return True
        time.sleep(1)
    return nexus.list_summary(name) is not None


def test_stop_and_start_keeps_registration(nexus, git_repo):
    git_repo.commit({"nexus.yaml": YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    sha = nexus.wait_for_sha("app")
    assert nexus.list_summary("app") is not None, "project should be live after deploy"

    # Stop: services come down and it drops out of the daemon's live list...
    nexus.cli("project", "stop", "app")
    assert _wait_gone(nexus, "app"), "project still live after stop"

    # ...but it is still tracked in the DB and shown as stopped.
    out = nexus.cli("project", "list").stdout
    assert "app" in out and "stopped" in out, out

    # Start: it recovers from the same SHA.
    nexus.cli("project", "start", "app")
    assert _wait_live(nexus, "app"), "project not live after start"
    assert nexus.wait_for_sha("app") == sha


def test_stopped_persists_across_daemon_restart(nexus, git_repo):
    git_repo.commit({"nexus.yaml": YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    nexus.cli("project", "stop", "app")
    assert _wait_gone(nexus, "app"), "project still live after stop"

    # Restart the whole stack. A stopped project must NOT come back on its own.
    nexus.stop()
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    time.sleep(5)
    assert nexus.list_summary("app") is None, "stopped project came back after restart"
    # Still tracked, still marked stopped.
    assert "stopped" in nexus.cli("project", "list").stdout

    # Explicitly starting it brings it back.
    nexus.cli("project", "start", "app")
    assert _wait_live(nexus, "app"), "project did not resume after start"
