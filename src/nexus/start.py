"""Build the process-compose command from nexus.yaml and exec it."""
import os
import sys
from pathlib import Path

from nexus.config import load_config


def collect_compose_files(nexus_home: Path, nexus_src: Path) -> list[str]:
    """Return all process-compose files to merge, starting with nexus's own."""
    files = [str(nexus_src / "process-compose.yaml")]

    config_file = nexus_home / "config.yaml"
    config = load_config(config_file)

    # Root-level process files (relative to nexus_src)
    for _name, compose_file in config.processes.items():
        files.append(str(nexus_src / compose_file))

    # Each included app's process files
    for inc in config.includes:
        app_dir = nexus_home / "apps" / inc.name
        app_nexus = app_dir / "nexus.yaml"
        if not app_nexus.exists():
            # Fall back to a bare process-compose.yaml in the app root
            bare = app_dir / "process-compose.yaml"
            if bare.exists():
                files.append(str(bare))
            continue
        app_config = load_config(app_nexus)
        for _name, compose_file in app_config.processes.items():
            files.append(str(app_dir / compose_file))

    return files


def main():
    nexus_home = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
    nexus_src = Path(os.environ.get("NEXUS_SRC", Path(__file__).parent.parent.parent))

    compose_files = collect_compose_files(nexus_home, nexus_src)

    cmd = ["process-compose", "up"]
    for f in compose_files:
        cmd += ["-f", f]
    cmd += ["-p", "9080", "-t=false"]

    env = os.environ.copy()
    env["NEXUS_HOME"] = str(nexus_home)
    env["NEXUS_SRC"] = str(nexus_src)
    env["PREFECT_API_URL"] = "http://localhost:4200/api"

    # Inject per-app directory and base path env vars
    config = load_config(nexus_home / "config.yaml")
    for inc in config.includes:
        key = f"NEXUS_APP_{inc.name.upper().replace('-', '_')}_DIR"
        env[key] = str(nexus_home / "apps" / inc.name)
        env[f"NEXUS_BASE_PATH_{inc.name.upper().replace('-', '_')}"] = f"/{inc.name}"

    print(f"[start] Launching with {len(compose_files)} compose file(s)...")
    print("[start] Nexus UI  → http://localhost:8080")
    print("[start] Prefect   → http://localhost:4200")
    os.execvpe(cmd[0], cmd, env)


if __name__ == "__main__":
    main()
