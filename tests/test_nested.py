"""
End-to-end tests for external nested (sub-)projects.

A root project's nexus.yaml can declare external sub-projects via `projects:` with
a `src:`. Each sub-project is an independent deployment — its own repo, ref, poller
and worktree — addressed `<root>/<alias>`. These tests verify that sub-projects are
discovered and deployed with their parent, redeploy independently on their own ref,
and are torn down when removed from the parent's config.
"""

import time

from conftest import GitRepo

DB_YAML = """\
services:
  store:
    run: sleep 3600
"""


def _root_yaml(db_spec: str) -> str:
    return f"""\
projects:
  db:
    src: {db_spec}
    ref: "@main"
"""


def _setup(nexus, tmp_path):
    """Create a db sub-project repo and a root repo that composes it; start nexus."""
    db = GitRepo(tmp_path / "db")
    db.commit({"nexus.yaml": DB_YAML})

    root = GitRepo(tmp_path / "root")
    root.commit({"nexus.yaml": _root_yaml(db.spec_path)})

    nexus.add_project(root.spec_path, "root")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    return db, root


def test_external_subproject_deploys_with_parent(nexus, tmp_path):
    db, root = _setup(nexus, tmp_path)

    # Root deploys, then its external sub-project "root/db" is discovered and deployed.
    nexus.wait_for_sha("root")
    entry = nexus.wait_for_list_entry("root/db", healthy=True, timeout=60)

    assert entry["name"] == "root/db"
    assert entry["current_sha"], f"sub-project has no SHA: {entry}"
    assert entry["health"] == "healthy", f"sub-project not healthy: {entry}"


def test_subproject_redeploys_on_own_ref(nexus, tmp_path):
    db, root = _setup(nexus, tmp_path)

    nexus.wait_for_sha("root")
    entry = nexus.wait_for_list_entry("root/db", healthy=True, timeout=60)
    db_sha1 = entry["current_sha"]
    root_sha1 = nexus.list_summary("root")["current_sha"]

    # A new commit to the db repo only — the sub-project must redeploy on its own.
    db_sha2 = db.commit({"nexus.yaml": DB_YAML, "bump": "2"}, message="db v2")
    assert db_sha2 != db_sha1

    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        cur = nexus.list_summary("root/db")
        if cur and cur.get("current_sha") == db_sha2:
            break
        time.sleep(1)
    else:
        cur = nexus.list_summary("root/db")
        raise AssertionError(
            f"sub-project did not redeploy to {db_sha2}; current={cur}"
        )

    # The root must NOT have redeployed — sub-projects track their own ref.
    assert nexus.list_summary("root")["current_sha"] == root_sha1, (
        "root SHA changed when only the sub-project's repo advanced"
    )


def test_subproject_removed_when_dropped_from_config(nexus, tmp_path):
    db, root = _setup(nexus, tmp_path)

    nexus.wait_for_sha("root")
    nexus.wait_for_list_entry("root/db", healthy=True, timeout=60)

    # New root commit that no longer declares the db sub-project.
    root.commit({"nexus.yaml": "services: {}\n", "bump": "2"}, message="drop db")

    # The sub-project and its services must be torn down.
    nexus.wait_for_list_gone("root/db", timeout=60)

    # Root itself is still present and deployed.
    assert nexus.list_summary("root") is not None
