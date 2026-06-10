"""Unit tests for start.py — compose file collection and env var injection."""
from pathlib import Path

from nexus.config import IncludeConfig, NexusConfig, ProcessConfig
from nexus.start import build_env, collect_compose_files


def _config(**kwargs) -> NexusConfig:
    return NexusConfig(
        project=kwargs.get("project"),
        includes=kwargs.get("includes", []),
        flows=kwargs.get("flows", {}),
        processes=kwargs.get("processes", {}),
        deploy=kwargs.get("deploy", []),
    )


# ── collect_compose_files ─────────────────────────────────────────────────────

def test_always_includes_nexus_own_compose(tmp_path):
    nexus_src = tmp_path / "nexus"
    c = collect_compose_files(tmp_path, nexus_src, _config())
    assert c == [str(nexus_src / "process-compose.yaml")]


def test_includes_root_level_processes(tmp_path):
    nexus_src = tmp_path / "nexus"
    config = _config(processes={"infra": ProcessConfig(file="infra-compose.yaml")})
    files = collect_compose_files(tmp_path, nexus_src, config)
    assert str(nexus_src / "infra-compose.yaml") in files


def test_includes_app_processes_when_nexus_yaml_exists(tmp_path):
    nexus_src = tmp_path / "nexus"
    app_dir = tmp_path / "apps" / "api"
    app_dir.mkdir(parents=True)
    (app_dir / "nexus.yaml").write_text(
        "processes:\n  web: api-compose.yaml\n"
    )
    config = _config(includes=[IncludeConfig(name="api", repo="https://example.com/api")])
    files = collect_compose_files(tmp_path, nexus_src, config)
    assert str(app_dir / "api-compose.yaml") in files


def test_skips_app_without_nexus_yaml(tmp_path):
    nexus_src = tmp_path / "nexus"
    (tmp_path / "apps" / "api").mkdir(parents=True)
    config = _config(includes=[IncludeConfig(name="api", repo="https://example.com/api")])
    files = collect_compose_files(tmp_path, nexus_src, config)
    assert len(files) == 1  # only the nexus own compose


def test_skips_app_with_flows_only(tmp_path):
    nexus_src = tmp_path / "nexus"
    app_dir = tmp_path / "apps" / "workers"
    app_dir.mkdir(parents=True)
    (app_dir / "nexus.yaml").write_text("flows:\n  job: flows/job.py:run\n")
    config = _config(includes=[IncludeConfig(name="workers", repo="https://example.com/w")])
    files = collect_compose_files(tmp_path, nexus_src, config)
    assert len(files) == 1  # no compose file added for flows-only app


def test_multiple_apps_multiple_processes(tmp_path):
    nexus_src = tmp_path / "nexus"
    for name, compose in [("api", "api-compose.yaml"), ("jobs", "jobs-compose.yaml")]:
        d = tmp_path / "apps" / name
        d.mkdir(parents=True)
        (d / "nexus.yaml").write_text(f"processes:\n  web: {compose}\n")
    config = _config(includes=[
        IncludeConfig(name="api", repo="r"),
        IncludeConfig(name="jobs", repo="r"),
    ])
    files = collect_compose_files(tmp_path, nexus_src, config)
    assert str(tmp_path / "apps" / "api" / "api-compose.yaml") in files
    assert str(tmp_path / "apps" / "jobs" / "jobs-compose.yaml") in files


# ── build_env ─────────────────────────────────────────────────────────────────

def test_build_env_sets_nexus_home_and_src(tmp_path):
    nexus_home = tmp_path / "nexus_home"
    nexus_src = tmp_path / "nexus_src"
    env = build_env(nexus_home, nexus_src, _config())
    assert env["NEXUS_HOME"] == str(nexus_home)
    assert env["NEXUS_SRC"] == str(nexus_src)


def test_build_env_sets_prefect_api_url(tmp_path):
    env = build_env(tmp_path, tmp_path, _config())
    assert env["PREFECT_API_URL"] == "http://localhost:4200/api"


def test_build_env_injects_app_dir_per_include(tmp_path):
    nexus_home = tmp_path
    config = _config(includes=[IncludeConfig(name="api", repo="r")])
    env = build_env(nexus_home, tmp_path, config)
    assert env["NEXUS_APP_API_DIR"] == str(nexus_home / "apps" / "api")


def test_build_env_injects_base_path_per_include(tmp_path):
    config = _config(includes=[IncludeConfig(name="api", repo="r")])
    env = build_env(tmp_path, tmp_path, config)
    assert env["NEXUS_BASE_PATH_API"] == "/api"


def test_build_env_normalises_hyphen_to_underscore(tmp_path):
    config = _config(includes=[IncludeConfig(name="my-app", repo="r")])
    env = build_env(tmp_path, tmp_path, config)
    assert "NEXUS_APP_MY_APP_DIR" in env
    assert "NEXUS_BASE_PATH_MY_APP" in env


def test_build_env_no_includes_has_no_app_keys(tmp_path):
    env = build_env(tmp_path, tmp_path, _config())
    assert not any(k.startswith("NEXUS_APP_") for k in env)
    assert not any(k.startswith("NEXUS_BASE_PATH_") for k in env)
