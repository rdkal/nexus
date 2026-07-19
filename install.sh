#!/bin/sh
# Nexus installer.
#
# Usage:
#   curl -fsSL https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
#     --project github.com/myorg/system-a \
#     --project github.com/myorg/system-b:custom-name
#
# What it does:
#   1. Installs nexus-pm and nexus into $NEXUS_HOME/bin (prebuilt from a GitHub
#      release, or built from source as a fallback)
#   2. Creates the $NEXUS_HOME directory structure
#   3. Registers each --project repo
#   4. Installs and starts a user-mode service pointing at nexus-pm
#
# Requirements: git and curl on PATH. Go is only needed to build from source —
# a local checkout, a branch ref, --from-source, or when no prebuilt binary is
# available for the platform. No root required.

set -eu

NEXUS_MODULE="github.com/rdkal/nexus"
NEXUS_REPO_URL="https://github.com/rdkal/nexus"

# --- configuration (overridable via env or flags) ---
NEXUS_HOME="${NEXUS_HOME:-$HOME/.nexus}"
NEXUS_REF="${NEXUS_REF:-}"       # version to install (empty = latest release); a branch builds from source
NEXUS_SRC="${NEXUS_SRC:-}"       # optional path to a local nexus checkout to build from
install_service=1
from_source=0                    # force building from source instead of downloading
projects=""

usage() {
	cat <<'EOF'
nexus installer

Options:
  --project <spec-path[:name]>   Project repo to watch. Repeatable.
  --home <path>                  Install location (default: $HOME/.nexus).
  --ref <ref>                    nexus version to install (default: latest release).
                                 A version tag (v1.2.3) downloads that release;
                                 a branch/other ref builds from source.
  --from-source                  Build from source instead of downloading.
  --no-service                   Skip systemd/launchctl service setup.
  -h, --help                     Show this help.

Environment:
  NEXUS_HOME   Same as --home.
  NEXUS_REF    Same as --ref.
  NEXUS_SRC    Build from a local nexus checkout instead of downloading.
EOF
}

die() { echo "nexus install: $*" >&2; exit 1; }
info() { echo "==> $*"; }

# --- parse arguments ---
while [ $# -gt 0 ]; do
	case "$1" in
		--project) [ $# -ge 2 ] || die "--project needs a value"; projects="$projects $2"; shift 2 ;;
		--project=*) projects="$projects ${1#--project=}"; shift ;;
		--home) [ $# -ge 2 ] || die "--home needs a value"; NEXUS_HOME="$2"; shift 2 ;;
		--home=*) NEXUS_HOME="${1#--home=}"; shift ;;
		--ref) [ $# -ge 2 ] || die "--ref needs a value"; NEXUS_REF="$2"; shift 2 ;;
		--ref=*) NEXUS_REF="${1#--ref=}"; shift ;;
		--from-source) from_source=1; shift ;;
		--no-service) install_service=0; shift ;;
		-h|--help) usage; exit 0 ;;
		*) die "unknown argument: $1 (try --help)" ;;
	esac
done

# --- preflight ---
# git is needed at runtime (nexus clones and polls repos). Go is checked lazily,
# only if we end up building from source.
command -v git >/dev/null 2>&1 || die "'git' is required"

# NEXUS_HOME may contain ~ or be relative; resolve to an absolute path.
mkdir -p "$NEXUS_HOME"
NEXUS_HOME=$(cd "$NEXUS_HOME" && pwd)
BIN="$NEXUS_HOME/bin"

info "installing nexus to $NEXUS_HOME"
mkdir -p "$BIN" "$NEXUS_HOME/repos" "$NEXUS_HOME/volumes" "$NEXUS_HOME/logs"

# --- install the binaries ---

is_version() { printf '%s' "$1" | grep -q '^v[0-9]'; }

# detect_platform prints "<os>-<arch>" for the release asset names, or fails on
# an unsupported OS/arch.
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

# download_prebuilt fetches nexus + nexus-pm from a GitHub release. Returns
# non-zero (so the caller falls back to source) on any failure.
download_prebuilt() {
	platform=$(detect_platform) || { info "no prebuilt binaries for this OS/arch"; return 1; }
	command -v curl >/dev/null 2>&1 || { info "curl not found; cannot download prebuilt binaries"; return 1; }
	if is_version "$NEXUS_REF"; then
		base="$NEXUS_REPO_URL/releases/download/$NEXUS_REF"
		info "downloading prebuilt nexus $NEXUS_REF ($platform)"
	else
		base="$NEXUS_REPO_URL/releases/latest/download"
		info "downloading prebuilt nexus (latest release, $platform)"
	fi
	for b in nexus nexus-pm; do
		curl -fsSL "$base/$b-$platform" -o "$BIN/.$b.new" || { rm -f "$BIN/.$b.new"; return 1; }
		chmod +x "$BIN/.$b.new"
		mv -f "$BIN/.$b.new" "$BIN/$b"
	done
	return 0
}

