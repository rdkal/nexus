"""Build the process-compose command and exec it."""
import os
import sys
from pathlib import Path

from nexus.config import load_config


def main():
    nexus_home = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
    nexus_src = Path(os.environ.get("NEXUS_SRC", Path(__file__).parent.parent.parent))
    config_file = nexus_home / "config.yaml"

    config = load_config(config_file)

    compose_files = [str(nexus_src / "process-compose.yaml")]
    for app in config.apps:
        app_compose = nexus_home / "apps" / app.name / "process-compose.yaml"
        if app_compose.exists():
            compose_files.append(str(app_compose))
        else:
            print(f"[start] Warning: no process-compose.yaml in {app.name}, skipping")

    cmd = ["process-compose", "up"]
    for f in compose_files:
        cmd += ["-f", f]
    cmd += ["-p", "9080", "--tui=false"]

    env = os.environ.copy()
    env["NEXUS_HOME"] = str(nexus_home)
    env["NEXUS_SRC"] = str(nexus_src)
    env["PREFECT_API_URL"] = "http://localhost:4200/api"
    for app in config.apps:
        key = f"NEXUS_APP_{app.name.upper().replace('-', '_')}_DIR"
        env[key] = str(nexus_home / "apps" / app.name)

    print(f"[start] Launching process-compose with {len(compose_files)} file(s)...")
    print(f"[start] Nexus UI: http://localhost:8080")
    print(f"[start] Prefect UI: http://localhost:4200")
    os.execvpe(cmd[0], cmd, env)


if __name__ == "__main__":
    main()
