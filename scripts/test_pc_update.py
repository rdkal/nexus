#!/usr/bin/env python3
"""
Test process-compose `project update` behaviour.

Checks whether calling `project update -f file1 -f file2` to add or remove
compose files disrupts already-running processes that are unchanged.

Run with:  uv run python scripts/test_pc_update.py
"""
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import httpx

PC_PORT = 19080
PC_BASE = f"http://localhost:{PC_PORT}"
SETTLE = 0.6   # seconds to let PC settle after an update


# ── helpers ───────────────────────────────────────────────────────────────────

def _ok(msg):   print(f"  \033[32m✓\033[0m {msg}")
def _fail(msg): print(f"  \033[31m✗\033[0m {msg}")
def _info(msg): print(f"    {msg}")
def _section(title):
    print(f"\n\033[1;36m{'─'*60}\033[0m")
    print(f"\033[1;36m  {title}\033[0m")
    print(f"\033[1;36m{'─'*60}\033[0m")


def _wait_ready(timeout=20.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            r = httpx.get(f"{PC_BASE}/processes", timeout=2)
            if r.status_code == 200:
                return True
        except Exception:
            pass
        time.sleep(0.3)
    return False


def _procs() -> dict:
    r = httpx.get(f"{PC_BASE}/processes", timeout=5)
    return {p["name"]: p for p in r.json().get("data", [])}


def _wait_running(name: str, timeout=15.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if _procs().get(name, {}).get("is_running"):
            return True
        time.sleep(0.3)
    return False


def _wait_stopped(name: str, timeout=15.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        p = _procs().get(name, {})
        if not p.get("is_running"):
            return True
        time.sleep(0.3)
    return False


def _update(*files) -> subprocess.CompletedProcess:
    cmd = ["process-compose", "project", "update", "--port", str(PC_PORT)]
    for f in files:
        cmd += ["-f", str(f)]
    return subprocess.run(cmd, capture_output=True, text=True)


def _start_count(log_file: Path) -> int:
    if not log_file.exists():
        return 0
    return log_file.read_text().count("\n")


# ── main ──────────────────────────────────────────────────────────────────────

def main():
    pc_bin = shutil.which("process-compose")
    if not pc_bin:
        print("ERROR: process-compose not in PATH")
        sys.exit(1)

    tmpdir = Path(tempfile.mkdtemp(prefix="pc-update-test-"))
    log_a = tmpdir / "a.log"
    log_b = tmpdir / "b.log"
    log_c = tmpdir / "c.log"
    results: list[tuple[bool, str]] = []

    def check(condition: bool, msg: str):
        results.append((condition, msg))
        (_ok if condition else _fail)(msg)

    # ── compose files ─────────────────────────────────────────────────────────

    # Sentinel: always running, used as a baseline
    # proc-a:   writes a line to log_a on every (re)start
    compose_base = tmpdir / "base.yaml"
    compose_base.write_text(f"""\
version: "0.5"
processes:
  _sentinel:
    command: sleep 86400
  proc-a:
    command: sh -c 'date >> {log_a}; sleep 86400'
""")

    # proc-b: added later
    compose_b = tmpdir / "b.yaml"
    compose_b.write_text(f"""\
version: "0.5"
processes:
  proc-b:
    command: sh -c 'date >> {log_b}; sleep 86400'
""")

    # proc-a with a changed command (to test config-change restart)
    compose_base_changed = tmpdir / "base_changed.yaml"
    compose_base_changed.write_text(f"""\
version: "0.5"
processes:
  _sentinel:
    command: sleep 86400
  proc-a:
    command: sh -c 'date >> {log_a}; echo RESTARTED >> {log_a}; sleep 86400'
""")

    # proc-a with an unchanged command (same as original — tests no-op update)
    # (compose_base is reused)

    pc_proc = None
    try:
        # ── start ─────────────────────────────────────────────────────────────
        _section("1. Start process-compose with base.yaml (sentinel + proc-a)")

        pc_proc = subprocess.Popen(
            [pc_bin, "-f", str(compose_base), f"-p={PC_PORT}", "-t=false", "up"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )

        check(_wait_ready(), "process-compose API is reachable")
        check(_wait_running("proc-a"), "proc-a is running after start")
        check(_wait_running("_sentinel"), "_sentinel is running after start")

        time.sleep(SETTLE)
        count = _start_count(log_a)
        _info(f"proc-a start-log line count: {count}")

        def snapshot() -> int:
            time.sleep(SETTLE)
            return _start_count(log_a)

        def assert_no_restart(before: int, label: str) -> int:
            after = snapshot()
            check(after == before,
                  f"proc-a NOT restarted during: {label}  (lines: {before} → {after})")
            return after

        def assert_restarted(before: int, label: str) -> int:
            after = snapshot()
            check(after > before,
                  f"proc-a WAS restarted during: {label}  (lines: {before} → {after})")
            return after

        # ── add proc-b ────────────────────────────────────────────────────────
        _section("2. project update: base.yaml + b.yaml  (add proc-b)")

        before = _start_count(log_a)
        r = _update(compose_base, compose_b)
        _info(f"exit code: {r.returncode}  stderr: {r.stderr.strip()[:120] or '(none)'}")
        check(r.returncode == 0, "project update returned exit 0")
        check(_wait_running("proc-b"), "proc-b is running after update")
        check(_procs().get("_sentinel", {}).get("is_running", False),
              "_sentinel still running after adding proc-b")
        count = assert_no_restart(before, "adding proc-b")

        # ── remove proc-b ─────────────────────────────────────────────────────
        _section("3. project update: base.yaml only  (remove proc-b)")

        before = _start_count(log_a)
        r = _update(compose_base)
        _info(f"exit code: {r.returncode}  stderr: {r.stderr.strip()[:120] or '(none)'}")
        check(r.returncode == 0, "project update returned exit 0")
        check(_wait_stopped("proc-b"), "proc-b is stopped/removed after update")
        count = assert_no_restart(before, "removing proc-b")

        # ── no-op update (same config, unchanged) ────────────────────────────
        _section("4. project update: base.yaml again  (no-op / identical config)")

        before = _start_count(log_a)
        r = _update(compose_base)
        _info(f"exit code: {r.returncode}")
        check(r.returncode == 0, "project update returned exit 0")
        count = assert_no_restart(before, "no-op update")

        # ── add proc-b again (second time) ────────────────────────────────────
        _section("5. project update: base.yaml + b.yaml  (add proc-b again, second time)")

        before = _start_count(log_a)
        r = _update(compose_base, compose_b)
        _info(f"exit code: {r.returncode}")
        check(r.returncode == 0, "project update returned exit 0")
        check(_wait_running("proc-b"), "proc-b is running after second add")
        count = assert_no_restart(before, "adding proc-b a second time")

        # ── config change (proc-a command changes) ────────────────────────────
        _section("6. project update: base_changed.yaml + b.yaml  (proc-a command differs)")

        before = _start_count(log_a)
        r = _update(compose_base_changed, compose_b)
        _info(f"exit code: {r.returncode}")
        check(r.returncode == 0, "project update returned exit 0")
        count = assert_restarted(before, "proc-a config change")

        # Also confirm _sentinel still alive through all of this
        check(_procs().get("_sentinel", {}).get("is_running", False),
              "_sentinel survived all updates")

    finally:
        if pc_proc:
            try:
                os.killpg(os.getpgid(pc_proc.pid), signal.SIGTERM)
                pc_proc.wait(timeout=5)
            except Exception:
                pass
        shutil.rmtree(tmpdir, ignore_errors=True)

    # ── summary ───────────────────────────────────────────────────────────────
    _section("Summary")
    passed = sum(1 for ok, _ in results if ok)
    total = len(results)
    for ok, msg in results:
        (_ok if ok else _fail)(msg)
    print(f"\n  {passed}/{total} checks passed\n")

    sys.exit(0 if passed == total else 1)


if __name__ == "__main__":
    main()
