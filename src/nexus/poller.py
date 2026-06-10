"""Poll included app repos and run deploy gates before updating."""
import os
import shutil
import subprocess
import time
from pathlib import Path

import httpx

from nexus.config import FlowConfig, IncludeConfig, NexusConfig, load_config
from nexus.register import register_app_flows

NEXUS_HOME = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
DEFAULT_POLL_INTERVAL = int(os.environ.get("NEXUS_POLL_INTERVAL", 60))
PC_PORT = int(os.environ.get("PROCESS_COMPOSE_PORT", 9080))
PC_BASE = f"http://localhost:{PC_PORT}"
PREFECT_API_URL = os.environ.get("PREFECT_API_URL", "http://localhost:4200/api")


# ── git helpers ───────────────────────────────────────────────────────────────

def remote_head(repo_dir: Path, ref: str) -> str:
    """Commit SHA of ref on origin. Handles branches and tags (incl. annotated)."""
    r = subprocess.run(
        ["git", "-C", str(repo_dir), "ls-remote", "origin",
         f"refs/heads/{ref}", f"refs/tags/{ref}", f"refs/tags/{ref}^{{}}"],
        capture_output=True, text=True,
    )
    sha = ""
    for line in r.stdout.strip().splitlines():
        parts = line.split()
        if parts:
            sha = parts[0]  # last entry wins: ^{} (dereferenced tag) comes last
    return sha


def local_head(repo_dir: Path) -> str:
    r = subprocess.run(
        ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
        capture_output=True, text=True,
    )
    return r.stdout.strip()


def fetch_ref(repo_dir: Path, ref: str) -> None:
    """Fetch a specific branch or tag from origin and update FETCH_HEAD."""
    subprocess.run(["git", "-C", str(repo_dir), "fetch", "origin", ref], check=True)


def prepare_staging(active_dir: Path, staging_dir: Path) -> None:
    _remove_worktree(active_dir, staging_dir)
    subprocess.run(
        ["git", "-C", str(active_dir), "worktree", "add", str(staging_dir), "FETCH_HEAD"],
        check=True,
    )


def _remove_worktree(active_dir: Path, staging_dir: Path) -> None:
    subprocess.run(
        ["git", "-C", str(active_dir), "worktree", "remove", "--force", str(staging_dir)],
        capture_output=True,
    )
    if staging_dir.exists():
        shutil.rmtree(staging_dir, ignore_errors=True)


def apply_update(active_dir: Path) -> None:
    subprocess.run(
        ["git", "-C", str(active_dir), "reset", "--hard", "FETCH_HEAD"],
        check=True,
    )
    subprocess.run(["uv", "sync"], cwd=str(active_dir), check=True)


# ── deploy gates ──────────────────────────────────────────────────────────────

def run_entrypoint(staging_dir: Path, entrypoint: str, env: dict) -> bool:
    """Execute a flow by file:function entrypoint. Returns True on success."""
    file_path, func_name = entrypoint.rsplit(":", 1)
    code = (
        "import sys; sys.path.insert(0, '.'); "
        "import importlib.util; "
        f"spec = importlib.util.spec_from_file_location('_flow', r'{file_path}'); "
        "mod = importlib.util.module_from_spec(spec); "
        "spec.loader.exec_module(mod); "
        f"getattr(mod, '{func_name}')()"
    )
    return subprocess.run(
        ["uv", "run", "python", "-c", code],
        cwd=str(staging_dir),
        env=env,
    ).returncode == 0


def run_gates(
    staging_dir: Path,
    gate_names: list[str],
    all_flows: dict[str, FlowConfig],
    label: str,
    env: dict,
) -> bool:
    """Run named gate flows in order. Returns False on first failure."""
    for name in gate_names:
        if name not in all_flows:
            print(f"[poller] Unknown gate flow '{name}' ({label})")
            return False
        print(f"[poller] Gate [{label}] → {name}")
        if not run_entrypoint(staging_dir, all_flows[name].entrypoint, env):
            print(f"[poller] Gate [{label}] '{name}' FAILED — aborting deploy")
            return False
    return True


