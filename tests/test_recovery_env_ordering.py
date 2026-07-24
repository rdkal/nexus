"""
Regression test for #62: after a daemon restart, an external sub-project whose
service references a sibling's cross-project volume var must still come back up.

Recovery walks children in a deterministic alias order, so a consumer (`authelia`)
recovers before its provider (`traefik`) — its `NEXUS_RETU_TRAEFIK_DYNAMIC`
reference is unresolved at that instant and its service was silently skipped. The
daemon now re-spawns such services once every project has recovered.
"""

import time

from conftest import GitRepo

PROVIDER = "volumes:\n  dynamic: {}\nservices:\n  proxy:\n    run: sleep 3600\n"

# References the provider's volume via the nested global var, with no default —
# so it fails to resolve (and the service is skipped) if the provider isn't up.
CONSUMER = (
    "environment:\n"
    "  DIR: ${NEXUS_RETU_TRAEFIK_DYNAMIC}\n"
    "services:\n"
    "  auth:\n"
    "    run: sh -c 'echo DIR=[$DIR]; exec sleep 3600'\n"
)


def _pid(nexus, addr, svc, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        pid = nexus.service_pid(addr, svc)
        if pid:
            return pid
        time.sleep(1)
    return None


def test_recovery_respawns_service_needing_sibling_volume(nexus, tmp_path):
    provider = GitRepo(tmp_path / "provider")
    provider.commit({"nexus.yaml": PROVIDER})
    consumer = GitRepo(tmp_path / "consumer")
    consumer.commit({"nexus.yaml": CONSUMER})

    root = GitRepo(tmp_path / "root")
    root.commit(
        {
            "nexus.yaml": (
                "projects:\n"
                f"  authelia:\n    src: {consumer.spec_path}\n"
                '    ref: "@main"\n'
                f"  traefik:\n    src: {provider.spec_path}\n"
                '    ref: "@main"\n'
            )
        }
    )

    nexus.add_project(root.spec_path, "retu")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # Both deploy (a first-deploy ordering race self-heals via the deploy retry).
    nexus.wait_for_list_entry("retu/traefik", healthy=True, timeout=90)
    nexus.wait_for_list_entry("retu/authelia", healthy=True, timeout=90)
    assert _pid(nexus, "retu/authelia", "auth"), "consumer never came up initially"

    # Restart the whole stack — recovery runs, authelia (alias-sorted first)
    # recovers before traefik and its env is momentarily unresolvable.
    nexus.stop()
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # The consumer's service must be running again — not silently skipped.
    assert _pid(nexus, "retu/authelia", "auth", timeout=40), (
        "consumer service was not recovered after restart "
        "(skipped due to cross-project env ordering)"
    )
