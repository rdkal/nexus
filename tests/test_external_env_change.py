"""
Regression test for #57 / #58: changing a parent's environment: override for an
already-running external sub-project must actually take effect.

#51 wired the override in for a *fresh* child, but reconcileChildren only diffed
sub-projects by alias presence — so a changed override (or ref/src) on an
already-running child was silently ignored. The child now rebuilds with the new
parent config.
"""

import time

from conftest import GitRepo

CHILD = (
    "environment:\n"
    "  DIR: ${NEXUS_TRAEFIK_DYNAMIC:-}\n"  # child's own default: empty
    "services:\n"
    "  svc:\n"
    "    run: sh -c 'echo RUN_DIR=[$DIR]; exec sleep 3600'\n"
)


def _root(child_spec: str, override: str) -> str:
    return (
        "projects:\n"
        "  edge:\n"
        f"    src: {child_spec}\n"
        '    ref: "@main"\n'
        "    environment:\n"
        f"      DIR: {override}\n"
    )


def _wait_dir(nexus, value, timeout=45):
    deadline = time.monotonic() + timeout
    log = ""
    while time.monotonic() < deadline:
        log = nexus.client.get_log("retu/edge", "svc")
        if value in log:
            return log
        time.sleep(2)
    return log


def test_changing_parent_override_takes_effect(nexus, tmp_path):
    child = GitRepo(tmp_path / "child")
    child.commit({"nexus.yaml": CHILD})
    root = GitRepo(tmp_path / "root")
    root.commit({"nexus.yaml": _root(child.spec_path, "/literal/one")})

    nexus.add_project(root.spec_path, "retu")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_list_entry("retu/edge", healthy=True, timeout=60)

    # First value applies (this already worked with #51).
    assert "RUN_DIR=[/literal/one]" in _wait_dir(nexus, "/literal/one"), "initial override missing"

    # Change ONLY the parent's override for the running child; the child's own
    # src/ref never move. This must still propagate.
    root.commit({"nexus.yaml": _root(child.spec_path, "/literal/TWO")})
    log = _wait_dir(nexus, "/literal/TWO")
    assert "RUN_DIR=[/literal/TWO]" in log, f"changed override not applied; svc log: {log!r}"
