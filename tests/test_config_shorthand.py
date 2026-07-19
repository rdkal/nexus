"""
End-to-end test for the projects: string shorthand.

A `projects:` entry can be written as a bare string `<spec>@<ref>` instead of a
`{src, ref}` map. This verifies an external sub-project declared with the
shorthand is discovered and deployed.
"""

from conftest import GitRepo

DB_YAML = """\
services:
  store:
    run: sh -c 'echo STORE_UP; exec sleep 3600'
"""


def test_projects_string_shorthand(nexus, tmp_path):
    db = GitRepo(tmp_path / "db")
    db.commit({"nexus.yaml": DB_YAML})

    # Shorthand: projects.db is a plain string "<spec>@<ref>", not a map.
    root = GitRepo(tmp_path / "root")
    root.commit({"nexus.yaml": f"projects:\n  db: {db.spec_path}@main\n"})

    nexus.add_project(root.spec_path, "root")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    entry = nexus.wait_for_list_entry("root/db", healthy=True, timeout=60)
    assert entry["current_sha"], f"sub-project not deployed: {entry}"

    log = nexus.client.get_log("root/db", "store")
    assert "STORE_UP" in log, f"unexpected store log: {log!r}"
