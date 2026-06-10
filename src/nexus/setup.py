"""Clone or update all included app repos."""
import os
import subprocess
import sys
from pathlib import Path

from nexus.config import IncludeConfig, load_config


def clone_or_update(inc: IncludeConfig, dest: Path) -> None:
    if dest.joinpath(".git").exists():
        print(f"  Updating {inc.name}...")
        subprocess.run(
            ["git", "-C", str(dest), "fetch", "origin", inc.ref],
            check=True,
        )
        subprocess.run(
            ["git", "-C", str(dest), "reset", "--hard", "FETCH_HEAD"],
            check=True,
        )
    else:
        print(f"  Cloning {inc.name} from {inc.repo}...")
        dest.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "clone", "--branch", inc.ref, inc.repo, str(dest)],
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
    for inc in config.includes:
        clone_or_update(inc, apps_dir / inc.name)

    print("Done.")


if __name__ == "__main__":
    main()
