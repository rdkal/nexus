"""
End-to-end tests for tasks (scheduled & triggered) — DESIGN "Tasks".

Covers: a scheduled task fires and records a successful run; a task with after:
runs after its upstream succeeds; a failed task does NOT run its dependent; and a
manual trigger (nexus task run) fires a task on demand.
"""

import time


def _runs(nexus, address):
    runs = nexus.client.list_task_runs(address)
    return runs if isinstance(runs, list) else []


def _wait_run(nexus, address, task, status=None, timeout=25):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        for r in _runs(nexus, address):
            if r["task"] == task and (status is None or r["status"] == status):
                return r
        time.sleep(1)
    return None


SCHEDULED_YAML = """\
tasks:
  first:
    schedule: "@every 2s"
    run: sh -c 'true'
  second:
    after: first
    run: sh -c 'true'
"""


def test_scheduled_task_fires_and_cascades(nexus, git_repo):
    git_repo.commit({"nexus.yaml": SCHEDULED_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # The scheduled task fires on its own and succeeds.
    assert _wait_run(nexus, "app", "first", status="success"), "scheduled task never succeeded"
    # ...and its after: dependent runs, triggered by that success.
    second = _wait_run(nexus, "app", "second", status="success")
    assert second and second["reason"] == "after:first", f"cascade run wrong: {second}"


FAILURE_YAML = """\
tasks:
  boom:
    schedule: "@every 2s"
    run: sh -c 'exit 3'
  wont_run:
    after: boom
    run: sh -c 'true'
"""


def test_failure_stops_the_chain(nexus, git_repo):
    git_repo.commit({"nexus.yaml": FAILURE_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    boom = _wait_run(nexus, "app", "boom", status="failed")
    assert boom and boom["exit_code"] == 3, f"boom should fail with exit 3: {boom}"
    # Give the (wrong) cascade ample time to not happen.
    time.sleep(5)
    assert not any(r["task"] == "wont_run" for r in _runs(nexus, "app")), "dependent of a failed task ran"


MANUAL_YAML = """\
tasks:
  greet:
    run: sh -c 'true'
"""


def test_manual_trigger(nexus, git_repo):
    git_repo.commit({"nexus.yaml": MANUAL_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # Nothing runs until triggered.
    time.sleep(3)
    assert not _runs(nexus, "app"), "manual-only task ran unprompted"

    # Trigger via the CLI.
    nexus.cli("task", "run", "app/greet")
    run = _wait_run(nexus, "app", "greet", status="success")
    assert run and run["reason"] == "manual", f"manual run wrong: {run}"
