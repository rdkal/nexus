"""Address-tree construction and path resolution.

Pure logic (no iris/httpx) so it can be unit-tested in isolation. The daemon's
``GET /projects`` returns a flat list of projects keyed by resource address
(roots and external sub-projects, e.g. ``my-system`` and ``my-system/db``).
These helpers turn that flat list into a tree for the overview page and resolve
a URL path to either a project or one of its services.
"""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class TreeNode:
    address: str  # full resource address, e.g. "my-system/db"
    label: str  # last path segment, e.g. "db"
    project: dict | None  # the /projects entry, or None for a synthetic ancestor
    children: list["TreeNode"] = field(default_factory=list)


def build_tree(projects: list[dict]) -> list[TreeNode]:
    """Build a nested tree of projects from the flat /projects list.

    Ancestors that are not themselves listed get a synthetic node (project=None)
    so the tree stays connected. Children are sorted by address.
    """
    by_addr = {p["name"]: p for p in projects}
    index: dict[str, TreeNode] = {}
    roots: list[TreeNode] = []

    def ensure(address: str) -> TreeNode:
        node = index.get(address)
        if node is not None:
            return node
        node = TreeNode(
            address=address,
            label=address.rsplit("/", 1)[-1],
            project=by_addr.get(address),
        )
        index[address] = node
        if "/" in address:
            parent = address.rsplit("/", 1)[0]
            ensure(parent).children.append(node)
        else:
            roots.append(node)
        return node

    for address in sorted(by_addr):
        ensure(address)

    def sort_rec(nodes: list[TreeNode]) -> None:
        nodes.sort(key=lambda n: n.address)
        for n in nodes:
            sort_rec(n.children)

    sort_rec(roots)
    return roots


def resolve(path: str, project_addresses: set[str]):
    """Resolve a URL path to a target.

    Returns one of:
      ("project", address)          — path is a known project address
      ("service", address, svc)     — path is <project>/<service-rel-address>
      None                          — no match

    A service is identified as the remainder after the longest project-address
    prefix; the caller confirms the service actually exists. This mirrors the
    daemon socket's splitRoute so clean URLs like /my-system/db/postgres work.
    """
    path = path.strip("/")
    if not path:
        return None
    if path in project_addresses:
        return ("project", path)

    segs = path.split("/")
    for i in range(len(segs) - 1, 0, -1):
        prefix = "/".join(segs[:i])
        if prefix in project_addresses:
            return ("service", prefix, "/".join(segs[i:]))
    return None
