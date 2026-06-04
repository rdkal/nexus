"""Poll app git repos and run deploy flows before restarting processes."""
import os
import shutil
import subprocess
import time
from pathlib import Path

import httpx

from nexus.config import AppConfig, load_config

NEXUS_HOME = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
DEFAULT_POLL_INTERVAL = int(os.environ.get("NEXUS_POLL_INTERVAL", 60))
PC_PORT = int(os.environ.get("PROCESS_COMPOSE_PORT", 9080))
PC_BASE = f"http://localhost:{PC_PORT}"
PREFECT_API_URL = os.environ.get("PREFECT_API_URL", "http://localhost:4200/api")


# ── git helpers ───────────────────────────────────────────────────────────────

def remote_head(repo_dir: Path, branch: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo_dir), "ls-remote", "origin", f"refs/heads/{branch}"],
        capture_output=True,
        text=True,
    )
    parts = result.stdout.strip().split()
    return parts[0] if parts else ""


def local_head(repo_dir: Path) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
        capture_output=True,
        text=True,
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
    """Run nexus_deploy.py in staging if present. Returns True if deploy should proceed."""
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
        cwd=str(staging_dir),
        env=env,
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


# ── update orchestration ──────────────────────────────────────────────────────

def update_app(app: AppConfig) -> bool:
    active_dir = NEXUS_HOME / "apps" / app.name
    staging_dir = NEXUS_HOME / "apps" / f"{app.name}.next"

    if not active_dir.exists():
        return False

    try:
        fetch_origin(active_dir)
        prepare_staging(active_dir, staging_dir, app.branch)

        if not run_deploy_flow(staging_dir, app.name):
            _remove_worktree(active_dir, staging_dir)
            return False

        processes = app_processes(app.name)
        for proc in processes:
            print(f"[poller]   stop {proc}")
            stop_process(proc)

        time.sleep(2)
        apply_update(active_dir, app.branch)

        for proc in processes:
            print(f"[poller]   start {proc}")
            start_process(proc)

        _remove_worktree(active_dir, staging_dir)
        print(f"[poller] {app.name} deployed")
        return True

    except Exception as e:
        print(f"[poller] Error deploying {app.name}: {e}")
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

        for app in config.apps:
            active_dir = NEXUS_HOME / "apps" / app.name
            if not active_dir.exists():
                continue
            try:
                rhead = remote_head(active_dir, app.branch)
                lhead = known.get(app.name) or local_head(active_dir)

                if rhead and rhead != lhead:
                    print(f"[poller] Change in {app.name}: {lhead[:8]} → {rhead[:8]}")
                    if update_app(app):
                        known[app.name] = rhead
                else:
                    known[app.name] = lhead
            except Exception as e:
                print(f"[poller] Error checking {app.name}: {e}")

        time.sleep(DEFAULT_POLL_INTERVAL)


if __name__ == "__main__":
    main()
