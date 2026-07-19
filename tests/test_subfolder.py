"""
End-to-end test for subdirectory (monorepo) projects.

A project's spec path can point at a subdirectory of a repo; nexus clones the
repo root and reads the app's `nexus.yaml` from the subdirectory, running its
build and services there. This lets several apps share one repo, each deployed
independently.
"""


def test_subfolder_project_deploys_from_subdir(nexus, git_repo):
    # A monorepo: two apps in subdirectories, plus an unrelated root file.
    git_repo.commit(
        {
            "README.md": "monorepo root\n",
            # `cat marker.txt` only succeeds if the build runs with cwd = the
            # subdir (marker.txt lives there, not at the repo root).
            "services/api/nexus.yaml": (
                "build: cat marker.txt\n"
                "services:\n"
                "  server:\n"
                "    run: sh -c 'echo API_SERVER_UP; exec sleep 3600'\n"
            ),
            "services/api/marker.txt": "I_AM_THE_API_APP\n",
            "services/web/nexus.yaml": "services:\n  ui:\n    run: sleep 3600\n",
        }
    )

    # Add the api app by its subdirectory (spec = repo root, subdir = services/api).
    nexus.add_project(git_repo.spec_path, "api", subdir="services/api")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("api")

    # The service defined in services/api/nexus.yaml is running.
    deadline_services = nexus.client.list_services("api")
    names = {s["name"] for s in deadline_services} if isinstance(deadline_services, list) else set()
    assert "server" in names, f"subdir service not deployed: {deadline_services}"

    server = next(s for s in nexus.client.list_services("api") if s["name"] == "server")
    assert server["running"], f"subdir service not running: {server}"

    # The service log shows it started (proving the run command executed).
    log = nexus.client.get_log("api", "server")
    assert "API_SERVER_UP" in log, f"unexpected server log: {log!r}"

    # The build ran with cwd = the subdir: `cat marker.txt` printed the marker
    # that only exists at services/api/marker.txt.
    sha = nexus.list_summary("api")["current_sha"]
    build_log = nexus.client.get_build_log("api", sha)
    assert "I_AM_THE_API_APP" in build_log, f"build did not run in subdir: {build_log!r}"

    # The other app's service ("ui") must NOT be running — we only added api.
    assert "ui" not in names
