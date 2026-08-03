"""End-to-end tests for managed runs (nexus run) — DESIGN "Managed runs".

A managed run is an imperative, run-once, durable host operation. These cover:
it runs and its outcome + log are recorded; and the headline property — it
survives a nexus runtime restart (it runs under nexus-pm).
"""

import time


def _run(nexus, name):
    for r in nexus.client.list_runs():
        if r["name"] == name:
            return r
    return None


def _wait_run(nexus, name, status=None, timeout=25):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            r = _run(nexus, name)
        except Exception:
            r = None  # tolerate a transient socket error during a restart
        if r and (status is None or r["status"] == status):
            return r
        time.sleep(1)
    return None


def test_run_completes_and_captures_log(nexus):
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # An unattached run (cwd is NEXUS_HOME, under no project worktree).
    status, _ = nexus.client.start_run(
        "hello", "sh -c 'echo RUN_MARKER'", str(nexus.home)
    )
    assert status == 201, f"start_run status {status}"

    run = _wait_run(nexus, "hello", status="success")
    assert run, "run never succeeded"
    assert run["address"] == "", f"expected unattached, got {run['address']!r}"
    assert "RUN_MARKER" in nexus.client.run_log("hello")


def test_run_stop(nexus):
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    status, _ = nexus.client.start_run("slow", "sh -c 'sleep 60'", str(nexus.home))
    assert status == 201
    assert _wait_run(nexus, "slow"), "run did not start"

    status, _ = nexus.client.stop_run("slow")
    assert status == 202, f"stop status {status}"

    # A stopped run terminates and is recorded distinctly as cancelled.
    run = _wait_run(nexus, "slow", status="cancelled", timeout=20)
    assert run, "stopped run was not recorded as cancelled"


def test_run_survives_runtime_restart(nexus):
    # A managed run is a child of nexus-pm, so killing the nexus runtime mid-run
    # must not kill it; the new runtime recovers and records its outcome.
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    status, _ = nexus.client.start_run(
        "long", "sh -c 'sleep 6; echo SURVIVED'", str(nexus.home)
    )
    assert status == 201
    assert _wait_run(nexus, "long"), "run did not start"

    time.sleep(1)
    nexus.kill_runtime()
    nexus.wait_for_socket()

    run = _wait_run(nexus, "long", status="success", timeout=30)
    assert run, "run was not recovered as success after runtime restart"
    assert "SURVIVED" in nexus.client.run_log("long")
