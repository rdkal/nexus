"""Integration tests for install.sh and the nexus web server."""
import httpx
import pytest


def test_directory_structure(running_nexus):
    """install.sh creates expected directory layout."""
    assert (running_nexus / "config.yaml").exists(), "config.yaml missing"
    assert (running_nexus / "apps").is_dir(), "apps/ dir missing"


def test_config_content(running_nexus):
    """Copied config matches the fixture."""
    import yaml
    config = yaml.safe_load((running_nexus / "config.yaml").read_text())
    assert config["project"] == "nexus-test"
    assert "includes" not in config or config.get("includes") is None


def test_web_responds(running_nexus):
    resp = httpx.get("http://localhost:8080", timeout=10)
    assert resp.status_code == 200


def test_web_content_has_nexus_branding(running_nexus):
    resp = httpx.get("http://localhost:8080", timeout=10)
    assert "Nexus" in resp.text


def test_web_links_to_prefect(running_nexus):
    resp = httpx.get("http://localhost:8080", timeout=10)
    assert "Prefect" in resp.text
    assert "4200" in resp.text
