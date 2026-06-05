"""Tests for nexus.setup — app repo cloning using local bare remotes."""
import subprocess
from pathlib import Path

from nexus.config import IncludeConfig
from nexus.setup import clone_or_update


def test_initial_clone(make_app, nexus_home, tmp_path):
    """clone_or_update clones a fresh repo when dest doesn't exist."""
    app = make_app("myapp")
    dest = nexus_home / "apps" / "fresh-clone"

    inc = IncludeConfig(name="fresh-clone", repo=str(app.bare))
    clone_or_update(inc, dest)

    assert (dest / ".git").exists()
    assert (dest / "nexus.yaml").exists()


def test_update_existing_clone(make_app, nexus_home):
    """clone_or_update fast-forwards an existing clone after a push."""
    app = make_app("myapp")

    sha_before = app.active_sha()
    new_sha = app.push_update({"README.md": "v2"})

    assert new_sha != sha_before  # remote has moved

    inc = IncludeConfig(name="myapp", repo=str(app.bare))
    clone_or_update(inc, app.active)

    assert app.active_sha() == new_sha


def test_clone_respects_branch(make_app, nexus_home, tmp_path):
    """clone_or_update checks out the requested branch."""
    app = make_app("myapp")
    bare = app.bare
    scratch = app._scratch

    # Create a feature branch on the remote
    subprocess.run(["git", "-C", str(scratch), "checkout", "-b", "feature"],
                   check=True, capture_output=True)
    subprocess.run(["git", "-C", str(scratch), "push", "origin", "feature"],
                   check=True, capture_output=True)

    dest = nexus_home / "apps" / "feature-clone"
    inc = IncludeConfig(name="feature-clone", repo=str(bare), branch="feature")
    clone_or_update(inc, dest)

    branch = subprocess.run(
        ["git", "-C", str(dest), "rev-parse", "--abbrev-ref", "HEAD"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    assert branch == "feature"
