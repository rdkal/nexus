"""
End-to-end test for adding/removing a project while the daemon is running.

Previously the daemon only loaded projects at startup, so `nexus project add`
after install did nothing until a restart. Now `add`/`remove` notify the daemon
(POST /projects) which reconciles: it starts newly-added root projects and stops
removed ones, live.
"""

import time

YAML = "services:\n  api:\n    run: sleep 3600\n"


def test_add_and_remove_project_while_running(nexus, git_repo):
    git_repo.commit({"nexus.yaml": YAML})

    # Start the daemon with NO projects (as a fresh install would be).
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    assert nexus.client.list_projects() == [], "daemon should start with no projects"

    # Add a project AFTER the daemon is running, then reconcile — exactly what
    # `nexus project add` does (DB write + POST /projects).
    nexus.add_project(git_repo.spec_path, "app")
    status, _ = nexus.client.reconcile()
    assert status == 202

    # It deploys without any daemon restart.
    nexus.wait_for_sha("app", timeout=60)
    svc = next(s for s in nexus.client.list_services("app") if s["name"] == "api")
    assert svc["running"], f"service not running after live add: {svc}"

    # Remove it from the DB + reconcile — the daemon stops it.
    import sqlite3

    conn = sqlite3.connect(str(nexus.db_path))
    conn.execute("DELETE FROM projects WHERE name = 'app'")
    conn.commit()
    conn.close()
    status, _ = nexus.client.reconcile()
    assert status == 202

    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if nexus.list_summary("app") is None:
            break
        time.sleep(1)
    assert nexus.list_summary("app") is None, "project still present after live remove"
