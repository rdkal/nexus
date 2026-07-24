"""
Regression test for #56: a failed deploy retries with backoff instead of waiting
for a new commit. A transient build failure (here, a missing readiness file that
appears later — standing in for a self-update racing the release upload, a flaky
network, etc.) must self-heal with no new commit and no manual re-add.
"""

import time

# Build fails until $NEXUS_HOME/ready exists; then it succeeds.
YAML = (
    "build: sh -c 'test -f \"$NEXUS_HOME/ready\" || { echo NOT_READY; exit 1; }'\n"
    "services:\n"
    "  svc:\n"
    "    run: sleep 3600\n"
)


def test_failed_deploy_retries_without_new_commit(nexus, git_repo):
    git_repo.commit({"nexus.yaml": YAML})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # The first deploy fails (no readiness file); the project stays undeployed.
    time.sleep(4)
    summary = nexus.list_summary("app")
    assert not (summary and summary.get("current_sha")), f"unexpectedly deployed: {summary}"

    # Make the transient condition clear — WITHOUT touching the repo.
    (nexus.home / "ready").write_text("")

    # nexus retries the same SHA on backoff and deploys it — no new commit.
    sha = nexus.wait_for_sha("app", timeout=30)
    assert sha, "project never deployed after the transient failure cleared"
