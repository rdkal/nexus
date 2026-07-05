"""
End-to-end tests for inline sub-projects.

An inline sub-project is a `projects:` entry with no `src:`. It shares the parent's
worktree and deploys atomically with it — its services start under a nested address
(`<parent>/<alias>/<service>`) as part of the same deployment, with no independent
polling. These tests verify inline services deploy with their parent, are observable
under the parent's address, and redeploy together with the parent.
"""

import time

ROOT_YAML = """\
services:
  api:
    run: sleep 3600
projects:
  metrics:
    services:
      exporter:
        run: sleep 3600
"""


def test_inline_services_deploy_with_parent(nexus, git_repo):
    git_repo.commit({"nexus.yaml": ROOT_YAML})

    nexus.add_project(git_repo.spec_path, "app")
    nexus.start()
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # The inline sub-project has no independent project entry; its services appear
    # under the parent's address with a nested name.
    deadline = time.monotonic() + 30
    services = []
    while time.monotonic() < deadline:
        services = nexus.client.list_services("app")
        names = {s["name"] for s in services} if isinstance(services, list) else set()
        if {"api", "metrics/exporter"} <= names:
            break
        time.sleep(1)
    else:
        raise AssertionError(f"inline service not listed under parent: {services}")

    by_name = {s["name"]: s for s in services}
    assert by_name["api"]["running"], f"root service not running: {by_name['api']}"
    assert by_name["metrics/exporter"]["running"], (
        f"inline service not running: {by_name['metrics/exporter']}"
    )
    # The inline service's supervisor key is the fully nested address.
    assert by_name["metrics/exporter"]["key"] == "app/metrics/exporter"

    # The project is healthy overall (health spans inline services).
    assert nexus.list_summary("app")["health"] == "healthy"


def test_inline_project_redeploys_with_parent(nexus, git_repo):
    sha1 = git_repo.commit({"nexus.yaml": ROOT_YAML})

    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # Record the inline service PID, then push a new parent commit.
    def exporter_pid():
        for s in nexus.client.list_services("app"):
            if s["name"] == "metrics/exporter":
                return s.get("pid")
        return None

    deadline = time.monotonic() + 30
    while time.monotonic() < deadline and not exporter_pid():
        time.sleep(0.5)
    pid1 = exporter_pid()
    assert pid1, "inline service never reported a PID"

    sha2 = git_repo.commit({"nexus.yaml": ROOT_YAML, "bump": "2"}, message="v2")
    assert sha2 != sha1
    nexus.wait_for_project_sha("app", sha2)

    # The inline service redeployed with the parent — new worktree, new process.
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        pid2 = exporter_pid()
        if pid2 and pid2 != pid1:
            break
        time.sleep(0.5)
    else:
        raise AssertionError(
            f"inline service did not redeploy with parent (pid stayed {pid1})"
        )

    assert nexus.list_summary("app")["health"] == "healthy"
