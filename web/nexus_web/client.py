"""HTTP client for the nexus daemon's Unix socket API.

The daemon exposes its API over ``$NEXUS_HOME/nexus.sock`` (never a public port);
nexus-web is the only public HTTP interface. This wraps the seven endpoints with
an httpx client bound to the Unix socket.
"""

from __future__ import annotations

import httpx


class NexusError(Exception):
    """A request to the daemon failed."""


class NexusClient:
    def __init__(self, socket_path: str, timeout: float = 5.0):
        self._socket_path = socket_path
        transport = httpx.HTTPTransport(uds=socket_path)
        # The host in base_url is irrelevant — the UDS transport routes every
        # request to the socket — but httpx requires an absolute base_url.
        self._client = httpx.Client(
            transport=transport, base_url="http://nexus", timeout=timeout
        )

    def close(self) -> None:
        self._client.close()

    # --- read endpoints ---

    def list_projects(self) -> list[dict]:
        return self._get_json("/projects")

    def get_project(self, address: str) -> dict:
        return self._get_json(f"/projects/{address}")

    def get_history(self, address: str) -> list[dict]:
        return self._get_json(f"/projects/{address}/history")

    def list_services(self, address: str) -> list[dict]:
        return self._get_json(f"/projects/{address}/services")

    def get_log(self, address: str, service: str) -> str:
        r = self._client.get(f"/projects/{address}/services/{service}/log")
        if r.status_code == 404:
            return ""
        self._check(r)
        return r.text

    def get_build_log(self, address: str, sha: str) -> str:
        r = self._client.get(f"/projects/{address}/builds/{sha}/log")
        if r.status_code == 404:
            return ""
        self._check(r)
        return r.text

    # --- write endpoints (used by the actions PR; wrapped here for completeness) ---

    def redeploy(self, address: str) -> dict:
        return self._post_json(f"/projects/{address}/redeploy")

    def restart(self, address: str, service: str) -> dict:
        return self._post_json(f"/projects/{address}/services/{service}/restart")

    # --- internals ---

    def _get_json(self, path: str):
        r = self._client.get(path)
        self._check(r)
        return r.json()

    def _post_json(self, path: str):
        r = self._client.post(path)
        self._check(r)
        return r.json()

    @staticmethod
    def _check(r: httpx.Response) -> None:
        if r.status_code >= 400:
            raise NexusError(f"{r.request.method} {r.request.url.path}: {r.status_code} {r.text.strip()}")
