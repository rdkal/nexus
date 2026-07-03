#!/bin/sh
# Self-update build step for nexus.
#
# Invoked as the `build:` command in nexus's own nexus.yaml. It runs inside the
# worktree checked out at the new SHA (working directory = worktree root) with
# NEXUS_HOME in the environment. It rebuilds the nexus runtime and atomically
# swaps it into $NEXUS_HOME/bin/nexus.
#
# After this succeeds and the deployment is promoted, the nexus runtime asks
# nexus-pm to restart it (POST /runtime/restart), which loads the new binary.
# User services are never touched — nexus-pm holds their process handles.
#
# Only the nexus runtime is swapped here. nexus-pm is intentionally left alone:
# updating it is a separate, rare event that restarts everything.
set -eu

: "${NEXUS_HOME:?NEXUS_HOME must be set}"

command -v go >/dev/null 2>&1 || { echo "nexus build: 'go' is required" >&2; exit 1; }

bin_dir="$NEXUS_HOME/bin"
mkdir -p "$bin_dir"

# Build to a temp file in the target directory, then rename over the live binary.
# rename(2) is atomic within a filesystem, so nexus-pm never observes a
# partially written binary even if it restarts the runtime mid-build.
tmp="$bin_dir/.nexus.new.$$"
trap 'rm -f "$tmp"' EXIT INT TERM

go build -o "$tmp" ./cmd/nexus
chmod +x "$tmp"
mv -f "$tmp" "$bin_dir/nexus"
trap - EXIT INT TERM

echo "nexus build: swapped $bin_dir/nexus"
