"""End-to-end test for `nexus project set-src` — repointing a project at a new
git location (its repo moved) without losing its name or history.
"""

import time

from conftest import GitRepo

SVC_A = "services:\n  web_a:\n    run: sleep 3600\n"
SVC_B = "services:\n  web_b:\n    run: sleep 3600\n"


def test_set_src_rebuilds_from_new_location(nexus, tmp_path):
    a = GitRepo(tmp_path / "a")
    a.commit({"nexus.yaml": SVC_A})

    nexus.add_project(a.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_list_entry("app", healthy=True, timeout=90)
    assert any(s["name"] == "web_a" for s in nexus.client.list_services("app")), "original service not running"

    # The project's repository moves to a new location (with a distinguishable
    # service so we can prove the deployment actually switched).
    b = GitRepo(tmp_path / "b")
    b.commit({"nexus.yaml": SVC_B})
    nexus.cli("project", "set-src", "app", b.spec_path)

    # The daemon rebuilds from B live: the stored src updates and B's service runs.
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        summ = nexus.list_summary("app")
        svcs = nexus.client.list_services("app")
        names = {s["name"] for s in svcs} if isinstance(svcs, list) else set()
        if summ and summ.get("spec_path") == b.spec_path and "web_b" in names:
            break
        time.sleep(1)
    else:
        raise AssertionError(
            f"set-src did not rebuild from B: summary={nexus.list_summary('app')} "
            f"services={nexus.client.list_services('app')}"
        )

    # The old repo's service is gone — the deployment came entirely from B.
    assert not any(s["name"] == "web_a" for s in nexus.client.list_services("app")), "old service still present"
