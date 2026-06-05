"""Poll included app repos and run deploy flows before restarting processes."""
import os
import shutil
import subprocess
import time
from pathlib import Path

import httpx

from nexus.config import IncludeConfig, NexusConfig, load_config

NEXUS_HOME = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
DEFAULT_POLL_INTERVAL = int(os.environ.get("NEXUS_POLL_INTERVAL", 60))
PC_PORT = int(os.environ.get("PROCESS_COMPOSE_PORT", 9080))
PC_BASE = f"http://localhost:{PC_PORT}"
PREFECT_API_URL = os.environ.get("PREFECT_API_URL", "http://localhost:4200/api")


# ── git helpers ───────────────────────────────────────────────────────────────

def remote_head(repo_dir: Path, branch: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo_dir), "ls-remote", "origin", f"refs/heads/{branch}"],
        capture_output=True, text=True,
    )
    parts = result.stdout.strip().split()
    return parts[0] if parts else ""


def local_head(repo_dir: Path) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
        capture_output=True, text=True,
    )
    return result.stdout.strip()


def fetch_origin(repo_dir: Path) -> None:
    subprocess.run(["git", "-C", str(repo_dir), "fetch", "origin"], check=True)


def prepare_staging(active_dir: Path, staging_dir: Path, branch: str) -> None:
    _remove_worktree(active_dir, staging_dir)
    subprocess.run(
        ["git", "-C", str(active_dir), "worktree", "add",
         str(staging_dir), f"origin/{branch}"],
        check=True,
    )


def _remove_worktree(active_dir: Path, staging_dir: Path) -> None:
    subprocess.run(
        ["git", "-C", str(active_dir), "worktree", "remove", "--force", str(staging_dir)],
        capture_output=True,
    )
    if staging_dir.exists():
        shutil.rmtree(staging_dir, ignore_errors=True)


def apply_update(active_dir: Path, branch: str) -> None:
    subprocess.run(
        ["git", "-C", str(active_dir), "reset", "--hard", f"origin/{branch}"],
        check=True,
    )
    subprocess.run(["uv", "sync"], cwd=str(active_dir), check=True)


# ── deploy flow ───────────────────────────────────────────────────────────────

def run_deploy_flow(staging_dir: Path, app_name: str) -> bool:
    deploy_script = staging_dir / "nexus_deploy.py"
    if not deploy_script.exists():
        return True

    print(f"[poller] Running deploy flow for {app_name}...")
    if subprocess.run(["uv", "sync"], cwd=str(staging_dir)).returncode != 0:
        print(f"[poller] uv sync failed in staging for {app_name}")
        return False

    env = os.environ.copy()
    env["PREFECT_API_URL"] = PREFECT_API_URL
    result = subprocess.run(
        ["uv", "run", "python", "nexus_deploy.py"],
        cwd=str(staging_dir), env=env,
    )

    if result.returncode == 0:
        print(f"[poller] Deploy flow passed for {app_name}")
        return True
    print(f"[poller] Deploy flow FAILED for {app_name} — keeping current version")
    return False


# ── process-compose API ───────────────────────────────────────────────────────

def app_processes(app_name: str) -> list[str]:
    try:
        resp = httpx.get(f"{PC_BASE}/processes", timeout=5)
        resp.raise_for_status()
        return [
            p["name"]
            for p in resp.json().get("data", [])
            if p["name"].startswith(f"{app_name}-")
        ]
    except Exception:
        return []


def stop_process(name: str) -> None:
    try:
        httpx.patch(f"{PC_BASE}/process/stop/{name}", timeout=10)
    except Exception as e:
        print(f"[poller] Failed to stop {name}: {e}")


def start_process(name: str) -> None:
    try:
        httpx.post(f"{PC_BASE}/process/start/{name}", timeout=10)
    except Exception as e:
        print(f"[poller] Failed to start {name}: {e}")


# ── app config helpers ────────────────────────────────────────────────────────

def app_nexus_config(active_dir: Path) -> NexusConfig | None:
    nexus_yaml = active_dir / "nexus.yaml"
    if nexus_yaml.exists():
        return load_config(nexus_yaml)
    return None


def has_processes(app_config: NexusConfig | None, active_dir: Path) -> bool:
    if app_config and app_config.processes:
        return True
    # Backward compat: bare process-compose.yaml with no nexus.yaml
    return (active_dir / "process-compose.yaml").exists() and app_config is None


# ── update orchestration ──────────────────────────────────────────────────────

def update_app(inc: IncludeConfig) -> bool:
    active_dir = NEXUS_HOME / "apps" / inc.name
    staging_dir = NEXUS_HOME / "apps" / f"{inc.name}.next"

    if not active_dir.exists():
        return False

    try:
        fetch_origin(active_dir)
        prepare_staging(active_dir, staging_dir, inc.branch)

        if not run_deploy_flow(staging_dir, inc.name):
            _remove_worktree(active_dir, staging_dir)
            return False

        app_config = app_nexus_config(active_dir)
        with_procs = has_processes(app_config, active_dir)

        if with_procs:
            processes = app_processes(inc.name)
            for proc in processes:
                print(f"[poller]   stop {proc}")
                stop_process(proc)
            time.sleep(2)

        apply_update(active_dir, inc.branch)

        if with_procs:
            processes = app_processes(inc.name)
            for proc in processes:
                print(f"[poller]   start {proc}")
                start_process(proc)

        _remove_worktree(active_dir, staging_dir)

        if app_config and app_config.flows:
            print(f"[poller] {inc.name} has flows: {list(app_config.flows)}")
            # TODO: re-register Prefect deployments

        print(f"[poller] {inc.name} deployed")
        return True

    except Exception as e:
        print(f"[poller] Error deploying {inc.name}: {e}")
        _remove_worktree(active_dir, staging_dir)
        return False


# ── main loop ─────────────────────────────────────────────────────────────────

def main():
    config_file = NEXUS_HOME / "config.yaml"
    known: dict[str, str] = {}

    print(f"[poller] Started (default interval: {DEFAULT_POLL_INTERVAL}s)")

    while True:
        try:
            config = load_config(config_file)
        except Exception as e:
            print(f"[poller] Failed to read config: {e}")
            time.sleep(DEFAULT_POLL_INTERVAL)
            continue

        for inc in config.includes:
            active_dir = NEXUS_HOME / "apps" / inc.name
            if not active_dir.exists():
                continue
            try:
                rhead = remote_head(active_dir, inc.branch)
                lhead = known.get(inc.name) or local_head(active_dir)

                if rhead and rhead != lhead:
                    print(f"[poller] Change in {inc.name}: {lhead[:8]} → {rhead[:8]}")
                    if update_app(inc):
                        known[inc.name] = rhead
                else:
                    known[inc.name] = lhead
            except Exception as e:
                print(f"[poller] Error checking {inc.name}: {e}")

        time.sleep(DEFAULT_POLL_INTERVAL)


if __name__ == "__main__":
    main()
