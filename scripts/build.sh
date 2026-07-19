#!/bin/sh
# Self-update build step for nexus.
#
# Invoked as the `build:` command in nexus's own nexus.yaml. It runs inside the
# worktree checked out at the new SHA (working directory = worktree root) with
# NEXUS_HOME and NEXUS_SHA in the environment. It installs the new nexus runtime
# and atomically swaps it into $NEXUS_HOME/bin/nexus.
#
# It prefers the prebuilt binary from the GitHub release for the tag at this
# commit, so hosts need no Go toolchain to self-update. It falls back to building
# from source when there is no release for the commit (e.g. a branch-tracked or
# untagged SHA) or the download is unavailable — that path requires Go.
#
# After this succeeds and the deployment is promoted, the nexus runtime asks
# nexus-pm to restart it (POST /runtime/restart), which loads the new binary.
# User services are never touched — nexus-pm holds their process handles.
#
# Only the nexus runtime is swapped here. nexus-pm is intentionally left alone:
# updating it is a separate, rare event that restarts everything.
set -eu

: "${NEXUS_HOME:?NEXUS_HOME must be set}"

NEXUS_REPO_URL="https://github.com/rdkal/nexus"
bin_dir="$NEXUS_HOME/bin"
mkdir -p "$bin_dir"

sha="${NEXUS_SHA:-$(git rev-parse HEAD 2>/dev/null || true)}"

# detect_platform prints "<os>-<arch>" matching the release asset names.
detect_platform() {
	_os=$(uname -s | tr '[:upper:]' '[:lower:]')
	_arch=$(uname -m)
	case "$_os" in linux|darwin) ;; *) return 1 ;; esac
	case "$_arch" in
		x86_64|amd64) _arch=amd64 ;;
		aarch64|arm64) _arch=arm64 ;;
		*) return 1 ;;
	esac
	printf '%s-%s' "$_os" "$_arch"
}

# Build to a temp file in the target directory, then rename over the live binary.
# rename(2) is atomic within a filesystem, so nexus-pm never observes a
# partially written binary even if it restarts the runtime mid-update.
tmp="$bin_dir/.nexus.new.$$"
trap 'rm -f "$tmp"' EXIT INT TERM

# Find a release tag whose commit is this SHA. For an annotated tag the peeled
# line (refs/tags/x^{}) carries the commit; a lightweight tag carries it directly.
tag=""
if command -v curl >/dev/null 2>&1 && [ -n "$sha" ]; then
	tag=$(git ls-remote --tags origin 2>/dev/null \
		| awk -v s="$sha" '$1 == s {print $2}' \
		| sed -e 's#refs/tags/##' -e 's/\^{}$//' \
		| head -n1)
fi
platform=$(detect_platform 2>/dev/null || true)

if [ -n "$tag" ] && [ -n "$platform" ] \
	&& curl -fsSL "$NEXUS_REPO_URL/releases/download/$tag/nexus-$platform" -o "$tmp"; then
	chmod +x "$tmp"
	mv -f "$tmp" "$bin_dir/nexus"
	trap - EXIT INT TERM
	echo "nexus build: installed prebuilt $tag ($platform)"
	exit 0
fi

# Fall back to building from source. Requires the Go toolchain.
command -v go >/dev/null 2>&1 || {
	echo "nexus build: no prebuilt release for this commit and 'go' is not available" >&2
	exit 1
}
go build -o "$tmp" ./cmd/nexus
chmod +x "$tmp"
mv -f "$tmp" "$bin_dir/nexus"
trap - EXIT INT TERM
echo "nexus build: built from source and swapped $bin_dir/nexus"
