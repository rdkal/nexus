"""
End-to-end test for wildcard tag refs.

A project can track `@<glob>` (e.g. `@app-v*`), resolving to the highest semver
tag matching that pattern. This lets one app in a monorepo track only its own
tags, using whatever naming scheme it likes — a tag for a *different* app (even a
numerically higher one) must not trigger a redeploy.
"""

import time

YAML = "services:\n  api:\n    run: sleep 3600\n"


def test_glob_ref_tracks_only_matching_tags(nexus, git_repo):
    # app-v1.0.0 at the first commit, app-v2.0.0 at the second.
    git_repo.commit({"nexus.yaml": YAML})
    git_repo.tag("app-v1.0.0")
    sha_v2 = git_repo.commit({"nexus.yaml": YAML, "bump": "2"}, message="v2")
    git_repo.tag("app-v2.0.0")

    nexus.add_project(git_repo.spec_path, "app", ref="@app-v*")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()

    # Resolves to the highest app-v* tag → app-v2.0.0.
    nexus.wait_for_project_sha("app", sha_v2, timeout=60)

    # A different app's tag — numerically higher — must be ignored by @app-v*.
    git_repo.commit({"nexus.yaml": YAML, "bump": "3"}, message="other")
    git_repo.tag("other-v9.0.0")
    time.sleep(5)  # two-plus poll cycles
    assert nexus.list_summary("app")["current_sha"] == sha_v2, (
        "glob ref jumped to a non-matching (other-app) tag"
    )

    # A newer matching tag does trigger a redeploy.
    sha_v21 = git_repo.commit({"nexus.yaml": YAML, "bump": "4"}, message="v2.1")
    git_repo.tag("app-v2.1.0")
    nexus.wait_for_project_sha("app", sha_v21, timeout=60)
