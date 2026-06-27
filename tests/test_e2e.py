"""
End-to-end tests for the nexus daemon stack.

Each test starts a fresh nexus-pm + nexus-runtime pair with its own NEXUS_HOME.
Tests use local bare git repos (file:// URLs) as project sources so they run
offline and fast.
"""

import time

import pytest


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

NEXUS_YAML_SLEEP = """\
services:
  web:
    run: sleep 3600
"""

NEXUS_YAML_FAIL_BUILD = """\
build: "false"
"""


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_service_starts_after_deploy(nexus, git_repo):
    """A service defined in nexus.yaml should be running after a successful deploy."""
    git_repo.commit({"nexus.yaml": NEXUS_YAML_SLEEP})

    nexus.add_project(git_repo.spec_path, "myproject")
    nexus.start()
    nexus.wait_for_socket()
    nexus.wait_for_sha("myproject")

    services = nexus.client.list_services("myproject")
    assert isinstance(services, list), f"expected list, got: {services}"
    assert len(services) == 1, f"expected 1 service, got: {services}"
    svc = services[0]
    assert svc["name"] == "web"
    assert svc["running"] is True, f"service not running: {svc}"


def test_deployment_recorded_in_history(nexus, git_repo):
    """A successful deploy must create an 'active' entry in deployment history."""
    git_repo.commit({"nexus.yaml": NEXUS_YAML_SLEEP})

    nexus.add_project(git_repo.spec_path, "myproject")
    nexus.start()
    nexus.wait_for_socket()
    nexus.wait_for_sha("myproject")

    history = nexus.client.get_history("myproject")
    assert isinstance(history, list) and len(history) >= 1, f"empty history: {history}"
    active = [e for e in history if e["status"] == "active"]
    assert active, f"no active deployment in history: {history}"


def test_failed_build_does_not_promote_sha(nexus, git_repo):
    """A nexus.yaml with a failing build command must leave current_sha empty."""
    git_repo.commit({"nexus.yaml": NEXUS_YAML_FAIL_BUILD})

    nexus.add_project(git_repo.spec_path, "badproject")
    nexus.start()
    nexus.wait_for_socket()

    # Wait until history shows a failed deployment.
    nexus.wait_for_history_status("badproject", "failed")

    project = nexus.client.get_project("badproject")
    assert isinstance(project, dict), f"unexpected response: {project}"
    assert project.get("current_sha", "") == "", (
        f"current_sha should be empty after failed build, got: {project}"
    )


def test_new_commit_triggers_redeploy(nexus, git_repo):
    """Pushing a new commit to the upstream repo must trigger an automatic redeploy."""
    sha1 = git_repo.commit({"nexus.yaml": NEXUS_YAML_SLEEP})

    nexus.add_project(git_repo.spec_path, "myproject")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # Wait for the first deploy to promote sha1.
    current = nexus.wait_for_sha("myproject")
    assert current == sha1, f"expected sha1={sha1}, got {current}"

    # Push a new commit and wait for the poller to pick it up and deploy.
    sha2 = git_repo.commit(
        {"nexus.yaml": NEXUS_YAML_SLEEP, "marker": "v2"},
        message="bump to v2",
    )
    assert sha2 != sha1

    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        try:
            project = nexus.client.get_project("myproject")
            if isinstance(project, dict) and project.get("current_sha") == sha2:
                break
        except Exception:
            pass
        time.sleep(1)
    else:
        project = nexus.client.get_project("myproject")
        pytest.fail(
            f"project was not redeployed to sha2={sha2} within 60s; "
            f"current_sha={project.get('current_sha')}"
        )

    services = nexus.client.list_services("myproject")
    assert any(s["running"] for s in services), f"no running service after redeploy: {services}"
