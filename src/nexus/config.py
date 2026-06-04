from dataclasses import dataclass, field
from pathlib import Path
import yaml


@dataclass
class AppConfig:
    name: str
    repo: str
    branch: str = "main"
    poll_interval: int = 60


@dataclass
class NexusConfig:
    project: str
    apps: list[AppConfig] = field(default_factory=list)


def load_config(path: Path) -> NexusConfig:
    data = yaml.safe_load(path.read_text())
    apps = [AppConfig(**a) for a in data.get("apps", [])]
    return NexusConfig(project=data["project"], apps=apps)
