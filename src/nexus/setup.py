"""Clone or update all included app repos."""
import os
import shutil
import subprocess
import sys
from pathlib import Path

from nexus.config import IncludeConfig, load_config


def _clone_urls(repo: str) -> list[str]:
    """Candidate git clone URLs for a schema-less repo identifier, in priority order."""
    if repo.startswith(("/", ".")):
        return [repo]  # local path — use as-is
    host, _, path = repo.partition("/")
    return [
        f"https://{repo}",
        f"git@{host}:{path}",
    ]


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
        last_err = None
        for url in _clone_urls(inc.repo):
            result = subprocess.run(
                ["git", "clone", "--branch", inc.ref, url, str(dest)],
                capture_output=True,
            )
            if result.returncode == 0:
                return
            shutil.rmtree(dest, ignore_errors=True)
            last_err = result.stderr.decode(errors="replace").strip()
        raise RuntimeError(f"Could not clone {inc.repo!r}: {last_err}")


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