def all_gates_pass(
    staging_dir: Path,
    app_config: NexusConfig,
    app_name: str,
    extra_env: dict | None = None,
) -> bool:
    """Run all deploy gates for an app in staging. All must pass."""
    env = os.environ.copy()
    env["PREFECT_API_URL"] = PREFECT_API_URL
    if extra_env:
        env.update(extra_env)
    flows = app_config.flows

    # 1. Root gates
    if not run_gates(staging_dir, app_config.deploy, flows, app_name, env):
        return False

    # 2. Per-process gates
    for proc_name, proc in app_config.processes.items():
        label = f"{app_name}/{proc_name}"
        if not run_gates(staging_dir, proc.deploy, flows, label, env):
            return False

    # 3. Per-flow gates
    for flow_name, flow in app_config.flows.items():
        label = f"{app_name}/{flow_name}"
        if not run_gates(staging_dir, flow.deploy, flows, label, env):
            return False

    return True


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

def update_app(inc: IncludeConfig, nexus_home: Path = NEXUS_HOME) -> bool:
    active_dir = nexus_home / "apps" / inc.name
    staging_dir = nexus_home / "apps" / f"{inc.name}.next"

    if not active_dir.exists():
        return False

    try:
        fetch_ref(active_dir, inc.ref)
        prepare_staging(active_dir, staging_dir)
        subprocess.run(["uv", "sync"], cwd=str(staging_dir), check=True)

        app_nexus = staging_dir / "nexus.yaml"
        if not app_nexus.exists():
            print(f"[poller] No nexus.yaml in {inc.name}, skipping")
            _remove_worktree(active_dir, staging_dir)
            return False

        app_config = load_config(app_nexus)

        if not all_gates_pass(staging_dir, app_config, inc.name, extra_env=inc.env):
            _remove_worktree(active_dir, staging_dir)
            return False

        with_procs = bool(app_config.processes)
        processes = app_processes(inc.name) if with_procs else []

        for proc in processes:
            print(f"[poller]   stop {proc}")
            stop_process(proc)
        if processes:
            time.sleep(2)

        apply_update(active_dir)

        for proc in processes:
            print(f"[poller]   start {proc}")
            start_process(proc)

        _remove_worktree(active_dir, staging_dir)

        if app_config.flows:
            register_app_flows(inc.name, active_dir, app_config, PREFECT_API_URL)

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
    last_checked: dict[str, float] = {}
    registered: set[str] = set()
    print(f"[poller] Started (default interval: {DEFAULT_POLL_INTERVAL}s)")

    while True:
        try:
            config = load_config(config_file)
        except Exception as e:
            print(f"[poller] Failed to read config: {e}")
            time.sleep(DEFAULT_POLL_INTERVAL)
            continue

        now = time.monotonic()
        for inc in config.includes:
            active_dir = NEXUS_HOME / "apps" / inc.name
            if not active_dir.exists():
                continue

            # Register flows once on startup
            if inc.name not in registered:
                registered.add(inc.name)
                app_nexus = active_dir / "nexus.yaml"
                if app_nexus.exists():
                    try:
                        app_cfg = load_config(app_nexus)
                        register_app_flows(inc.name, active_dir, app_cfg, PREFECT_API_URL)
                    except Exception as e:
                        print(f"[poller] Startup registration error for {inc.name}: {e}")

            if now - last_checked.get(inc.name, 0) < inc.poll_interval:
                continue
            last_checked[inc.name] = now
            try:
                rhead = remote_head(active_dir, inc.ref)
                lhead = known.get(inc.name) or local_head(active_dir)
                if rhead and rhead != lhead:
                    print(f"[poller] Change in {inc.name}: {lhead[:8]} → {rhead[:8]}")
                    if update_app(inc):
                        known[inc.name] = rhead
                else:
                    known[inc.name] = lhead
            except Exception as e:
                print(f"[poller] Error checking {inc.name}: {e}")

        time.sleep(5)


if __name__ == "__main__":
    main()
