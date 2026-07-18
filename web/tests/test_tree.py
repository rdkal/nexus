"""Unit tests for the pure address-tree / resolution logic (no iris, no socket)."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from nexus_web import tree  # noqa: E402


def _projects(*names):
    return [{"name": n, "health": "healthy", "current_sha": "x", "ref": "@main"} for n in names]


def test_build_tree_nests_by_address():
    roots = tree.build_tree(_projects("my-system", "my-system/db", "app"))
    # Two roots, sorted: app, my-system
    assert [r.address for r in roots] == ["app", "my-system"]
    my = roots[1]
    assert [c.address for c in my.children] == ["my-system/db"]
    assert my.children[0].label == "db"


def test_build_tree_synthesizes_missing_ancestors():
    # Only the nested address is listed; the parent must be synthesized.
    roots = tree.build_tree(_projects("root/db"))
    assert [r.address for r in roots] == ["root"]
    assert roots[0].project is None  # synthetic
    assert [c.address for c in roots[0].children] == ["root/db"]
    assert roots[0].children[0].project is not None


def test_build_tree_empty():
    assert tree.build_tree([]) == []


def test_resolve_project():
    addrs = {"my-system", "my-system/db"}
    assert tree.resolve("my-system", addrs) == ("project", "my-system")
    assert tree.resolve("/my-system/", addrs) == ("project", "my-system")
    assert tree.resolve("my-system/db", addrs) == ("project", "my-system/db")


def test_resolve_service_top_level_and_inline():
    addrs = {"app", "root/db"}
    # top-level service under a root project
    assert tree.resolve("app/api", addrs) == ("service", "app", "api")
    # inline service (multi-segment remainder) under a root project
    assert tree.resolve("app/metrics/exporter", addrs) == ("service", "app", "metrics/exporter")
    # service under an external sub-project (longest-prefix wins)
    assert tree.resolve("root/db/store", addrs) == ("service", "root/db", "store")


def test_resolve_unknown():
    addrs = {"app"}
    assert tree.resolve("nope", addrs) is None
    assert tree.resolve("", addrs) is None
