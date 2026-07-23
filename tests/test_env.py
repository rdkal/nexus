"""
End-to-end tests for environment injection:

- docker-compose-style `environment:` on projects and services,
- a `.env` file next to nexus.yaml,
- the global NEXUS_<PROJECT>_<VOLUME> variable that lets one project reference
  another's volume path (the Traefik/Authelia pattern) with ${VAR} interpolation.
"""

import time

from conftest import GitRepo


def _wait_log(client, address, svc, needle, timeout=30):
    deadline = time.monotonic() + timeout
    body = ""
    while time.monotonic() < deadline:
        try:
            body = client.get_log(address, svc)
        except Exception:
            body = ""
        if needle in body:
            return body
        time.sleep(0.5)
    raise AssertionError(f"{needle!r} not in {address}/{svc} log; got:\n{body}")


PROJECT_AND_SERVICE_ENV = """\
environment:
  GREETING: hello
  SHARED: from_project
services:
  api:
    environment:
      - SVCVAR=world
      - SHARED=from_service
    run: sh -c 'echo GREETING=$GREETING SVCVAR=$SVCVAR SHARED=$SHARED; exec sleep 3600'
"""


def test_project_and_service_environment(nexus, git_repo):
    git_repo.commit({"nexus.yaml": PROJECT_AND_SERVICE_ENV})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    log = _wait_log(nexus.client, "app", "api", "GREETING=")
    assert "GREETING=hello" in log
    assert "SVCVAR=world" in log
    # Service-level environment overrides project-level for the same key.
    assert "SHARED=from_service" in log


DOTENV_YAML = """\
services:
  api:
    run: sh -c 'echo TOKEN=$TOKEN; exec sleep 3600'
"""


def test_dotenv_file_is_loaded(nexus, git_repo):
    git_repo.commit({"nexus.yaml": DOTENV_YAML, ".env": "TOKEN=abc123\n# comment\n"})
    nexus.add_project(git_repo.spec_path, "app")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("app")

    log = _wait_log(nexus.client, "app", "api", "TOKEN=")
    assert "TOKEN=abc123" in log


TRAEFIK_YAML = """\
volumes:
  dynamic: {}
services:
  proxy:
    run: sh -c 'echo traefik up; exec sleep 3600'
"""

# Authelia references Traefik's volume purely by the global env var, and remaps it
# to the name it actually consumes — no bind:, no hardcoded path.
AUTHELIA_YAML = """\
services:
  auth:
    environment:
      ROUTES_DIR: ${NEXUS_TRAEFIK_DYNAMIC}/authelia
    run: sh -c 'echo DYN=$NEXUS_TRAEFIK_DYNAMIC ROUTES_DIR=$ROUTES_DIR; exec sleep 3600'
"""


def test_cross_project_volume_env(nexus, git_repo, tmp_path):
    # Deploy Traefik first so its volume is known when Authelia deploys.
    git_repo.commit({"nexus.yaml": TRAEFIK_YAML})
    nexus.add_project(git_repo.spec_path, "traefik")
    nexus.start(poll_interval="2s")
    nexus.wait_for_socket()
    nexus.wait_for_sha("traefik")

    # Add Authelia live (separate repo, separate root project).
    authelia = GitRepo(tmp_path / "authelia")
    authelia.commit({"nexus.yaml": AUTHELIA_YAML})
    nexus.add_project(authelia.spec_path, "authelia")
    status, _ = nexus.client.reconcile()
    assert status == 202
    nexus.wait_for_sha("authelia", timeout=60)

    expected = str(nexus.home / "volumes" / "traefik" / "dynamic")
    log = _wait_log(nexus.client, "authelia", "auth", "DYN=")
    assert f"DYN={expected}" in log, log
    assert f"ROUTES_DIR={expected}/authelia" in log, log
