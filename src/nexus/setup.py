"""Clone or update all app repos listed in config."""
import os
import subprocess
import sys
from pathlib import Path

from nexus.config import AppConfig, load_config


def clone_or_update(app: AppConfig, dest: Path) -> None:
    if dest.joinpath(".git").exists():
        print(f"  Updating {app.name}...")
        subprocess.run(["git", "-C", str(dest), "fetch", "origin"], check=True)
        subprocess.run(
            ["git", "-C", str(dest), "reset", "--hard", f"origin/{app.branch}"],
            check=True,
        )
    else:
        print(f"  Cloning {app.name} from {app.repo}...")
        dest.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "clone", "--branch", app.branch, app.repo, str(dest)],
            check=True,
        )


def main():
    if len(sys.argv) < 2:
        print("Usage: python -m nexus.setup <config-file>")
        sys.exit(1)

    config_file = Path(sys.argv[1])
    nexus_home = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))

    config = load_config(config_file)
    print(f"Setting up project: {config.project}")

    apps_dir = nexus_home / "apps"
    for app in config.apps:
        clone_or_update(app, apps_dir / app.name)

    print("Done.")


if __name__ == "__main__":
    main()
