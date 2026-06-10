from dataclasses import dataclass, field
from pathlib import Path
import yaml


def _parse_repo(raw: str) -> tuple[str, str]:
    """Parse 'github.com/user/repo@ref' → (url, ref). Default ref is 'main'.

    Supports:
      github.com/user/repo           → https://github.com/user/repo,  ref=main
      github.com/user/repo@v1.2.3    → https://github.com/user/repo,  ref=v1.2.3
      https://github.com/user/repo   → unchanged (legacy full URL)
      git@github.com:user/repo       → unchanged (SSH URL)
      /absolute/path                 → local path
    """
    ref = "main"
    # Split @ref suffix, but leave git@host:path SSH URLs intact
    if "@" in raw and not raw.startswith("git@"):
        raw, ref = raw.rsplit("@", 1)
    # Prepend https:// when there is no scheme and it is not a local path
    if not raw.startswith(("/", ".", "https://", "http://", "git@")):
        raw = f"https://{raw}"
    return raw, ref


@dataclass
class IncludeConfig:
    name: str
    repo: str                                            # clean URL or local path
    ref: str = "main"                                    # branch, tag, or SHA
    poll_interval: int = 60
    env: dict[str, str] = field(default_factory=dict)   # injected into app processes


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
            repo, ref = _parse_repo(val)
            includes.append(IncludeConfig(name=name, repo=repo, ref=ref))
        else:
            repo, ref = _parse_repo(val["repo"])
            includes.append(IncludeConfig(
                name=name,
                repo=repo,
                ref=ref,
                poll_interval=val.get("poll_interval", 60),
                env=val.get("env") or {},
            ))

    return NexusConfig(
        project=data.get("project"),
        includes=includes,
        flows={k: _parse_flow(v) for k, v in data.get("flows", {}).items()},
        processes={k: _parse_process(v) for k, v in data.get("processes", {}).items()},
        deploy=data.get("deploy") or [],
    )
