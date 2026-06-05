from dataclasses import dataclass, field
from pathlib import Path
import yaml


@dataclass
class IncludeConfig:
    name: str
    repo: str
    branch: str = "main"
    poll_interval: int = 60


@dataclass
class NexusConfig:
    project: str | None
    includes: list[IncludeConfig]
    flows: dict[str, str]       # name → file:function entrypoint
    processes: dict[str, str]   # name → compose file path


def load_config(path: Path) -> NexusConfig:
    data = yaml.safe_load(path.read_text())

    includes = []
    for name, val in data.get("includes", {}).items():
        if isinstance(val, str):
            includes.append(IncludeConfig(name=name, repo=val))
        else:
            includes.append(IncludeConfig(
                name=name,
                repo=val["repo"],
                branch=val.get("branch", "main"),
                poll_interval=val.get("poll_interval", 60),
            ))

    return NexusConfig(
        project=data.get("project"),
        includes=includes,
        flows=data.get("flows", {}),
        processes=data.get("processes", {}),
    )
