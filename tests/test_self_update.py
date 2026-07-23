"""
End-to-end test for nexus self-update.

The headline property: nexus can rebuild and restart *itself* without disturbing
user services, because nexus-pm — not nexus — owns the user service processes.

Setup:
  - a "user" project with a long-running service (sleep)
  - a "nexus-self" stand-in project whose build atomically swaps the nexus runtime
    binary and declares no services (mirrors nexus's own nexus.yaml)

We identify the stand-in as "self" via NEXUS_SELF_SPEC, and point its build at the
pristine session-built nexus binary via NEXUS_SELFTEST_BIN (a valid no-op swap, so
the restarted runtime actually works). Pushing a new commit to the self repo must
restart the runtime while the user service keeps the same PID throughout.
"""

import time

from conftest import GitRepo

USER_YAML = """\
services:
  web:
    run: sleep 3600
"""

# Stand-in for nexus's own nexus.yaml: atomically swap the runtime binary, no services.
# NEXUS_SELFTEST_BIN is a daemon-env var specific to this test; since projects are
# isolated from the daemon's environment, forward it explicitly (bare-key form).
# (Real self-update's build only needs NEXUS_HOME/NEXUS_SHA, which are injected.)
SELF_YAML = (
    "environment:\n"
    "  - NEXUS_SELFTEST_BIN\n"
    'build: cp "$NEXUS_SELFTEST_BIN" "$NEXUS_HOME/bin/.nexus.new" '
    '&& chmod +x "$NEXUS_HOME/bin/.nexus.new" '
    '&& mv "$NEXUS_HOME/bin/.nexus.new" "$NEXUS_HOME/bin/nexus"\n'
)


def test_self_update_keeps_user_services_running(nexus, tmp_path):
    user = GitRepo(tmp_path / "user")
    user.commit({"nexus.yaml": USER_YAML})

    selfrepo = GitRepo(tmp_path / "selfrepo")
    self_sha1 = selfrepo.commit({"nexus.yaml": SELF_YAML})

    nexus.add_project(user.spec_path, "user")
    nexus.add_project(selfrepo.spec_path, "nexus-self")

    nexus.start(
        poll_interval="2s",
        extra_env={
            "NEXUS_SELF_SPEC": selfrepo.spec_path,
            "NEXUS_SELFTEST_BIN": str(nexus.nexus_source),
        },
    )
    nexus.wait_for_socket()

    # Let the system settle: user service deployed, self settled at sha1.
    # (The self project self-deploys once at startup, which restarts the runtime;
    # we wait past that so the recorded PID is stable.)
    nexus.wait_for_sha("user")
    nexus.wait_for_project_sha("nexus-self", self_sha1)

    # Record the user service PID once it is reported.
    pid_before = None
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        pid_before = nexus.service_pid("user", "web")
        if pid_before:
            break
        time.sleep(0.5)
    assert pid_before, "user service never reported a PID"

    # Push a new commit to the self repo → self-update: build swaps the binary,
    # deploy promotes, daemon asks nexus-pm to restart the runtime.
    self_sha2 = selfrepo.commit(
        {"nexus.yaml": SELF_YAML, "bump": "2"}, message="self v2"
    )
    assert self_sha2 != self_sha1

    # After the runtime restart the new nexus recovers state and reports sha2.
    nexus.wait_for_project_sha("nexus-self", self_sha2)

    # The user service must have survived with the SAME PID — nexus-pm owns it and
    # never restarted it across the nexus runtime swap.
    pid_after = nexus.service_pid("user", "web")
    assert pid_after == pid_before, (
        f"user service PID changed across nexus self-update: "
        f"before={pid_before}, after={pid_after}"
    )

    services = nexus.client.list_services("user")
    assert any(s["name"] == "web" and s["running"] for s in services), (
        f"user service not running after self-update: {services}"
    )
