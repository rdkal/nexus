"""Build the process-compose command from nexus.yaml and exec it."""
import os
from pathlib import Path

from nexus.config import NexusConfig, load_config


def collect_compose_files(nexus_home: Path, nexus_src: Path, config: NexusConfig) -> list[str]:
    files = [str(nexus_src / "process-compose.yaml")]

    for _name, proc in config.processes.items():
        files.append(str(nexus_src / proc.file))

    for inc in config.includes:
        app_dir = nexus_home / "apps" / inc.name
        app_nexus = app_dir / "nexus.yaml"
        if not app_nexus.exists():
            continue
        app_config = load_config(app_nexus)
        for _name, proc in app_config.processes.items():
            files.append(str(app_dir / proc.file))

    return files


def build_env(nexus_home: Path, nexus_src: Path, config: NexusConfig) -> dict:
    env = os.environ.copy()
    env["NEXUS_HOME"] = str(nexus_home)
    env["NEXUS_SRC"] = str(nexus_src)
    env["PREFECT_API_URL"] = "http://localhost:4200/api"
    for inc in config.includes:
        key = f"NEXUS_APP_{inc.name.upper().replace('-', '_')}_DIR"
        env[key] = str(nexus_home / "apps" / inc.name)
        env[f"NEXUS_BASE_PATH_{inc.name.upper().replace('-', '_')}"] = f"/{inc.name}"
    return env


def main():
    nexus_home = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
    nexus_src = Path(os.environ.get("NEXUS_SRC", Path(__file__).parent.parent.parent))

    config = load_config(nexus_home / "config.yaml")
    compose_files = collect_compose_files(nexus_home, nexus_src, config)

    cmd = ["process-compose", "up"]
    for f in compose_files:
        cmd += ["-f", f]
    cmd += ["-p", "9080", "-t=false"]

    env = build_env(nexus_home, nexus_src, config)

    print(f"[start] Launching with {len(compose_files)} compose file(s)...")
    print("[start] Nexus UI  → http://localhost:8080")
    print("[start] Prefect   → http://localhost:4200")
    os.execvpe(cmd[0], cmd, env)


if __name__ == "__main__":
    main()
