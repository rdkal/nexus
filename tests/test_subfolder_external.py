"""
End-to-end test for external sub-projects whose `src` points at a subdirectory.

A parent's `projects:` entry can reference an app that lives in a subdirectory of
another repo (e.g. `src: <repo>/db`). nexus walks up to find the repo root, clones
it, and deploys the sub-project from its subdirectory — the same discovery used
for root projects, applied to nested externals.
"""

from conftest import GitRepo


def test_external_subproject_with_subdir(nexus, tmp_path):
    # A components monorepo with a database app in the db/ subdirectory.
    components = GitRepo(tmp_path / "components")
    components.commit(
        {
            "README.md": "components monorepo\n",
            "db/nexus.yaml": (
                "services:\n"
                "  store:\n"
                "    run: sh -c 'echo STORE_UP; exec sleep 3600'\n"
            ),
        }
    )

    # A system repo that composes the db app by its subdirectory path.
    root = GitRepo(tmp_path / "system")
    root.commit(
        {
            "nexus.yaml": (
                "projects:\n"
                "  db:\n"
                f"    src: {components.spec_path}/db\n"
                '    ref: "@main"\n'
            )
        }
    )

    nexus.add_project(root.spec_path, "system")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # The external sub-project is discovered, its repo root walked up from the
    # subdir src, cloned, and deployed from components.git/db.
    entry = nexus.wait_for_list_entry("system/db", healthy=True, timeout=60)
    assert entry["current_sha"], f"sub-project not deployed: {entry}"

    # Its service (defined in db/nexus.yaml) is running with the expected log.
    services = nexus.client.list_services("system/db")
    names = {s["name"] for s in services}
    assert "store" in names, f"subdir sub-project service missing: {services}"
    log = nexus.client.get_log("system/db", "store")
    assert "STORE_UP" in log, f"unexpected store log: {log!r}"
