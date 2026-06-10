"""Register app flows as Prefect deployments via the REST API."""
import os
from pathlib import Path

import httpx

from nexus.config import NexusConfig

WORK_POOL = "nexus-pool"


def register_app_flows(
    app_name: str,
    app_dir: Path,
    app_config: NexusConfig,
    prefect_api_url: str | None = None,
) -> None:
    """Create or update Prefect deployments for every flow declared in app_config."""
    if not app_config.flows:
        return
    api = prefect_api_url or os.environ.get("PREFECT_API_URL", "http://localhost:4200/api")
    for flow_name, flow_cfg in app_config.flows.items():
        # Prefect rejects slashes in names; use hyphen as the separator
        deployment_name = f"{app_name}-{flow_name}".replace("/", "-")
        _upsert_deployment(
            deployment_name=deployment_name,
            app_dir=app_dir,
            entrypoint=flow_cfg.entrypoint,
            api=api,
        )


def _upsert_deployment(
    deployment_name: str,
    app_dir: Path,
    entrypoint: str,
    api: str,
) -> None:
    _, func_name = entrypoint.rsplit(":", 1)
    try:
        # POST /flows is idempotent — creates or returns existing flow record
        r = httpx.post(
            f"{api}/flows", json={"name": func_name},
            timeout=10, follow_redirects=True,
        )
        r.raise_for_status()
        flow_id = r.json()["id"]

        # POST /deployments upserts on (flow_id, name)
        r = httpx.post(
            f"{api}/deployments",
            json={
                "name": deployment_name,
                "flow_id": str(flow_id),
                "path": str(app_dir),
                "entrypoint": entrypoint,
                "work_pool_name": WORK_POOL,
            },
            timeout=10,
            follow_redirects=True,
        )
        r.raise_for_status()
        print(f"[register] {deployment_name} → {entrypoint}")
    except Exception as e:
        print(f"[register] Failed to register {deployment_name}: {e}")
