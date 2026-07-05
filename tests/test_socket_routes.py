"""
End-to-end tests for the socket API's nested-address routes.

Project addresses (external sub-projects) and inline service names contain
slashes. These routes are dispatched through a single {rest...} wildcard, so this
verifies detail/history for a nested project, and log/restart for a nested
(inline) service, all work over the real socket.
"""

import time

from conftest import GitRepo

DB_YAML = """\
services:
  store:
    run: sleep 3600
"""

APP_YAML = """\
services:
  api:
    run: sleep 3600
projects:
  metrics:
    services:
      exporter:
        run: sh -c 'echo EXPORTER_STARTED; exec sleep 3600'
"""


def test_nested_project_detail_and_history(nexus, tmp_path):
    db = GitRepo(tmp_path / "db")
    db.commit({"nexus.yaml": DB_YAML})
    root = GitRepo(tmp_path / "root")
    root.commit({"nexus.yaml": f"projects:\n  db:\n    src: {db.spec_path}\n    ref: \"@main\"\n"})

    nexus.add_project(root.spec_path, "root")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_list_entry("root/db", healthy=True, timeout=60)

    # Detail of a nested external sub-project (previously unreachable by route).
    detail = nexus.client.get_project("root/db")
    assert isinstance(detail, dict), f"unexpected detail: {detail}"
    assert detail["name"] == "root/db"
    assert detail["current_sha"], f"no sha in detail: {detail}"
    assert detail["health"] == "healthy"

    # History of the nested sub-project.
    history = nexus.client.get_history("root/db")
    assert isinstance(history, list) and any(
        e["status"] == "active" for e in history
    ), f"no active deployment in nested history: {history}"


def _exporter(nexus):
    for s in nexus.client.list_services("app"):
        if s["name"] == "metrics/exporter":
            return s
    return None


def test_inline_service_log_and_restart(nexus, git_repo):
    git_repo.commit({"nexus.yaml": APP_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start()
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # Wait for the inline service to be running with a PID.
    deadline = time.monotonic() + 30
    svc = None
    while time.monotonic() < deadline:
        svc = _exporter(nexus)
        if svc and svc.get("pid"):
            break
        time.sleep(0.5)
    assert svc and svc.get("pid"), f"inline service not running: {svc}"
    pid1 = svc["pid"]

    # Its log is reachable via the nested service-log route.
    deadline = time.monotonic() + 15
    log = ""
    while time.monotonic() < deadline:
        log = nexus.client.get_log("app", "metrics/exporter")
        if isinstance(log, str) and "EXPORTER_STARTED" in log:
            break
        time.sleep(0.5)
    assert "EXPORTER_STARTED" in log, f"log did not contain marker: {log!r}"

    # Restart the inline service via the nested restart route.
    status, _ = nexus.client.restart_service("app", "metrics/exporter")
    assert status == 202, f"restart should be accepted, got {status}"

    # A new process comes up with a different PID.
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        svc = _exporter(nexus)
        if svc and svc.get("pid") and svc["pid"] != pid1:
            break
        time.sleep(0.5)
    else:
        raise AssertionError(f"inline service PID did not change after restart (stayed {pid1})")

    assert _exporter(nexus)["running"], "inline service not running after restart"
