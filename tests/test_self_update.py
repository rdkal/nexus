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


def _runtime_starts(nexus) -> int:
    """How many times the nexus runtime has started (one line per process start)."""
    log = nexus.home / "logs" / "nexus-runtime" / "current.log"
    if not log.exists():
        return 0
    return log.read_text(errors="replace").count("nexus daemon starting")

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
            # Deliberately scheme-less, while the project's stored spec_path is the
            # resolved file:// clone URL — so isSelf must match across transport
            # forms. (Real installs store https://… but compare against the bare
            # spec path; this is the #61 mismatch in fixture form.)
            "NEXUS_SELF_SPEC": str(selfrepo.bare),
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

    starts_before = _runtime_starts(nexus)

    # Push a new commit to the self repo → self-update: build swaps the binary,
    # deploy promotes, daemon asks nexus-pm to restart the runtime.
    self_sha2 = selfrepo.commit(
        {"nexus.yaml": SELF_YAML, "bump": "2"}, message="self v2"
    )
    assert self_sha2 != self_sha1

    # After the runtime restart the new nexus recovers state and reports sha2.
    nexus.wait_for_project_sha("nexus-self", self_sha2)

    # The RUNNING runtime must actually restart onto the new binary — promoting
    # sha2 alone is not self-update (#61: isSelf never matched, so the process
    # was never reloaded). Each runtime start logs "nexus daemon starting".
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline and _runtime_starts(nexus) <= starts_before:
        time.sleep(0.5)
    assert _runtime_starts(nexus) > starts_before, (
        f"nexus runtime did not restart on self-update "
        f"(daemon-start count stayed at {starts_before})"
    )

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
