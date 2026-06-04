"""Poll app git repos and restart processes when changes are detected."""
import os
import subprocess
import time
from pathlib import Path

import httpx

from nexus.config import load_config

NEXUS_HOME = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
DEFAULT_POLL_INTERVAL = int(os.environ.get("NEXUS_POLL_INTERVAL", 60))
PC_PORT = int(os.environ.get("PROCESS_COMPOSE_PORT", 9080))
PC_BASE = f"http://localhost:{PC_PORT}"


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


def app_processes(app_name: str) -> list[str]:
    try:
        resp = httpx.get(f"{PC_BASE}/processes", timeout=5)
        resp.raise_for_status()
        return [p["name"] for p in resp.json() if p["name"].startswith(f"{app_name}-")]
    except Exception:
        return []


def stop_process(name: str) -> None:
    try:
        httpx.post(f"{PC_BASE}/process/stop", json={"name": name}, timeout=10)
    except Exception as e:
        print(f"[poller] Failed to stop {name}: {e}")


def start_process(name: str) -> None:
    try:
        httpx.post(f"{PC_BASE}/process/start", json={"name": name}, timeout=10)
    except Exception as e:
        print(f"[poller] Failed to start {name}: {e}")


def update_app(app_name: str, repo_dir: Path, branch: str) -> None:
    print(f"[poller] Updating {app_name}...")
    processes = app_processes(app_name)

    for proc in processes:
        print(f"[poller]   stop {proc}")
        stop_process(proc)

    time.sleep(2)

    subprocess.run(
        ["git", "-C", str(repo_dir), "pull", "origin", branch],
        check=True,
    )

    for proc in processes:
        print(f"[poller]   start {proc}")
        start_process(proc)

    print(f"[poller] {app_name} updated")


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
            repo_dir = NEXUS_HOME / "apps" / app.name
            if not repo_dir.exists():
                continue
            try:
                rhead = remote_head(repo_dir, app.branch)
                lhead = known.get(app.name) or local_head(repo_dir)
                if rhead and rhead != lhead:
                    update_app(app.name, repo_dir, app.branch)
                    known[app.name] = rhead
                else:
                    known[app.name] = lhead
            except Exception as e:
                print(f"[poller] Error checking {app.name}: {e}")

        time.sleep(DEFAULT_POLL_INTERVAL)


if __name__ == "__main__":
    main()
