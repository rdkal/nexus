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
class FlowConfig:
    entrypoint: str                                    # file:function
    deploy: list[str] = field(default_factory=list)   # gate flow names


@dataclass
class ProcessConfig:
    file: str                                          # compose file path
    deploy: list[str] = field(default_factory=list)   # gate flow names


@dataclass
class NexusConfig:
    project: str | None
    includes: list[IncludeConfig]
    flows: dict[str, FlowConfig]
    processes: dict[str, ProcessConfig]
    deploy: list[str] = field(default_factory=list)   # root gates (flow names)


def _parse_flow(val: str | dict) -> FlowConfig:
    if isinstance(val, str):
        return FlowConfig(entrypoint=val)
    return FlowConfig(
        entrypoint=val["entrypoint"],
        deploy=val.get("deploy", []),
    )


def _parse_process(val: str | dict) -> ProcessConfig:
    if isinstance(val, str):
        return ProcessConfig(file=val)
    return ProcessConfig(
        file=val["file"],
        deploy=val.get("deploy", []),
    )


def load_config(path: Path) -> NexusConfig:
    data = yaml.safe_load(path.read_text()) or {}

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
        flows={k: _parse_flow(v) for k, v in data.get("flows", {}).items()},
        processes={k: _parse_process(v) for k, v in data.get("processes", {}).items()},
        deploy=data.get("deploy") or [],
    )
