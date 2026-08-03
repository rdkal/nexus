"""End-to-end test for `nexus project set-ref` — changing the git ref a project
tracks, live, without re-adding it.
"""

YAML = "services:\n  web:\n    run: sleep 3600\n"


def test_set_ref_switches_tracked_ref(nexus, git_repo):
    # v1.0.0 at the first commit; main advances to a second commit.
    sha_v1 = git_repo.commit({"nexus.yaml": YAML})
    git_repo.tag("v1.0.0")
    sha_main = git_repo.commit({"nexus.yaml": YAML, "bump": "2"}, message="second")

    nexus.add_project(git_repo.spec_path, "app", ref="main")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # Tracking main → deployed at main's HEAD.
    nexus.wait_for_project_sha("app", sha_main, timeout=60)

    # Switch to the v1.0.0 tag; the daemon re-resolves and redeploys live.
    nexus.cli("project", "set-ref", "app", "v1.0.0")
    nexus.wait_for_project_sha("app", sha_v1, timeout=60)