# build_from_source builds with the Go toolchain — a local checkout, or the
# module at the requested ref (default: main).
build_from_source() {
	command -v go >/dev/null 2>&1 || die "no prebuilt binary available and 'go' is not on PATH — install Go >= 1.22, or pass a released --ref (e.g. --ref v1.0.0)"
	if [ -n "$NEXUS_SRC" ]; then
		info "building from local source: $NEXUS_SRC"
		( cd "$NEXUS_SRC" && GOBIN="$BIN" go install ./cmd/nexus ./cmd/nexus-pm )
	else
		src_ref="${NEXUS_REF:-main}"
		info "building $NEXUS_MODULE@$src_ref"
		GOBIN="$BIN" go install "$NEXUS_MODULE/cmd/nexus@$src_ref"
		GOBIN="$BIN" go install "$NEXUS_MODULE/cmd/nexus-pm@$src_ref"
	fi
}

# Decide how to install. Prefer prebuilt binaries; fall back to source.
use_source="$from_source"
if [ -n "$NEXUS_SRC" ]; then use_source=1; fi
# Running from inside a nexus checkout → build that local source.
if [ -z "$NEXUS_SRC" ] && [ -f "./go.mod" ] && grep -q "^module $NEXUS_MODULE\$" ./go.mod 2>/dev/null; then
	NEXUS_SRC="$(pwd)"; use_source=1
fi
# A non-version ref names a branch, which has no release — build it from source.
if [ "$use_source" -eq 0 ] && [ -n "$NEXUS_REF" ] && ! is_version "$NEXUS_REF"; then
	use_source=1
fi

installed=0
if [ "$use_source" -eq 0 ]; then
	if download_prebuilt; then installed=1; else info "falling back to building from source"; fi
fi
[ "$installed" -eq 1 ] || build_from_source

[ -x "$BIN/nexus" ] || die "install did not produce $BIN/nexus"
[ -x "$BIN/nexus-pm" ] || die "install did not produce $BIN/nexus-pm"
info "installed $BIN/nexus and $BIN/nexus-pm"

# --- register projects ---
for p in $projects; do
	info "registering project: $p"
	"$BIN/nexus" --home "$NEXUS_HOME" project add "$p"
done

# --- service setup ---
setup_systemd() {
	command -v systemctl >/dev/null 2>&1 || return 1
	# A user bus must be reachable for `systemctl --user` to work.
	systemctl --user show-environment >/dev/null 2>&1 || return 1

	unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
	mkdir -p "$unit_dir"
	cat > "$unit_dir/nexus.service" <<EOF
[Unit]
Description=Nexus process manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=NEXUS_HOME=$NEXUS_HOME
ExecStart=$BIN/nexus-pm
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
EOF

	systemctl --user daemon-reload
	systemctl --user enable --now nexus.service
	info "systemd user service 'nexus' installed and started"
	info "  status: systemctl --user status nexus"
	return 0
}

setup_launchd() {
	command -v launchctl >/dev/null 2>&1 || return 1

	agent_dir="$HOME/Library/LaunchAgents"
	mkdir -p "$agent_dir"
	plist="$agent_dir/com.rdkal.nexus.plist"
	cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.rdkal.nexus</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN/nexus-pm</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>NEXUS_HOME</key>
		<string>$NEXUS_HOME</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>$NEXUS_HOME/logs/nexus-pm.log</string>
	<key>StandardOutPath</key>
	<string>$NEXUS_HOME/logs/nexus-pm.log</string>
</dict>
</plist>
EOF

	launchctl unload "$plist" >/dev/null 2>&1 || true
	launchctl load "$plist"
	info "launchd agent 'com.rdkal.nexus' installed and started"
	return 0
}

manual_instructions() {
	cat <<EOF

Could not set up an automatic service on this system. To run nexus manually:

    NEXUS_HOME=$NEXUS_HOME $BIN/nexus-pm

To run it under your own init system, point it at: $BIN/nexus-pm
with NEXUS_HOME=$NEXUS_HOME in the environment.
EOF
}

if [ "$install_service" -eq 1 ]; then
	case "$(uname -s)" in
		Linux)  setup_systemd || manual_instructions ;;
		Darwin) setup_launchd || manual_instructions ;;
		*)      manual_instructions ;;
	esac
else
	info "skipping service setup (--no-service)"
	manual_instructions
fi

info "done"
