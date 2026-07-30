"""
End-to-end tests for tasks (scheduled & triggered) — DESIGN "Tasks".

Covers: a scheduled task fires and records a successful run; a task with after:
runs after its upstream succeeds; a failed task does NOT run its dependent; and a
manual trigger (nexus task run) fires a task on demand.
"""

import time


def _runs(nexus, address):
    # Tolerate transient socket errors while the runtime is restarting.
    try:
        runs = nexus.client.list_task_runs(address)
    except Exception:
        return []
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


SURVIVE_YAML = """\
tasks:
  long:
    run: sh -c 'sleep 6; echo done'
  after_long:
    after: long
    run: sh -c 'true'
"""


def test_task_survives_runtime_restart(nexus, git_repo):
    # A task runs under nexus-pm, so killing the nexus runtime mid-task must NOT
    # kill it — and the new runtime must recover its outcome and fire the cascade.
    git_repo.commit({"nexus.yaml": SURVIVE_YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    # Start the long task, then kill the runtime while it's still sleeping.
    nexus.client.run_task("app", "long")
    assert _wait_run(nexus, "app", "long"), "long task did not start"
    time.sleep(1)
    nexus.kill_runtime()

    # nexus-pm restarts the runtime; wait for the socket to come back.
    nexus.wait_for_socket()

    # The new runtime recovers the in-flight run: long finishes success and its
    # after: dependent fires — all across the restart.
    long_run = _wait_run(nexus, "app", "long", status="success", timeout=30)
    assert long_run, "long task was not recovered as success after runtime restart"
    after = _wait_run(nexus, "app", "after_long", status="success", timeout=20)
    assert after and after["reason"] == "after:long", f"cascade did not survive restart: {after}"
